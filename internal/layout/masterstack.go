package layout

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/mschulkind-oss/tilekeeper/internal/logging"
	"github.com/mschulkind-oss/tilekeeper/internal/sway"
)

// StackLayout controls how the stack area arranges windows.
type StackLayout int

const (
	StackSplitV   StackLayout = iota + 1 // Vertical splits (default)
	StackSplitH                          // Horizontal splits
	StackStacking                        // Stacking containers
	StackTabbed                          // Tabbed containers
)

// NextStackLayout cycles through stack layouts.
func (s StackLayout) NextStackLayout() StackLayout {
	switch s {
	case StackSplitV:
		return StackSplitH
	case StackSplitH:
		return StackStacking
	case StackStacking:
		return StackTabbed
	case StackTabbed:
		return StackSplitV
	default:
		return StackSplitV
	}
}

// String returns the sway layout command name.
func (s StackLayout) String() string {
	switch s {
	case StackSplitV:
		return "splitv"
	case StackSplitH:
		return "splith"
	case StackStacking:
		return "stacking"
	case StackTabbed:
		return "tabbed"
	}
	return "splitv"
}

// ParseStackLayout converts a string to a StackLayout.
func ParseStackLayout(s string) StackLayout {
	switch strings.ToLower(s) {
	case "splith":
		return StackSplitH
	case "stacking":
		return StackStacking
	case "tabbed":
		return StackTabbed
	default:
		return StackSplitV
	}
}

// Side controls which side the stack is on.
type Side int

const (
	SideRight Side = iota + 1
	SideLeft
)

// Opposite returns the other side.
func (s Side) Opposite() Side {
	if s == SideLeft {
		return SideRight
	}
	return SideLeft
}

// MoveDir returns the sway direction for moving toward this side.
func (s Side) MoveDir() string {
	if s == SideLeft {
		return "left"
	}
	return "right"
}

// ParseSide converts a string to a Side.
func ParseSide(s string) Side {
	if strings.ToLower(s) == "left" {
		return SideLeft
	}
	return SideRight
}

// MasterStackConfig configures a MasterStack layout manager.
type MasterStackConfig struct {
	MasterWidth       int
	StackLayout       StackLayout
	StackSide         Side
	VisibleStackLimit int
	MasterCount       int
}

// DefaultMasterStackConfig returns sensible defaults.
func DefaultMasterStackConfig() MasterStackConfig {
	return MasterStackConfig{
		MasterWidth:       50,
		StackLayout:       StackSplitV,
		StackSide:         SideRight,
		VisibleStackLimit: 3,
		MasterCount:       1,
	}
}

// MasterStack implements the classic master-stack tiling layout.
//
// One large "master" window on one side, with remaining windows
// stacked on the other. Supports multi-master, visible stack limit
// (substack), stack layout cycling, maximize toggle, and rotation.
type MasterStack struct {
	mu   sync.Mutex
	conn sway.Client

	config MasterStackConfig
	logger *slog.Logger // optional; nil silences manager logs

	// Window tracking — ordered list: [master(s), stack1, stack2, ...]
	windowIDs []int64

	// Runtime state
	substackExists      bool
	lastFocusedID       *int64
	maximized           bool
	masterWidthBefore   int
	lastKnownMasterPx   int
	extraNestingPending bool
}

// SetLogger attaches a component-scoped logger to this manager.
func (m *MasterStack) SetLogger(l *slog.Logger) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logger = l
}

// log returns the configured logger or slog.Default(). Never nil.
func (m *MasterStack) log() *slog.Logger {
	if m.logger == nil {
		return slog.Default()
	}
	return m.logger
}

// NewMasterStackManager creates a new MasterStack layout manager.
func NewMasterStackManager(conn sway.Client, cfg MasterStackConfig) *MasterStack {
	if cfg.MasterWidth <= 0 || cfg.MasterWidth >= 100 {
		cfg.MasterWidth = 50
	}
	if cfg.MasterCount < 1 {
		cfg.MasterCount = 1
	}
	return &MasterStack{
		conn:   conn,
		config: cfg,
	}
}

func (m *MasterStack) Name() string { return "MasterStack" }

// Config returns a snapshot of the manager's configuration. Used by
// fuzzer invariants that need to read MasterWidth without recomputing
// it from the workspace config tree.
func (m *MasterStack) Config() MasterStackConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.config
}

// Maximized reports whether the manager is in the maximize-toggled state
// (master folded into a tabbed stack column). Fuzzer invariants that
// assert the canonical master/stack split shape must skip maximized
// workspaces — the shared parent is the INTENDED maximized shape.
func (m *MasterStack) Maximized() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.maximized
}

func (m *MasterStack) WindowIDs() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]int64, len(m.windowIDs))
	copy(result, m.windowIDs)
	return result
}

// --- Event handlers ---

func (m *MasterStack) WindowAdded(ws *sway.Node, window *sway.Node) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if window == nil || window.Type != "con" {
		return nil
	}

	// THE EVENT PAYLOAD IS A SNAPSHOT, NOT THE TRUTH. window::new
	// containers are JSON frozen at emission time; with single-threaded
	// dispatch the window may have floated, fullscreened, or closed while
	// earlier events were being processed. The 2026-06-12 ctrl-s incident:
	// a portal save-dialog's window::new said "tiled", but sway had
	// already floated it — the master-insert dance then ran `swap` against
	// a floating container, which in sway exchanges positions INCLUDING
	// floating-list membership and emits no corrective event, silently
	// floating the dialog's tracked swap partner. The Hub fetched `ws`
	// fresh for this op, so the live node is one FindByID away.
	live := window
	if ws != nil {
		live = ws.FindByID(window.ID)
		if live == nil {
			// Closed (or moved off this workspace) between event emission
			// and processing. Sway attaches views before emitting
			// window::new, so absence means gone — never "not yet mapped".
			m.log().Debug("window-added: not in fresh tree, skipping",
				"con_id", window.ID, "name", window.Name)
			return nil
		}
	}

	// Only skip truly unmanageable windows (floating, fullscreen, non-con).
	// Parent-layout == stacked/tabbed is NOT a reason to skip: the parent
	// can be a MasterStack-owned stacking wrapper (in which case we must
	// track to own the window), or a user-owned stack that gets flattened
	// later (in which case skipping at birth leaves the window permanently
	// missed from tracking — the "step 191 window:new con=1069" fuzzer bug).
	// Let pushWindow position the window; a subsequent arrangeWindows or
	// binding can rearrange if needed.
	if live.IsFloating() || live.FullscreenMode == 1 {
		m.log().Debug("window excluded from layout",
			"con_id", idOf(live), "name", nameOf(live),
			"type", typeOf(live), "floating", floatingOf(live))
		return nil
	}

	// Already tracked: a floating→tiled return (user toggled a window
	// back, or a float we never saw leave). Sway re-tiles the container
	// at an arbitrary position (sibling of the inactive focus), so the
	// tree no longer matches the tracked order — a skip here leaves the
	// layout visibly scrambled (the 2026-06-12 incident's L8: the user's
	// manual rescue toggles fixed nothing). Rebuild instead; arrangeWindows
	// re-derives tracking from the tree, so it is also safe against the
	// historical self-move corruption this branch used to guard with a
	// pure no-op. Same fullscreen guard as ArrangeAll: rebuilding while a
	// leaf is fullscreen strands it as a sibling of the new splith
	// wrapper (the live `just deploy`-while-fullscreened bug).
	if m.indexOfID(window.ID) >= 0 {
		if ws == nil || hasFullscreenLeaf(ws) {
			m.log().Debug("window-added: already tracked, re-arrange deferred",
				"con_id", window.ID, "ws_nil", ws == nil)
			return nil
		}
		m.log().Info("window-added: already tracked, re-arranging",
			"con_id", window.ID, "tracked", append([]int64(nil), m.windowIDs...))
		return m.arrangeWindows(ws)
	}

	// Drop tracked ids that floated or vanished without an event (swap
	// bleed) BEFORE the dance — pushWindow must never pair the new window
	// with a silently-floated mark target or swap partner.
	m.reconcileWindows(ws)

	m.log().Debug("window-added: pushing",
		"con_id", live.ID, "name", live.Name, "app_id", live.AppID,
		"tracked_before", append([]int64(nil), m.windowIDs...))
	err := m.pushWindow(ws, live, nil)
	m.log().Debug("window-added: done",
		"con_id", live.ID, "tracked_after", append([]int64(nil), m.windowIDs...), "error", err)
	// Collapse any singleton wrappers our insert/swap/move sequence may
	// have left around (e.g. the outer splitv that used to hold siblings
	// of a substack whose members have all migrated away). Real sway does
	// not auto-collapse these; the no-wrapper-chain invariant catches
	// accumulated [splitv → stacked → leaf]-style chains otherwise.
	// Scoped to WindowAdded — arrangeWindows has its own tail flatten,
	// and flattening between loop iterations would dismantle the rebuild.
	if err == nil {
		m.flattenFreshForTracked()
	}
	return err
}

func (m *MasterStack) WindowRemoved(ws *sway.Node, window *sway.Node) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Drop any ids that floated or vanished without an event before
	// popWindow computes indices — its master-promotion and substack
	// rebalance issue move/swap commands against neighbors of the removed
	// id, and pairing those with a silently-floated window transfers
	// floatingness onward (the 2026-06-12 op=115 vector: substack promote
	// moved window 18 onto a mark held by silently-floating 17, floating
	// 18 too). The removed id itself is exempt: popWindow needs its index
	// to decide whether the master was the one removed.
	m.reconcileWindows(ws, window.ID)

	m.log().Debug("window-removed: popping",
		"con_id", window.ID, "name", window.Name,
		"tracked_before", append([]int64(nil), m.windowIDs...))
	err := m.popWindow(window)
	m.log().Debug("window-removed: done",
		"con_id", window.ID, "tracked_after", append([]int64(nil), m.windowIDs...), "error", err)
	return err
}

func (m *MasterStack) WindowFocused(_ *sway.Node, window *sway.Node) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if window != nil {
		id := window.ID
		m.lastFocusedID = &id
		logging.Trace(m.logger, "window-focused", "con_id", window.ID, "name", window.Name)
	}
	return nil
}

func (m *MasterStack) ArrangeAll(ws *sway.Node) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	leaves := 0
	if ws != nil {
		leaves = len(ws.Leaves())
	}
	// Skip when a workspace has a fullscreen leaf. Rebuilding the tiling
	// tree while the user is "in" a fullscreen window strands the
	// fullscreen leaf as a sibling of the new splith wrapper — the live
	// ws7 bug after `just deploy` while fullscreened. NOTE: there is
	// currently NO deferred re-arrange on fullscreen exit — the daemon's
	// shouldDispatch filters window::fullscreen_mode and handleWindowEvent
	// has no case for it; the next new/close/floating/binding op on the
	// workspace triggers the rebuild instead. Adding a fullscreen-exit
	// hook requires extending BOTH the daemon allow-list and the hub
	// switch in lockstep.
	if ws != nil && hasFullscreenLeaf(ws) {
		m.log().Info("arrange-all skipped (workspace has fullscreen leaf)",
			"workspace", wsName(ws), "leaves", leaves)
		// Still refresh tracking from the visible (non-fullscreen) leaves so
		// window-added/removed events between now and exit-fullscreen don't
		// act on stale ids.
		m.reconcileWindows(ws)
		return nil
	}
	m.log().Info("arrange-all",
		"workspace", wsName(ws), "leaves", leaves,
		"tracked_before", append([]int64(nil), m.windowIDs...),
		"masterWidth", m.config.MasterWidth, "masterCount", m.config.MasterCount,
		"stackLayout", m.config.StackLayout.String(), "stackSide", sideName(m.config.StackSide),
	)
	err := m.arrangeWindows(ws)
	m.log().Debug("arrange-all: done",
		"workspace", wsName(ws), "tracked_after", append([]int64(nil), m.windowIDs...), "error", err)
	return err
}

func (m *MasterStack) Command(cmd string, ws *sway.Node) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Drop any con_ids sway has since lost. Without this, a dropped
	// window::close event leaves stale entries in windowIDs, and
	// swap/move commands then target a vanished window — the
	// "Failed to find con_id 92" warning we saw on ws7.
	m.reconcileWindows(ws)

	m.log().Debug("command",
		"command", cmd, "workspace", wsName(ws),
		"tracked", append([]int64(nil), m.windowIDs...),
		"masterCount", m.config.MasterCount,
	)
	err := m.onCommand(cmd, ws)
	if err != nil {
		m.log().Error("command failed", "command", cmd, "error", err)
	}
	return err
}

// hasFullscreenLeaf returns true if any leaf under ws has FullscreenMode=1.
// Sway allows at most one per workspace; we scan the whole subtree regardless
// of position because `container_set_fullscreen` does not re-parent in our
// sim and we want consistent behavior against real sway too.
func hasFullscreenLeaf(ws *sway.Node) bool {
	if ws == nil {
		return false
	}
	for _, l := range ws.Leaves() {
		if l != nil && l.FullscreenMode == 1 {
			return true
		}
	}
	return false
}

func sideName(s Side) string {
	if s == SideLeft {
		return "left"
	}
	return "right"
}

func idOf(n *sway.Node) int64 {
	if n == nil {
		return 0
	}
	return n.ID
}
func nameOf(n *sway.Node) string {
	if n == nil {
		return ""
	}
	return n.Name
}
func typeOf(n *sway.Node) string {
	if n == nil {
		return ""
	}
	return n.Type
}
func floatingOf(n *sway.Node) string {
	if n == nil {
		return ""
	}
	return n.Floating
}

// --- Core algorithms ---

// arrangeWindows rebuilds the entire layout from the workspace tree.
func (m *MasterStack) arrangeWindows(ws *sway.Node) error {
	// Collect every leaf we're going to rebuild. IsExcluded is the right
	// filter for *passive* checks ("is this window ours to touch?"), but
	// arrangeWindows is an active rebuild — it dismantles foreign
	// wrappers (tabbed/stacked) via `move to workspace`, so parent-layout
	// doesn't belong in this filter. Using IsExcluded here caused a
	// subtle bug: cascadeFlatten mid-loop could free a previously-
	// excluded child from its wrapper into the workspace root, leaving
	// it visible but untracked.
	all := ws.Leaves()
	leaves := make([]*sway.Node, 0, len(all))
	for _, l := range all {
		if l == nil || l.Type != "con" {
			continue
		}
		if l.IsFloating() || l.FullscreenMode == 1 {
			continue
		}
		leaves = append(leaves, l)
	}
	if len(leaves) == 0 {
		// Workspace is empty (or has only excluded leaves). Reset tracking
		// — otherwise an arrange against a fully-emptied workspace would
		// leave windowIDs referencing already-closed containers, and the
		// next binding command would target nothing. Observed in the
		// drop-resync fuzzer: workspace tracked=[1029] but leaves=[].
		m.windowIDs = m.windowIDs[:0]
		m.substackExists = false
		return nil
	}

	// Flatten wrapper containers first: re-parent every leaf directly
	// under the workspace. Otherwise pushWindow(2nd) will issue `splith`
	// on whatever container currently wraps the master, stacking a new
	// wrapper on top of the old ones — ws7 hit four layers of splith
	// wrappers this way. Sway auto-deletes empty containers, so a clean
	// "workspace -> leaf" tree is what we start from.
	m.flattenWorkspace(ws, leaves)

	// Place focused window first (becomes master). Only promote leaves
	// we're actually rebuilding — floating/fullscreen focused leaves must
	// not get prepended. Parent-layout is irrelevant here (same reasoning
	// as the loop above).
	focused := ws.FindFocused()
	if focused != nil && focused.Type == "con" &&
		!focused.IsFloating() && focused.FullscreenMode != 1 {
		reordered := make([]*sway.Node, 0, len(leaves))
		reordered = append(reordered, focused)
		for _, l := range leaves {
			if l.ID != focused.ID {
				reordered = append(reordered, l)
			}
		}
		leaves = reordered
	}

	m.windowIDs = nil
	m.substackExists = false
	// A full rebuild always produces the canonical UNMAXIMIZED shape, so
	// any maximized state is gone after this — clear the flag or the next
	// `maximize` binding would run the unmaximize sequence against an
	// already-normal tree.
	m.maximized = false
	var prev *sway.Node
	for _, window := range leaves {
		if err := m.pushWindow(ws, window, prev); err != nil {
			return err
		}
		prev = window
	}

	m.tryRemoveExtraNesting(ws)

	// Second flatten pass on a FRESH tree. Our own pushWindow loop drains
	// the BEFORE-arrange outer wrappers: the original multi-child splitv
	// that held the pre-lost-master shape (e.g. live ws7's [236]) gets
	// reduced to a single child once every leaf has been moved into the
	// newly-built master/stack split. Real sway does NOT auto-collapse
	// singletons — container_reap_empty only reaps empties — so that
	// outer wrapper remains as a singleton on top of the rebuilt layout.
	//
	// The local `ws` parameter is stale by this point: sway.Conn.GetTree
	// returns a freshly unmarshalled tree, so every IPC command during
	// the loop left `ws` out of date. Walking `ws` here would miss the
	// singleton (it was multi-child when `ws` was captured) AND emit
	// `split none` on pre-arrange wrappers that no longer exist.
	// Re-fetching is the only way to catch wrappers created by our own
	// moves.
	m.flattenFreshByName(ws.Name)

	// Snapshot the master's pixel width so popWindow can restore it when
	// sway later fires a window::move that zeroes container.pending.width
	// (sway/commands/move.c:227).
	//
	// Derive it from config.MasterWidth% of the workspace rect — NOT from
	// the master leaf's Rect.Width in `ws`. `ws` is the tree the CALLER
	// captured before this op; every splith/move/resize command we issue
	// above runs after that snapshot, so the leaf rect here is the
	// PRE-arrange value. For a container-moved-in master that's its default
	// (e.g. 50%) width, so popWindow would restore the next master to the
	// wrong size. Live 2026-06-14: a 75% master was container-moved onto
	// ws7, then moved out, and popWindow restored the new master to 1280px
	// (50%) read from this stale snapshot. setMasterWidth (below) applies
	// exactly config.MasterWidth%, so the workspace rect is the reliable
	// source: it's the output size and doesn't change under our commands.
	if len(m.windowIDs) > 0 && ws.Rect.Width > 0 {
		m.lastKnownMasterPx = ws.Rect.Width * m.config.MasterWidth / 100
	}

	if len(m.windowIDs) >= 2 {
		return m.setMasterWidth()
	}
	return nil
}

// pushWindow adds a window to the layout at the correct position.
func (m *MasterStack) pushWindow(ws *sway.Node, window *sway.Node, positionAfter *sway.Node) error {
	positionAtIndex := 0
	if positionAfter != nil {
		idx := m.indexOf(positionAfter.ID)
		if idx >= 0 {
			positionAtIndex = idx + 1
		}
	} else if m.lastFocusedID != nil {
		idx := m.indexOfID(*m.lastFocusedID)
		if idx >= 0 {
			positionAtIndex = idx
		}
	}

	var needsMasterWidth bool

	switch {
	case len(m.windowIDs) == 0:
		// First window — nothing to arrange

	case len(m.windowIDs) == 1:
		// Second window — create the master/stack split
		existingID := m.windowIDs[0]
		if ws.FindByID(existingID) == nil {
			m.windowIDs = nil
			break
		}

		var masterID, stackID int64
		if positionAtIndex == 0 {
			masterID = window.ID
			stackID = existingID
		} else {
			masterID = existingID
			stackID = window.ID
		}

		if m.config.StackSide == SideLeft {
			m.runCmd("[con_id=%d] splith", stackID)
			m.moveWindow(masterID, stackID)
		} else {
			m.runCmd("[con_id=%d] splith", masterID)
			m.moveWindow(stackID, masterID)
		}
		m.runCmd("[con_id=%d] splitv", stackID)

	default:
		// Third+ window — insert into stack
		if err := m.insertAtIndex(ws, window, positionAtIndex); err != nil {
			return err
		}
	}

	// Rebalance substack if inserting before visible limit boundary
	if m.substackExists && positionAtIndex < m.config.VisibleStackLimit {
		if err := m.promoteFromSubstack(); err != nil {
			return err
		}
	}

	// Insert into tracking list
	if positionAtIndex >= len(m.windowIDs) {
		m.windowIDs = append(m.windowIDs, window.ID)
	} else {
		m.windowIDs = append(m.windowIDs, 0)
		copy(m.windowIDs[positionAtIndex+1:], m.windowIDs[positionAtIndex:])
		m.windowIDs[positionAtIndex] = window.ID
	}

	m.createSubstackIfNeeded()

	if len(m.windowIDs) == 2 {
		m.setStackLayout()
		m.tryRemoveExtraNesting(ws)
		needsMasterWidth = true
	} else if positionAtIndex == 0 && len(m.windowIDs) > 2 {
		needsMasterWidth = true
	}

	if m.extraNestingPending && len(m.windowIDs) > 2 {
		if m.removeExtraNesting(ws) {
			m.extraNestingPending = false
			needsMasterWidth = true
		}
	}

	if needsMasterWidth {
		return m.setMasterWidth()
	}
	return nil
}

// insertAtIndex handles inserting the 3rd+ window at the right position.
func (m *MasterStack) insertAtIndex(ws *sway.Node, window *sway.Node, idx int) error {
	switch {
	case idx == 0:
		// New master — swap into master position
		m.swapWindows(window.ID, m.windowIDs[0])
		m.moveWindow(m.windowIDs[0], m.windowIDs[1])
		m.swapWindows(m.windowIDs[0], m.windowIDs[1])

	case idx == 1:
		// New top of stack
		m.moveWindow(window.ID, m.windowIDs[idx])
		m.swapWindows(window.ID, m.windowIDs[idx])

	case m.substackExists && idx == m.config.VisibleStackLimit:
		// New first substack entry
		m.moveWindow(window.ID, m.windowIDs[idx])
		m.swapWindows(window.ID, m.windowIDs[idx])

	default:
		// General insertion
		if idx > 0 && idx <= len(m.windowIDs) {
			m.moveWindow(window.ID, m.windowIDs[idx-1])
		}
	}
	return nil
}

// flattenFreshForTracked re-fetches the tree and flattens the workspace
// that owns any of the manager's tracked windows. Used by popWindow
// which has no `ws` parameter. Returns silently if no tracked window is
// found in the fresh tree (e.g. every tracked window was just closed).
func (m *MasterStack) flattenFreshForTracked() {
	if len(m.windowIDs) == 0 {
		return
	}
	fresh, err := m.conn.GetTree()
	if err != nil || fresh == nil {
		return
	}
	for _, id := range m.windowIDs {
		node := fresh.FindByID(id)
		if node == nil {
			continue
		}
		if ws := node.FindWorkspace(); ws != nil {
			m.flattenWorkspace(ws, nil)
			return
		}
	}
}

// flattenFreshByName re-fetches the tree and flattens the workspace with
// the given name. Used after a sequence of IPC commands where the caller's
// `ws` snapshot has gone stale — our own splith/splitv wrappers become
// singletons once the stack dwindles, and sway does not auto-collapse
// those (container_reap_empty only reaps empties). Walking a stale tree
// would miss the new singletons.
func (m *MasterStack) flattenFreshByName(name string) {
	fresh, err := m.conn.GetTree()
	if err != nil || fresh == nil {
		return
	}
	for _, freshWS := range fresh.Workspaces() {
		if freshWS.Name == name {
			m.flattenWorkspace(freshWS, nil)
			return
		}
	}
}

// popWindow removes a window from the layout.
func (m *MasterStack) popWindow(window *sway.Node) error {
	idx := m.indexOf(window.ID)
	if idx < 0 {
		return nil
	}

	m.windowIDs = append(m.windowIDs[:idx], m.windowIDs[idx+1:]...)

	// If master was removed and we have 2+ windows, promote.
	//
	// Preserve master width using the px snapshot captured during the last
	// arrangeWindows pass. Reading window.Rect.Width directly here looks
	// attractive but is dead code for the move path: sway zeroes
	// pending.width/height before firing window::move, so any event-driven
	// pop of a moved-out master sees Width=0. The pre-rebuild snapshot in
	// arrangeWindows is the one reliable source of the user-visible master
	// pixel width. If we have no snapshot yet (first pop before any
	// arrange), fall back to the configured ppt.
	if idx == 0 && len(m.windowIDs) >= 2 {
		dir := m.config.StackSide.Opposite().MoveDir()
		m.runCmd("[con_id=%d] move %s", m.windowIDs[0], dir)
		if m.lastKnownMasterPx > 0 {
			m.runCmd("[con_id=%d] resize set width %d px", m.windowIDs[0], m.lastKnownMasterPx)
		} else {
			m.runCmd("[con_id=%d] resize set width %d ppt", m.windowIDs[0], m.config.MasterWidth)
		}
	}

	// Rebalance substack
	if m.substackExists {
		if idx < m.config.VisibleStackLimit && len(m.windowIDs) > m.config.VisibleStackLimit-1 {
			// Promote from substack to visible stack
			lastVisible := m.windowIDs[m.config.VisibleStackLimit-2]
			firstSub := m.windowIDs[m.config.VisibleStackLimit-1]
			m.moveWindow(firstSub, lastVisible)
		}

		if !m.shouldSubstackExist() {
			m.destroySubstack()
		}
	}

	// Flatten any singleton wrappers our pushWindow left behind. When the
	// stack dwindles to one window, the W_splitv wrapper around it becomes
	// a singleton; when master is then removed, the outer W_splith also
	// becomes a singleton — the classic [splith splitv] 2-chain the
	// fuzzer's no-wrapper-chain invariant catches. Real sway doesn't
	// collapse these on its own, so flatten explicitly.
	m.flattenFreshForTracked()

	// "Lost master" recovery: if master was popped, the new master may have
	// been buried inside the stack-column wrapper by a prior pushWindow
	// (case idx==0 swaps the old master into mark-on-stack[0], landing it
	// in the stack column). `move <new-master> <opposite-of-stack-side>`
	// above is supposed to extract it, but it only fires the directional
	// move primitive — real sway's `move left` requires a horizontal
	// parent to bubble up to, and the sim's moveDir is intra-parent only.
	// Either way, the new master can stay inside the stack column —
	// observed live as the 2026-05-31 ws7 "3 stripes + substack" shape
	// when a 1Password popup spawned, was tile-classified, then
	// re-classified as floating ~16ms later. Detect (new master and
	// stack[0] share a parent) and rebuild via arrangeWindows. The
	// existing TestArrangeOnLiveWs7ReproducesLostMaster proves
	// arrangeWindows can recover this shape.
	if idx == 0 && len(m.windowIDs) >= 2 {
		m.recoverLostMaster()
	}

	return nil
}

// recoverLostMaster re-fetches the workspace and rebuilds via
// arrangeWindows when the new master shares a "con" wrapper parent with
// stack[0] — i.e. it's buried inside what should be the stack column.
// Caller must hold m.mu. No-op if the structure is healthy.
//
// Healthy 2+ window MasterStack shape:
//
//	outer splith wrapper
//	├── master leaf
//	└── stack column (splitv/stacked/tabbed)
//	    └── stack windows
//
// master.Parent == outer-splith. stack[0].Parent == stack-column. They
// differ. Lost-master shape (live 2026-05-31 ws7 bug after 1Password
// popup new→floating→close): master ends up inside the stack column
// alongside the other stack windows, so master.Parent == stack[0].Parent
// and both point at a "con" wrapper.
//
// Mock-test caveat: tests with command-recording mocks don't mutate the
// tree on splith/splitv/move/swap commands, so the tree never reflects
// the manager-built split. In those trees master and stack[0] are
// workspace-direct siblings and share the workspace as parent. Filtering
// to non-workspace shared parents skips that artifact (workspace-as-
// shared-parent only arises in the mock or in a pre-split transient that
// arrangeWindows will fix on its next pass anyway).
func (m *MasterStack) recoverLostMaster() {
	fresh, err := m.conn.GetTree()
	if err != nil || fresh == nil {
		return
	}
	// Probe the first two tracked ids whose fresh nodes are TILED. A
	// floating tracked id (swap-transferred floatingness — the 2026-06-12
	// incident) would make the structural compare read the wrong nodes
	// and report "healthy" while the tiled layout is scrambled; during
	// the live incident this probe was fooled exactly that way and never
	// fired.
	var masterNode, stackTop *sway.Node
	for _, id := range m.windowIDs {
		n := fresh.FindByID(id)
		if n == nil || n.IsFloating() {
			continue
		}
		if masterNode == nil {
			masterNode = n
		} else {
			stackTop = n
			break
		}
	}
	if masterNode == nil || stackTop == nil {
		return
	}
	if masterNode.Parent == nil || stackTop.Parent == nil {
		return
	}
	if masterNode.Parent != stackTop.Parent {
		return // healthy: master and stack column are siblings
	}
	if masterNode.Parent.Type != "con" {
		return // workspace-direct shared parent — mock or pre-split transient
	}
	ws := masterNode.FindWorkspace()
	if ws == nil {
		return
	}
	// Same fullscreen guard as ArrangeAll: a rebuild while the user is
	// "in" a fullscreen window strands the fullscreen leaf.
	if hasFullscreenLeaf(ws) {
		m.log().Info("lost-master recovery deferred (workspace has fullscreen leaf)",
			"workspace", ws.Name)
		return
	}
	m.log().Warn("lost-master recovery: rebuilding via arrangeWindows",
		"workspace", ws.Name, "master", m.windowIDs[0],
		"stack_top", m.windowIDs[1],
		"shared_parent", masterNode.Parent.ID,
		"tracked", append([]int64(nil), m.windowIDs...))
	if err := m.arrangeWindows(ws); err != nil {
		m.log().Error("lost-master recovery: arrangeWindows failed",
			"workspace", ws.Name, "error", err)
	}
}

// --- Substack management ---

func (m *MasterStack) shouldSubstackExist() bool {
	return m.config.StackLayout == StackSplitV &&
		m.config.VisibleStackLimit > 0 &&
		len(m.windowIDs) > m.config.VisibleStackLimit+m.config.MasterCount
}

func (m *MasterStack) createSubstackIfNeeded() {
	if !m.shouldSubstackExist() || m.substackExists {
		return
	}

	limit := m.config.VisibleStackLimit + m.config.MasterCount - 1
	if limit >= len(m.windowIDs) {
		return
	}

	firstSubID := m.windowIDs[limit]
	m.runCmd("[con_id=%d] splitv, layout stacking", firstSubID)

	// Move all remaining windows into substack (in reverse order for correct ordering)
	for i := len(m.windowIDs) - 1; i > limit; i-- {
		m.moveWindow(m.windowIDs[i], firstSubID)
	}
	m.substackExists = true
}

func (m *MasterStack) destroySubstack() {
	if !m.substackExists {
		return
	}

	limit := m.config.VisibleStackLimit + m.config.MasterCount - 1
	if limit >= len(m.windowIDs) {
		m.substackExists = false
		return
	}

	lastVisible := m.windowIDs[limit-1]
	for i := len(m.windowIDs) - 1; i >= limit; i-- {
		m.moveWindow(m.windowIDs[i], lastVisible)
	}
	m.substackExists = false
}

func (m *MasterStack) promoteFromSubstack() error {
	limit := m.config.VisibleStackLimit + m.config.MasterCount - 1
	if limit >= len(m.windowIDs) || limit < 1 {
		return nil
	}
	lastVisible := m.windowIDs[limit-1]
	firstSub := m.windowIDs[limit]
	m.moveWindow(lastVisible, firstSub)
	m.swapWindows(lastVisible, firstSub)
	return nil
}

// --- Commands ---

func (m *MasterStack) onCommand(cmd string, ws *sway.Node) error {
	switch cmd {
	case "swap-master":
		return m.swapMaster(ws)
	case "rotate cw":
		return m.rotateWindows(ws, 1)
	case "rotate ccw":
		return m.rotateWindows(ws, -1)
	case "stack toggle":
		return m.toggleStackLayout()
	case "stack side-toggle":
		return m.toggleStackSide(ws)
	case "maximize":
		return m.toggleMaximize(ws)
	case "focus master":
		return m.focusMaster()
	case "focus previous":
		return m.focusPrevious()
	case "focus up":
		return m.focusRelative(ws, -1)
	case "focus down":
		return m.focusRelative(ws, 1)
	case "focus left":
		return m.focusHorizontal(ws, SideLeft)
	case "focus right":
		return m.focusHorizontal(ws, SideRight)
	case "move up":
		return m.moveRelative(ws, -1)
	case "move down":
		return m.moveRelative(ws, 1)
	case "move left":
		return m.moveHorizontal(ws, SideLeft)
	case "move right":
		return m.moveHorizontal(ws, SideRight)
	case "master add":
		return m.adjustMasterCount(1, ws)
	case "master remove":
		return m.adjustMasterCount(-1, ws)
	case "master grow":
		return m.adjustMasterWidth(5)
	case "master shrink":
		return m.adjustMasterWidth(-5)
	default:
		return fmt.Errorf("unknown command: %s", cmd)
	}
}

// adjustMasterCount changes how many windows occupy the master column
// and re-arranges. Clamped to [1, len(windowIDs)-1] so there is always
// at least one stack window.
func (m *MasterStack) adjustMasterCount(delta int, ws *sway.Node) error {
	newCount := m.config.MasterCount + delta
	if newCount < 1 {
		newCount = 1
	}
	if len(m.windowIDs) > 0 && newCount >= len(m.windowIDs) {
		newCount = len(m.windowIDs) - 1
		if newCount < 1 {
			newCount = 1
		}
	}
	if newCount == m.config.MasterCount {
		return nil
	}
	m.config.MasterCount = newCount
	return m.arrangeWindows(ws)
}

// focusPrevious moves focus to the most recently focused window.
func (m *MasterStack) focusPrevious() error {
	if m.lastFocusedID == nil {
		return nil
	}
	m.runCmd("[con_id=%d] focus", *m.lastFocusedID)
	return nil
}

// focusHorizontal moves focus across the master/stack divide.
//
// Heading from the master INTO the stack, it names the top of the stack
// explicitly rather than letting sway pick. Sway's directional focus
// descends into a container via that container's focus history
// (seat_get_focus_inactive), not its first child, so a native `focus
// <stack side>` lands on whichever stack window was touched last —
// arbitrary, and usually somewhere in the middle. Verified against real
// headless sway: master=5, column=[6 top, 7 middle, 8 bottom]; touch 7,
// return to master, `focus right` → 7.
//
// The top of the stack is where MRU promotion parks the window that just
// left master (see swapMaster), so pinning focus there is what makes
// focus-into-stack + swap-master alternate between the same two windows.
// A focus-history landing breaks that cycle by dragging in a third.
//
// Every other direction still falls through to native focus: the master
// column is not the edge of the world, and sway is what carries focus
// across outputs and into neighbouring workspaces.
func (m *MasterStack) focusHorizontal(ws *sway.Node, toSide Side) error {
	native := func() error {
		m.runCmd("focus %s", toSide.MoveDir())
		return nil
	}
	if len(m.windowIDs) < 2 || ws == nil {
		return native()
	}
	focused := ws.FindFocused()
	if focused == nil {
		return native()
	}
	idx := m.indexOf(focused.ID)
	// Untracked (floating, foreign) window, or not heading stackward from a
	// master — sway's own notion of "that way" is the better answer.
	if idx < 0 || idx >= m.config.MasterCount || toSide != m.config.StackSide {
		return native()
	}
	// windowIDs is [master(s)..., stack...], so the stack's top sits at
	// MasterCount. Guarded by the len >= 2 check plus adjustMasterCount
	// keeping MasterCount <= len-1.
	top := m.config.MasterCount
	if top >= len(m.windowIDs) {
		return native()
	}
	m.runCmd("[con_id=%d] focus", m.windowIDs[top])
	return nil
}

// moveHorizontal implements direction-aware horizontal movement for the
// focused window, using the conventional tiling-WM semantics so existing
// muscle memory carries over:
//
//   - master moving toward the stack side → becomes top of stack
//   - stack window moving away from the stack side → becomes master
//   - otherwise no-op (already at the edge in that direction)
//
// For splith/tabbed stacks the stack is horizontal, so we fall back to
// moveRelative (±1 within the list).
func (m *MasterStack) moveHorizontal(ws *sway.Node, toSide Side) error {
	if len(m.windowIDs) < 2 {
		return nil
	}
	focused := ws.FindFocused()
	if focused == nil {
		return nil
	}
	srcIdx := m.indexOf(focused.ID)
	if srcIdx < 0 {
		return nil
	}
	isMaster := srcIdx == 0

	if m.config.StackLayout == StackSplitH || m.config.StackLayout == StackTabbed {
		// Horizontal stack: left/right = prev/next in list.
		delta := 1
		if toSide == SideLeft {
			delta = -1
		}
		return m.moveRelative(ws, delta)
	}

	// Vertical stack (splitv / stacking): only two meaningful moves.
	switch {
	case m.config.StackSide == toSide && isMaster:
		// Master pushed toward the stack side → top of stack.
		return m.moveWindowToIndex(focused, srcIdx, 1)
	case m.config.StackSide != toSide && !isMaster:
		// Stack window pushed away from the stack side → becomes master.
		return m.moveWindowToIndex(focused, srcIdx, 0)
	}
	return nil
}

// swapMaster promotes the focused window to master with MRU (alt-tab)
// ordering: the old master becomes the top of the stack and the windows
// the promoted one passed each shift down by one. It deliberately does
// not trade the two windows' places.
//
// The MRU order is what makes promote/focus cycles stable: after a
// promotion the previous master sits at the top of the stack, which is
// where `focus <stack side>` lands, so focus-then-promote alternates
// between the same two windows instead of dragging a third one in. Under
// swap semantics the old master was exiled to the promoted window's old
// slot, so the cycle partner moved every time.
func (m *MasterStack) swapMaster(ws *sway.Node) error {
	if len(m.windowIDs) < 2 {
		return nil
	}

	focused := ws.FindFocused()
	if focused == nil {
		return nil
	}

	idx := m.indexOf(focused.ID)
	if idx <= 0 {
		return nil
	}

	// Rotate windowIDs[0:idx+1] right by one, bubbling the focused window
	// up through adjacent swaps. Adjacent swaps (rather than one move)
	// keep every step a sibling exchange, including the one that crosses
	// the master/stack boundary.
	for i := idx; i > 0; i-- {
		m.swapWindows(m.windowIDs[i-1], m.windowIDs[i])
		m.windowIDs[i-1], m.windowIDs[i] = m.windowIDs[i], m.windowIDs[i-1]
	}
	return m.setMasterWidth()
}

func (m *MasterStack) rotateWindows(_ *sway.Node, direction int) error {
	if len(m.windowIDs) < 2 {
		return nil
	}

	if direction > 0 {
		// CW: last → first
		last := m.windowIDs[len(m.windowIDs)-1]
		m.windowIDs = append([]int64{last}, m.windowIDs[:len(m.windowIDs)-1]...)
	} else {
		// CCW: first → last
		first := m.windowIDs[0]
		m.windowIDs = append(m.windowIDs[1:], first)
	}

	// Re-apply from scratch by swapping to correct positions
	for i := 0; i < len(m.windowIDs)-1; i++ {
		m.swapWindows(m.windowIDs[i], m.windowIDs[i+1])
	}
	return m.setMasterWidth()
}

func (m *MasterStack) toggleStackLayout() error {
	m.config.StackLayout = m.config.StackLayout.NextStackLayout()
	m.setStackLayout()
	return nil
}

func (m *MasterStack) toggleStackSide(ws *sway.Node) error {
	m.config.StackSide = m.config.StackSide.Opposite()
	return m.arrangeWindows(ws)
}

// toggleMaximize folds the master into the stack column and tabs the
// whole stack — so the focused window covers the workspace, with a tab
// bar listing every other tracked window. Unmaximize reverses the
// specific sequence (not a full arrangeWindows rebuild) to restore the
// master column at its previous pixel width.
//
// The old implementation just called `[master] layout tabbed` in place
// and then triggered
// arrangeWindows on unmax — which set the master's *parent* (a 2-child
// splith wrapper) to tabbed, producing an outer 2-tab bar instead of
// real fullscreen, and then scrambled windows on every subsequent press
// because arrangeWindows was rebuilding on top of a tabbed wrapper.
func (m *MasterStack) toggleMaximize(ws *sway.Node) error {
	if len(m.windowIDs) < 2 {
		return nil
	}

	m.maximized = !m.maximized

	if m.maximized {
		// Capture the master's current pixel width so unmax can restore
		// it exactly. lastKnownMasterPx is snapshotted during the last
		// arrangeWindows; fall back to a live tree read if we have no
		// snapshot yet.
		if m.lastKnownMasterPx > 0 {
			m.masterWidthBefore = m.lastKnownMasterPx
		} else if master := ws.FindByID(m.windowIDs[0]); master != nil && master.Rect.Width > 0 {
			m.masterWidthBefore = master.Rect.Width
		}

		m.destroySubstack()

		// Fold master into the stack column: move it next to the first
		// stack window, then swap so it ends up at the top of the stack.
		m.moveWindow(m.windowIDs[0], m.windowIDs[1])
		m.swapWindows(m.windowIDs[0], m.windowIDs[1])

		// Tab the stack column — master is now inside it, so this sets
		// stackCol.layout = tabbed, giving a full-width tab bar.
		m.runCmd("[con_id=%d] layout tabbed", m.windowIDs[0])
		return nil
	}

	// Unmax: restore stack to vertical, move master back out, resize.
	m.runCmd("[con_id=%d] layout splitv", m.windowIDs[0])
	m.createSubstackIfNeeded()
	m.runCmd("[con_id=%d] move %s", m.windowIDs[0], m.config.StackSide.Opposite().MoveDir())
	if m.masterWidthBefore > 0 {
		m.runCmd("[con_id=%d] resize set width %d px", m.windowIDs[0], m.masterWidthBefore)
	}
	m.setStackLayout()
	return nil
}

func (m *MasterStack) focusMaster() error {
	if len(m.windowIDs) == 0 {
		return nil
	}
	m.runCmd("[con_id=%d] focus", m.windowIDs[0])
	return nil
}

func (m *MasterStack) focusRelative(ws *sway.Node, delta int) error {
	if len(m.windowIDs) < 2 {
		return nil
	}

	focused := ws.FindFocused()
	if focused == nil {
		return nil
	}

	idx := m.indexOf(focused.ID)
	if idx < 0 {
		return nil
	}

	newIdx := (idx + delta + len(m.windowIDs)) % len(m.windowIDs)
	m.runCmd("[con_id=%d] focus", m.windowIDs[newIdx])
	return nil
}

func (m *MasterStack) moveRelative(ws *sway.Node, delta int) error {
	if len(m.windowIDs) < 2 {
		return nil
	}

	focused := ws.FindFocused()
	if focused == nil {
		return nil
	}

	idx := m.indexOf(focused.ID)
	if idx < 0 {
		return nil
	}

	newIdx := idx + delta
	if newIdx < 0 || newIdx >= len(m.windowIDs) {
		return nil
	}

	return m.moveWindowToIndex(focused, idx, newIdx)
}

func (m *MasterStack) moveWindowToIndex(window *sway.Node, srcIdx, dstIdx int) error {
	if srcIdx == dstIdx {
		return nil
	}

	// Neighbor swap
	if abs(srcIdx-dstIdx) == 1 {
		m.swapWindows(m.windowIDs[srcIdx], m.windowIDs[dstIdx])
		m.windowIDs[srcIdx], m.windowIDs[dstIdx] = m.windowIDs[dstIdx], m.windowIDs[srcIdx]
		if srcIdx == 0 || dstIdx == 0 {
			return m.setMasterWidth()
		}
		return nil
	}

	// Master swap
	if srcIdx == 0 || dstIdx == 0 {
		masterIdx := 0
		otherIdx := srcIdx
		if srcIdx == 0 {
			otherIdx = dstIdx
		}
		m.swapWindows(m.windowIDs[masterIdx], m.windowIDs[1])
		if otherIdx != 1 {
			m.moveWindow(m.windowIDs[otherIdx], m.windowIDs[dstIdx])
		}
		m.windowIDs[srcIdx], m.windowIDs[dstIdx] = m.windowIDs[dstIdx], m.windowIDs[srcIdx]
		return m.setMasterWidth()
	}

	// General move within stack
	m.moveWindow(window.ID, m.windowIDs[dstIdx])
	m.windowIDs[srcIdx], m.windowIDs[dstIdx] = m.windowIDs[dstIdx], m.windowIDs[srcIdx]
	return nil
}

func (m *MasterStack) adjustMasterWidth(delta int) error {
	newWidth := m.config.MasterWidth + delta
	if newWidth < 10 {
		newWidth = 10
	}
	if newWidth > 90 {
		newWidth = 90
	}
	m.config.MasterWidth = newWidth
	return m.setMasterWidth()
}

// --- Sway command primitives ---

func (m *MasterStack) moveWindow(moveID, targetID int64) {
	m.runCmd("[con_id=%d] mark --add move_target", targetID)
	m.runCmd("[con_id=%d] move window to mark move_target", moveID)
	m.runCmd("[con_id=%d] unmark move_target", targetID)
}

func (m *MasterStack) swapWindows(a, b int64) {
	m.runCmd("[con_id=%d] swap container with con_id %d", a, b)
}

func (m *MasterStack) setMasterWidth() error {
	if len(m.windowIDs) < 2 {
		return nil
	}
	m.runCmd("[con_id=%d] resize set width %d ppt", m.windowIDs[0], m.config.MasterWidth)
	return nil
}

func (m *MasterStack) setStackLayout() {
	if len(m.windowIDs) < 2 {
		return
	}
	// With MasterCount masters, the stack starts at index MasterCount.
	// If every window is a master there is no stack to style.
	if m.config.MasterCount >= len(m.windowIDs) {
		return
	}
	stackID := m.windowIDs[m.config.MasterCount]
	m.runCmd("[con_id=%d] layout %s", stackID, m.config.StackLayout)
}

func (m *MasterStack) runCmd(format string, args ...any) {
	cmd := fmt.Sprintf(format, args...)
	// DEBUG (was TRACE) so the manager-side decision trail survives in
	// production logs with debug=true — the user's standing config. The
	// IPC-level run_command DEBUG line (conn.go) carries elapsed timing;
	// this line carries the *decision context* (component=layout.X,
	// workspace=N from the manager's scoped logger) so a reader can
	// attribute the command to the manager that issued it. Commands
	// between two managers can't interleave under m.mu, so workspace
	// scoping is enough to reconstruct the per-workspace decision stream.
	m.log().Debug("sway cmd", "cmd", cmd)
	if err := m.conn.RunCommand(cmd); err != nil {
		m.log().Warn("sway cmd failed", "cmd", cmd, "error", err)
	}
}

// reconcileWindows synchronizes m.windowIDs against the workspace tree
// without issuing any sway commands:
//
//   - drops ids the tree no longer contains (stale — closed/relocated
//     containers).
//   - drops ids whose container is now FLOATING. Sway's swap command
//     transfers floating-list membership without emitting any
//     window::floating event (the only emitter is container_set_floating,
//     which swap never calls), so a tracked window can float with no
//     event to remove it — the 2026-06-12 ctrl-s bleed. A floating id
//     left in tracking poisons every subsequent dance: a move targeting
//     its mark floats the moved window too, and a swap pairing with it
//     transfers floatingness onward. Note FindByID alone cannot catch
//     this — it descends FloatingNodes and "finds" floats happily.
//
// exempt ids are kept regardless: WindowRemoved passes the id being
// removed so popWindow still sees its index (master-promotion needs to
// know whether index 0 was the one removed).
//
// Used by Command, WindowAdded, WindowRemoved, and the fullscreen-skip
// branch of ArrangeAll where a full rebuild would strand the fullscreen
// leaf. Caller holds m.mu.
func (m *MasterStack) reconcileWindows(ws *sway.Node, exempt ...int64) {
	if ws == nil || len(m.windowIDs) == 0 {
		return
	}
	kept := m.windowIDs[:0]
	var dropped []int64
	for _, id := range m.windowIDs {
		node := ws.FindByID(id)
		keep := node != nil && !node.IsFloating()
		if !keep {
			for _, ex := range exempt {
				if id == ex {
					keep = true
					break
				}
			}
		}
		if keep {
			kept = append(kept, id)
		} else {
			dropped = append(dropped, id)
		}
	}
	m.windowIDs = kept
	if len(dropped) > 0 {
		m.log().Warn("dropped stale/floating con_ids from tracking",
			"dropped", dropped, "remaining", append([]int64(nil), m.windowIDs...))
	}
}

// flattenWorkspace collapses every singleton non-workspace wrapper
// anywhere under the workspace.
//
// History: we used to emit `[con_id=X] move to workspace <self>` here,
// believing sway would re-attach X as a workspace-direct child. It does
// not: container_move_to_workspace (sway/commands/move.c:200-202)
// early-returns when the destination equals the source workspace. So
// every rebuild silently kept the old wrappers, and MasterStack would
// accumulate singleton containers each time the 2-window state was
// re-entered (observed: 12-deep chain on live ws7).
//
// The real flatten primitive is `split none` (sway/commands/split.c:35-50,
// do_unsplit): when the target's parent has exactly one child, that
// parent is destroyed and the child is lifted into its grandparent.
//
// Strategy: walk the tree, and for every non-workspace singleton
// wrapper W, emit `split none` on W's *sole child C*. Targeting the
// child (not the wrapper) is what lets us drain a nested chain
// `ws → A → B → C → leaf` in one pass: each command's destruction
// target is the *parent* of what we named, so every named con_id still
// exists when sway processes its command. Emitting on the wrappers
// themselves (an earlier draft) raced: after the first `split none`
// collapsed W, the next iteration's wrapper was already gone, yielding
// "No matching node" errors in the live journal.
//
// Also skips singletons above a branching node that a "walk up from
// each leaf" heuristic would miss (e.g. `ws → A(singleton) → B(2-child)
// → [leaf1, leaf2]` — walking up from leaf1 stops at B because B isn't
// a singleton, even though A is).
//
// EXCEPTION: the stack-column wrapper when stack has shrunk to one
// element. pushWindow's 2-window branch builds a splitv wrapper around
// stack[0] specifically so that subsequent insertAtIndex(0) calls can
// land windows INSIDE the stack column (via `move to mark on stack[0]`).
// Collapsing it leaves master and stack[0] as direct siblings in the
// outer splith — the next insert produces "3 stripes where master
// should be" because move-to-mark-on-stack[0] now lands sibling-of-
// master, not inside-the-stack-column. That's the 2026-05-25 Chromium-
// relaunch bug; see isStackColumnWrapper.
func (m *MasterStack) flattenWorkspace(ws *sway.Node, _ []*sway.Node) {
	if ws == nil {
		return
	}
	var targets []int64
	var walk func(n *sway.Node)
	walk = func(n *sway.Node) {
		if n.Type == "con" && len(n.Nodes) == 1 {
			child := n.Nodes[0]
			if !m.isStackColumnWrapper(n, child) {
				targets = append(targets, child.ID)
			}
		}
		for _, child := range n.Nodes {
			walk(child)
		}
	}
	// Walk only the tiled subtree. FloatingNodes live in a separate list
	// and aren't part of the wrapper chain we're trying to collapse.
	for _, child := range ws.Nodes {
		walk(child)
	}
	for _, id := range targets {
		m.runCmd("[con_id=%d] split none", id)
	}
}

// isStackColumnWrapper reports whether `wrapper` is THE stack-column
// container that MasterStack created for stack[0] — the wrapper that
// must survive flattenWorkspace even when it's a singleton.
//
// Identity:
//   - wrapper's sole child is windowIDs[1] (stack[0]).
//   - wrapper's layout matches the configured StackLayout.
//
// The layout match handles sway's "stacked"/"stacking" string trap:
// MasterStack issues `layout stacking` but sway's IPC tree reports the
// container's layout as "stacked". Both forms count as the same wrapper.
//
// Returns false when there's no stack yet (len(windowIDs) < 2) so the
// 0-window and 1-window arrangements still get their orphan wrappers
// (if any) flattened normally.
func (m *MasterStack) isStackColumnWrapper(wrapper, child *sway.Node) bool {
	if len(m.windowIDs) < 2 {
		return false
	}
	if child == nil || child.ID != m.windowIDs[1] {
		return false
	}
	want := m.config.StackLayout.String()
	if wrapper.Layout == want {
		return true
	}
	// sway IPC normalizes "stacking" → "stacked" in get_tree output.
	if want == "stacking" && wrapper.Layout == "stacked" {
		return true
	}
	return false
}

// --- Extra nesting removal ---

func (m *MasterStack) tryRemoveExtraNesting(ws *sway.Node) {
	if m.removeExtraNesting(ws) {
		m.extraNestingPending = false
	} else {
		m.extraNestingPending = true
	}
}

func (m *MasterStack) removeExtraNesting(ws *sway.Node) bool {
	if len(m.windowIDs) < 2 {
		return true
	}
	masterNode := ws.FindByID(m.windowIDs[0])
	if masterNode == nil || masterNode.Parent == nil {
		return false
	}
	// `split none` is sway's flatten: it only succeeds when the target is
	// the sole child of its parent. If the master shares its parent with
	// siblings, emitting it would trip
	// "Can only flatten a child container with no siblings" — which is
	// the live ws7 flatten bug we saw in the journal. Require a solo
	// parent before flattening.
	if masterNode.Parent.Type != "workspace" && masterNode.Parent.Parent != nil &&
		masterNode.Parent.Parent.Type != "workspace" &&
		len(masterNode.Parent.Nodes) == 1 {
		m.runCmd("[con_id=%d] split none", m.windowIDs[0])
		return true
	}
	return true
}

// --- Helpers ---

func (m *MasterStack) indexOf(id int64) int {
	for i, wid := range m.windowIDs {
		if wid == id {
			return i
		}
	}
	return -1
}

func (m *MasterStack) indexOfID(id int64) int {
	return m.indexOf(id)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// Verify interface compliance at compile time.
var _ Manager = (*MasterStack)(nil)
