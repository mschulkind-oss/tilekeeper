// Package workspace manages per-workspace layout state and routes sway events.
//
// The Hub is the central coordinator. It maps workspace names/numbers to
// layout.Manager instances, routes window/workspace/binding events, and
// handles layout switching.
package workspace

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/mschulkind-oss/tilekeeper/internal/config"
	"github.com/mschulkind-oss/tilekeeper/internal/layout"
	"github.com/mschulkind-oss/tilekeeper/internal/logging"
	"github.com/mschulkind-oss/tilekeeper/internal/sway"
)

// opCtr is the global monotonic operation counter. Each top-level hub
// dispatch (window event, workspace event, binding event, IPC command)
// mints a fresh op id, logs an `op begin` line with seq+op_id+identity
// at INFO, and emits a matching `op end` at the boundary. Combined with
// the daemon's per-event seq number, every layout decision in the
// journal has a (seq, op) coordinate that pinpoints its source.
var opCtr atomic.Int64

// Hub manages layout managers for all workspaces.
//
// It is the main event handler — the daemon routes all sway events here,
// and the Hub dispatches them to the appropriate workspace's layout manager.
type Hub struct {
	mu       sync.Mutex
	client   sway.Client
	managers map[string]layout.Manager // key: workspace name (e.g., "4")
	cfg      config.Config
	logger   *slog.Logger
	// wsForCon remembers which workspace each tracked container currently
	// lives on. Window move events from sway have no "old workspace" field
	// and the event container's Parent is nil (parseWindowEvent unmarshals
	// from JSON), so tree-only lookup always returns the destination. This
	// map is the Hub's source of truth for the OLD workspace.
	wsForCon map[int64]string
}

// NewHub creates a Hub from config and sway client.
//
// Workspace layout managers are created lazily when events arrive, or
// eagerly for workspaces defined in config via Initialize().
func NewHub(client sway.Client, cfg config.Config, logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	return &Hub{
		client:   client,
		managers: make(map[string]layout.Manager),
		cfg:      cfg,
		logger:   logger,
		wsForCon: make(map[int64]string),
	}
}

// Initialize creates layout managers for all configured workspaces.
// Call this once at startup before processing events.
func (h *Hub) Initialize() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for name, wsCfg := range h.cfg.Workspaces {
		mgr := h.createManager(name, wsCfg)
		if mgr != nil {
			h.managers[name] = mgr
			h.logger.Info("initialized workspace", "workspace", name, "layout", mgr.Name())
		}
	}
}

// SeedTracking populates wsForCon for every leaf in the given tree.
//
// Without this, pre-existing windows (those that existed before tilekeeper
// started — so no window::new event ever fired for them) are missing from
// wsForCon. When one of them later moves cross-workspace, handleWindowMove
// reads hadTracked=false and skips the mgr.WindowRemoved call on the source
// manager, leaving a stale id in the source manager's windowIDs. The next
// swap/move command on that stale id targets the window on its NEW
// workspace — a cross-workspace swap that moves a sibling of that window
// onto the source workspace and the stale window onto the destination.
// Manifests as "windows jumping from ws7 to ws6 while I was just shuffling
// them on ws7" in the live journal.
//
// Call this once at daemon startup after fetching the tree, before
// subscribing to events.
func (h *Hub) SeedTracking(tree *sway.Node) {
	if tree == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ws := range tree.Workspaces() {
		for _, leaf := range ws.Leaves() {
			if leaf == nil || leaf.Type != "con" {
				continue
			}
			h.wsForCon[leaf.ID] = ws.Name
		}
	}
}

// HandleEvent processes a sway event by routing it to the correct workspace.
func (h *Hub) HandleEvent(event sway.Event) {
	switch event.Type {
	case "window":
		h.handleWindowEvent(event)
	case "workspace":
		h.handleWorkspaceEvent(event)
	case "binding":
		h.handleBindingEvent(event)
	}
}

// Manager returns the layout manager for a workspace, or nil.
func (h *Hub) Manager(wsName string) layout.Manager {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.managers[wsName]
}

// SetManager sets the layout manager for a workspace.
func (h *Hub) SetManager(wsName string, mgr layout.Manager) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.managers[wsName] = mgr
}

// RemoveManager removes the layout manager for a workspace.
func (h *Hub) RemoveManager(wsName string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.managers, wsName)
}

// WorkspaceForContainer returns the workspace name the Hub believes owns the
// given container, or "" if unknown. Exposed for tests and diagnostics.
func (h *Hub) WorkspaceForContainer(conID int64) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.wsForCon[conID]
}

// WorkspaceNames returns the names of all managed workspaces.
func (h *Hub) WorkspaceNames() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	names := make([]string, 0, len(h.managers))
	for name := range h.managers {
		names = append(names, name)
	}
	return names
}

// --- Event Routing ---

func (h *Hub) handleWindowEvent(event sway.Event) {
	if event.Container == nil {
		logging.Trace(h.logger, "window event with no container",
			"seq", event.Seq, "change", event.Change)
		return
	}
	container := event.Container

	logging.Trace(h.logger, "window event",
		"seq", event.Seq,
		"change", event.Change,
		"con_id", container.ID,
		"name", container.Name,
		"app_id", container.AppID,
		"type", container.Type,
		"layout", container.Layout,
		"floating", container.Floating,
		"focused", container.Focused,
		"fullscreen", container.FullscreenMode,
	)

	// Move events need special treatment: the NEW workspace from the tree
	// is not enough — we also need the OLD one so we can issue WindowRemoved
	// on the source manager. handleWindowMove owns that logic end-to-end.
	if event.Change == "move" {
		h.handleWindowMove(event.Seq, container)
		return
	}

	wsNode := h.findWorkspaceForContainer(container)
	if wsNode == nil {
		h.logger.Debug("no workspace found for container",
			"seq", event.Seq, "change", event.Change,
			"con_id", container.ID, "name", container.Name)
		return
	}
	wsName := wsNode.Name

	// Track wsForCon even when no manager is configured for this workspace.
	// `layout MasterStack` later may install a manager that picks up the
	// existing leaves via ArrangeAll; without an early wsForCon entry, a
	// subsequent cross-workspace move cannot find the old workspace and the
	// id leaks in tracking forever.
	switch event.Change {
	case "new":
		h.mu.Lock()
		h.wsForCon[container.ID] = wsName
		h.mu.Unlock()
	case "close":
		h.mu.Lock()
		delete(h.wsForCon, container.ID)
		h.mu.Unlock()
	}

	mgr := h.Manager(wsName)
	if mgr == nil {
		h.logger.Debug("no manager for workspace",
			"seq", event.Seq, "change", event.Change,
			"workspace", wsName, "con_id", container.ID)
		if event.Change == "close" {
			h.flattenWorkspace(wsName)
		}
		return
	}

	opID := opCtr.Add(1)

	// Divergence check: if the manager's tracking already disagrees with
	// the workspace tree at the start of this op, log it loudly. This is
	// the corruption-detection breadcrumb the user asked for — without
	// it, the journal showed corruption only later (e.g. when a `move to
	// mark` failed). With it, the journal points at the EXACT op that
	// inherited bad state.
	h.checkDivergence(opID, event.Seq, wsName, mgr, wsNode, container.ID)
	// Heal the one divergence class sway emits no event for: tracked
	// windows that the tree shows floating (swap-transferred
	// floatingness). After the check so the WARN documents what was
	// healed; before the op so the handler sees clean tracking.
	h.reconcileFloatingStale(opID, event.Seq, wsName, mgr, wsNode, container.ID)

	trackedBefore := mgr.WindowIDs()
	h.logger.Info("op begin",
		"op", opID,
		"op_name", event.Change,
		"seq", event.Seq,
		"workspace", wsName,
		"layout", mgr.Name(),
		"con_id", container.ID,
		"name", container.Name,
		"app_id", container.AppID,
		"tracked_before", trackedBefore,
	)

	var err error
	switch event.Change {
	case "new":
		err = mgr.WindowAdded(wsNode, container)
	case "close":
		err = mgr.WindowRemoved(wsNode, container)
	case "focus":
		err = mgr.WindowFocused(wsNode, container)
	case "floating":
		err = h.handleFloatingChange(container, wsNode, mgr)
	}

	trackedAfter := mgr.WindowIDs()
	if err != nil {
		h.logger.Error("op end (error)",
			"op", opID, "op_name", event.Change, "seq", event.Seq,
			"workspace", wsName, "con_id", container.ID,
			"tracked_after", trackedAfter, "error", err)
	} else {
		h.logger.Info("op end",
			"op", opID, "op_name", event.Change, "seq", event.Seq,
			"workspace", wsName, "con_id", container.ID,
			"tracked_after", trackedAfter)
	}

	// When a close shrinks a wrapper to a singleton, no one else will
	// clean it up on UNTRACKED-layout workspaces (Tabbed defers to sway;
	// WindowIDs()==nil is that sentinel). Tracking managers own their
	// workspace shape: MasterStack's popWindow flattens the whole
	// workspace itself — WITH its stack-column exception. Running the
	// generic FlattenSingletons here too destroyed that protected
	// singleton wrapper right after popWindow preserved it (split none on
	// the lone stack window), recreating the 2026-05-25 "3 stripes"
	// corruption on every stack-dwindles-to-one close.
	if event.Change == "close" && mgr.WindowIDs() == nil {
		h.flattenWorkspace(wsName)
	}
}

// checkDivergence compares the manager's tracked window-id set against
// the workspace's non-excluded tiled leaves. If they differ, logs a
// WARN with both sides and a (missed, stale) diff. Non-mutating: the
// goal is to MAKE corruption visible in the journal at the moment it's
// observed, not to fix it. Recovery for the silently-floated class is
// reconcileFloatingStale (Hub) plus MasterStack.reconcileWindows
// (manager); dropped events have NO recovery by design — prevention only
// (channel sizing + subscribe-side filter).
//
// exemptID is the op's own in-flight container, excluded from the diff:
// sway attaches a view BEFORE emitting window::new and destroys it BEFORE
// emitting window::close, so at op start the tree and tracking always
// differ by exactly that container — expected sequencing, not
// corruption. Without the exemption every routine new/close/move op
// WARNed, drowning the breadcrumb this check exists to provide. Pass 0
// when there is no in-flight container (binding ops).
//
// Called at the start of each managed-workspace event op. The check is
// cheap (set ops over a small slice) and the WARN only fires on actual
// divergence — silent in the healthy case.
func (h *Hub) checkDivergence(opID, seq int64, wsName string, mgr layout.Manager, ws *sway.Node, exemptID int64) {
	if ws == nil {
		return
	}
	tracked := mgr.WindowIDs()
	if tracked == nil {
		// Tabbed defers tracking to sway; nothing to compare.
		return
	}
	wantTracked := make(map[int64]struct{})
	for _, l := range ws.Leaves() {
		if layout.IsExcluded(l) {
			continue
		}
		wantTracked[l.ID] = struct{}{}
	}
	allLeaves := make(map[int64]struct{})
	for _, l := range ws.Leaves() {
		if l == nil || l.Type != "con" {
			continue
		}
		allLeaves[l.ID] = struct{}{}
	}
	got := make(map[int64]struct{}, len(tracked))
	for _, id := range tracked {
		got[id] = struct{}{}
	}
	var missed, stale []int64
	for id := range wantTracked {
		if _, ok := got[id]; !ok && id != exemptID {
			missed = append(missed, id)
		}
	}
	for id := range got {
		if _, ok := allLeaves[id]; !ok && id != exemptID {
			stale = append(stale, id)
		}
	}
	if len(missed) == 0 && len(stale) == 0 {
		return
	}
	slices.Sort(missed)
	slices.Sort(stale)
	h.logger.Warn("tracking diverged from tree",
		"op", opID, "seq", seq,
		"workspace", wsName,
		"tracked", tracked,
		"missed", missed,
		"stale", stale,
	)
}

// reconcileFloatingStale delivers the window::floating events sway never
// sent. Sway's swap command transfers floating-list membership without
// emitting window::floating (the only emitter in the sway tree is
// container_set_floating, which swap never calls), so a tracked window
// can sit in the workspace's floating list with tracking none the wiser
// — the 2026-06-12 ctrl-s incident, where a master-insert dance swapped
// with an already-floating portal dialog and bled floatingness onto
// tracked windows. For each tracked id whose node in the fresh tree is
// floating, deliver a synthetic WindowRemoved — semantically the missed
// event, taking the same path a real window::floating would
// (handleFloatingChange → WindowRemoved).
//
// This is NOT event-drop recovery (which stays forbidden by design —
// drops are prevented upstream, never repaired): no event was dropped
// here; sway never emitted one.
//
// exemptID is the op's own in-flight container: when the current op IS
// the floating event for that window, the main handler delivers the real
// WindowRemoved — synthesizing one here would be redundant and would
// mislabel a perfectly-delivered event as "missed" in the journal.
func (h *Hub) reconcileFloatingStale(opID, seq int64, wsName string, mgr layout.Manager, ws *sway.Node, exemptID int64) {
	if ws == nil {
		return
	}
	tracked := mgr.WindowIDs()
	if tracked == nil {
		// Tabbed defers tracking to sway; nothing to heal.
		return
	}
	for _, id := range tracked {
		if id == exemptID {
			continue
		}
		node := ws.FindByID(id)
		if node == nil || !node.IsFloating() {
			continue
		}
		h.logger.Warn("synthesizing missed floating event for tracked float",
			"op", opID, "seq", seq, "workspace", wsName,
			"con_id", id, "name", node.Name)
		if err := mgr.WindowRemoved(ws, node); err != nil {
			h.logger.Error("synthetic floating removal failed",
				"op", opID, "seq", seq, "workspace", wsName,
				"con_id", id, "error", err)
		}
	}
}

// unadoptedArrivals returns the ids of tiled leaves on ws that the
// manager does not yet track, excluding exceptID. These are the
// "fellow-travelers" of a container move — windows that rode along with a
// subtree relocation but generated no window::move event of their own.
//
// The filter MUST match arrangeWindows' leaf collection (every "con" leaf
// that is not floating/fullscreen) rather than layout.IsExcluded:
// IsExcluded skips windows under stacked/tabbed parents, but those are
// exactly the substack windows MasterStack legitimately tracks — and in
// the live 2026-06-13 incident, 8 of the 13 stranded windows sat in a
// stacked substack. Using IsExcluded here would miss them and skip the
// rebuild. Returns nil for managers that don't track (Tabbed: WindowIDs
// is nil), which never strand windows this way.
func (h *Hub) unadoptedArrivals(mgr layout.Manager, ws *sway.Node, exceptID int64) []int64 {
	if ws == nil {
		return nil
	}
	tracked := mgr.WindowIDs()
	if tracked == nil {
		return nil
	}
	have := make(map[int64]struct{}, len(tracked))
	for _, id := range tracked {
		have[id] = struct{}{}
	}
	var out []int64
	for _, l := range ws.Leaves() {
		if l == nil || l.Type != "con" || l.ID == exceptID {
			continue
		}
		if l.IsFloating() || l.FullscreenMode == 1 {
			continue
		}
		if _, ok := have[l.ID]; !ok {
			out = append(out, l.ID)
		}
	}
	return out
}

// flattenWorkspace re-fetches the tree and calls layout.FlattenSingletons
// on the named workspace. Used as a defensive cleanup after window::close
// so orphan wrappers don't linger on workspaces whose current manager
// doesn't track them (Tabbed, unmanaged, or MasterStack wrappers left
// from a prior layout).
func (h *Hub) flattenWorkspace(name string) {
	ws, err := h.getWorkspaceNode(name)
	if err != nil {
		return
	}
	layout.FlattenSingletons(h.client, ws)
}

func (h *Hub) handleWindowMove(seq int64, container *sway.Node) {
	tree, err := h.client.GetTree()
	if err != nil {
		h.logger.Error("failed to get tree for move event",
			"seq", seq, "con_id", container.ID, "error", err)
		return
	}
	tree.SetParents()

	// NEW workspace comes from the live tree.
	var newWS *sway.Node
	if node := tree.FindByID(container.ID); node != nil {
		newWS = node.FindWorkspace()
	}
	newWSName := ""
	if newWS != nil {
		newWSName = newWS.Name
	}

	// OLD workspace comes from the Hub's tracking map. The event container's
	// Parent is nil (parseWindowEvent) and the tree already reflects the
	// post-move state, so this map is the only authoritative source for the
	// source workspace.
	h.mu.Lock()
	oldWSName, hadTracked := h.wsForCon[container.ID]
	h.mu.Unlock()

	// Same-workspace reorder: sway fires window::move for every internal
	// reorder, including echoes of our own swap/move commands. The tracked
	// manager already updated its state when it issued the command; treating
	// the echo as remove+add would wipe windowIDs after each navigation.
	if hadTracked && oldWSName == newWSName {
		logging.Trace(h.logger, "window move (same workspace, ignored)",
			"seq", seq, "con_id", container.ID, "workspace", newWSName)
		return
	}

	opID := opCtr.Add(1)
	h.logger.Info("op begin",
		"op", opID, "op_name", "move",
		"seq", seq, "con_id", container.ID, "name", container.Name,
		"from", oldWSName, "to", newWSName, "tracked", hadTracked)

	if hadTracked {
		if mgr := h.Manager(oldWSName); mgr != nil {
			// Reuse the tree we already fetched — getWorkspaceNode would
			// trigger a second IPC round-trip for the same data. The tree
			// is post-move, so the OLD workspace node here represents the
			// state AFTER the container left; the manager's WindowRemoved
			// reads tracking from its own state and the container arg, so
			// the stale-vs-live distinction on the source workspace doesn't
			// matter for the remove path.
			var oldWSNode *sway.Node
			for _, ws := range tree.Workspaces() {
				if ws.Name == oldWSName {
					oldWSNode = ws
					break
				}
			}
			h.checkDivergence(opID, seq, oldWSName, mgr, oldWSNode, container.ID)
			h.reconcileFloatingStale(opID, seq, oldWSName, mgr, oldWSNode, container.ID)
			if removeErr := mgr.WindowRemoved(oldWSNode, container); removeErr != nil {
				h.logger.Error("remove from old workspace failed",
					"op", opID, "seq", seq,
					"con_id", container.ID, "from", oldWSName, "error", removeErr)
			}
		}
	}

	if newWS != nil {
		if mgr := h.Manager(newWSName); mgr != nil {
			h.checkDivergence(opID, seq, newWSName, mgr, newWS, container.ID)
			h.reconcileFloatingStale(opID, seq, newWSName, mgr, newWS, container.ID)

			// Sway emits ONE window::move for the focused container, even
			// when that container has children — so moving a multi-window
			// subtree across workspaces (e.g. holding the move-to-workspace
			// key while focused on a column) relocates N windows but only
			// the representative leaf gets an event. The "fellow-travelers"
			// land on the destination with no event of their own. Adopting
			// only `container` here is what stranded 10 of 13 windows on
			// live ws7 (2026-06-13): tracking captured the 3 that moved with
			// clean per-window events and never adopted the rest.
			//
			// Detect the unadopted arrivals and, if any, rebuild the
			// destination so all of them are tracked. arrangeWindows derives
			// its window set from the tree, so it adopts every arrival in one
			// pass (incrementally inserting an unknown subtree is fragile).
			// The common single-window move has zero arrivals and takes the
			// cheap WindowAdded path unchanged.
			if arrivals := h.unadoptedArrivals(mgr, newWS, container.ID); len(arrivals) > 0 {
				h.logger.Warn("adopting windows that moved in without their own event",
					"op", opID, "seq", seq, "workspace", newWSName,
					"trigger", container.ID, "adopted", arrivals)
				if arrErr := mgr.ArrangeAll(newWS); arrErr != nil {
					h.logger.Error("arrange after container move-in failed",
						"op", opID, "seq", seq, "workspace", newWSName, "error", arrErr)
				}
			} else if addErr := mgr.WindowAdded(newWS, container); addErr != nil {
				h.logger.Error("add to new workspace failed",
					"op", opID, "seq", seq,
					"con_id", container.ID, "to", newWSName, "error", addErr)
			}
		}
		// Update wsForCon for the container AND every tiled leaf now on the
		// destination — the fellow-travelers' last-known workspace is still
		// the source, so a later cross-workspace move of one of them would
		// misroute its WindowRemoved without this.
		h.mu.Lock()
		h.wsForCon[container.ID] = newWSName
		for _, l := range newWS.Leaves() {
			if l != nil && l.Type == "con" {
				h.wsForCon[l.ID] = newWSName
			}
		}
		h.mu.Unlock()
	} else {
		// Container left the tree entirely (e.g. moved to scratchpad).
		h.mu.Lock()
		delete(h.wsForCon, container.ID)
		h.mu.Unlock()
	}
	h.logger.Info("op end",
		"op", opID, "op_name", "move", "seq", seq,
		"con_id", container.ID, "from", oldWSName, "to", newWSName)
}

func (h *Hub) handleFloatingChange(container *sway.Node, wsNode *sway.Node, mgr layout.Manager) error {
	if container.IsFloating() {
		// Window became floating — remove from tiling layout
		return mgr.WindowRemoved(wsNode, container)
	}
	// Window returned to tiling — add back
	return mgr.WindowAdded(wsNode, container)
}

func (h *Hub) handleWorkspaceEvent(event sway.Event) {
	wsName := ""
	if event.Workspace != nil {
		wsName = event.Workspace.Name
	}
	logging.Trace(h.logger, "workspace event",
		"seq", event.Seq, "change", event.Change, "workspace", wsName)

	switch event.Change {
	case "init":
		if event.Workspace == nil {
			return
		}
		h.mu.Lock()
		_, exists := h.managers[wsName]
		if !exists {
			if wsCfg, ok := h.cfg.Workspaces[wsName]; ok {
				mgr := h.createManager(wsName, wsCfg)
				if mgr != nil {
					h.managers[wsName] = mgr
					h.logger.Info("lazy-init workspace",
						"seq", event.Seq, "workspace", wsName, "layout", mgr.Name())
				} else {
					h.logger.Debug("init: workspace configured but manager is nil",
						"seq", event.Seq, "workspace", wsName,
						"defaultLayout", wsCfg.DefaultLayout)
				}
			} else {
				h.logger.Debug("init: no config for workspace",
					"seq", event.Seq, "workspace", wsName)
			}
		}
		h.mu.Unlock()

	case "focus":
		h.logger.Debug("workspace focus", "seq", event.Seq, "workspace", wsName)
	}
}

func (h *Hub) handleBindingEvent(event sway.Event) {
	if event.Binding == nil {
		return
	}
	raw := event.Binding.Command
	logging.Trace(h.logger, "binding event (raw)", "seq", event.Seq, "raw", raw)

	cmd := ParseNopCommand(raw)
	if cmd == nil {
		logging.Trace(h.logger, "binding event ignored (not a nop tilekeeper command)",
			"seq", event.Seq, "raw", raw)
		return
	}

	h.logger.Debug("binding command",
		"seq", event.Seq, "command", cmd.Command, "workspace", cmd.Workspace, "raw", raw)

	wsName := cmd.Workspace
	if wsName == "" {
		ws, err := h.findFocusedWorkspace()
		if err != nil {
			h.logger.Error("failed to find focused workspace",
				"command", cmd.Command, "error", err)
			return
		}
		wsName = ws
		h.logger.Debug("binding resolved to focused workspace",
			"command", cmd.Command, "workspace", wsName)
	}

	// `set <layout>` swaps the manager out; it does not address the
	// current manager. Route it through the same path as the IPC
	// `layout-set` so binding and CLI behave identically.
	if rest, ok := strings.CutPrefix(cmd.Command, "layout "); ok {
		layoutName := strings.TrimSpace(rest)
		h.logger.Info("binding: layout-set",
			"workspace", wsName, "layout", layoutName)
		if resp := h.handleLayoutSet(wsName, layoutName); !resp.OK {
			h.logger.Error("layout-set failed",
				"workspace", wsName, "layout", layoutName, "error", resp.Error)
		}
		return
	}

	mgr := h.Manager(wsName)
	if mgr == nil {
		// No layout manager for this workspace — but simple directional
		// commands (`move left`, `focus down`, ...) have a perfectly good
		// native sway equivalent. Fall through so directional bindings keep
		// working on unmanaged workspaces (e.g. ws "B").
		if native, ok := nativeSwayFallback(cmd.Command); ok {
			if err := h.client.RunCommand(native); err != nil {
				h.logger.Error("native fallback failed",
					"workspace", wsName, "command", native, "error", err)
			}
			return
		}
		h.logger.Debug("no manager for binding",
			"workspace", wsName, "command", cmd.Command)
		return
	}

	wsNode, err := h.getWorkspaceNode(wsName)
	if err != nil {
		h.logger.Error("failed to get workspace node",
			"workspace", wsName, "command", cmd.Command, "error", err)
		return
	}

	opID := opCtr.Add(1)
	h.checkDivergence(opID, 0, wsName, mgr, wsNode, 0)
	h.reconcileFloatingStale(opID, 0, wsName, mgr, wsNode, 0)
	trackedBefore := mgr.WindowIDs()
	h.logger.Info("op begin",
		"op", opID, "op_name", "binding",
		"command", cmd.Command,
		"workspace", wsName,
		"layout", mgr.Name(),
		"tracked_before", trackedBefore,
	)
	if err := mgr.Command(cmd.Command, wsNode); err != nil {
		h.logger.Error("op end (error)",
			"op", opID, "op_name", "binding",
			"command", cmd.Command, "workspace", wsName,
			"tracked_after", mgr.WindowIDs(), "error", err)
	} else {
		h.logger.Info("op end",
			"op", opID, "op_name", "binding",
			"command", cmd.Command, "workspace", wsName,
			"tracked_after", mgr.WindowIDs())
	}
}

// nativeSwayFallback returns the native sway command for simple directional
// commands that don't need tilekeeper context. Used when a binding lands on
// a workspace without a configured manager.
func nativeSwayFallback(cmd string) (string, bool) {
	switch cmd {
	case "move left", "move right", "move up", "move down",
		"focus left", "focus right", "focus up", "focus down":
		return cmd, true
	}
	return "", false
}

// --- Helpers ---

func (h *Hub) findWorkspaceForContainer(container *sway.Node) *sway.Node {
	// If container has parent set, walk up
	if ws := container.FindWorkspace(); ws != nil {
		return ws
	}
	// Otherwise fetch tree and find it
	tree, err := h.client.GetTree()
	if err != nil {
		return nil
	}
	tree.SetParents()
	if node := tree.FindByID(container.ID); node != nil {
		return node.FindWorkspace()
	}
	// Container is gone from the tree. window::close events always hit
	// this path: IPC event JSON has no parent, so container.Parent=nil
	// and FindWorkspace fails; by the time the event arrives real sway
	// has already destroyed the container, so tree.FindByID misses too.
	// Without this fallback, WindowRemoved would never fire on close.
	// wsForCon records last-known workspace for every tracked container.
	h.mu.Lock()
	wsName, ok := h.wsForCon[container.ID]
	h.mu.Unlock()
	if !ok {
		return nil
	}
	for _, ws := range tree.Workspaces() {
		if ws.Name == wsName {
			return ws
		}
	}
	return nil
}

func (h *Hub) findFocusedWorkspace() (string, error) {
	workspaces, err := h.client.GetWorkspaces()
	if err != nil {
		return "", fmt.Errorf("get workspaces: %w", err)
	}
	for _, ws := range workspaces {
		if ws.Focused {
			return ws.Name, nil
		}
	}
	return "", fmt.Errorf("no focused workspace")
}

func (h *Hub) getWorkspaceNode(name string) (*sway.Node, error) {
	tree, err := h.client.GetTree()
	if err != nil {
		return nil, fmt.Errorf("get tree: %w", err)
	}
	tree.SetParents()
	for _, ws := range tree.Workspaces() {
		if ws.Name == name {
			return ws, nil
		}
	}
	return nil, fmt.Errorf("workspace %q not found in tree", name)
}

func (h *Hub) createManager(wsName string, wsCfg config.WorkspaceConfig) layout.Manager {
	layoutName := wsCfg.DefaultLayout
	if layoutName == "" {
		layoutName = h.cfg.General.DefaultLayout
	}

	var mgr layout.Manager
	switch layoutName {
	case "MasterStack", "masterstack", "master_stack":
		m := h.createMasterStackManager(wsCfg)
		m.SetLogger(h.managerLogger(wsName, "masterstack"))
		mgr = m
	case "ProjectTabs", "projecttabs", "project_tabs":
		m := h.createProjectTabsManager(wsCfg)
		m.SetLogger(h.managerLogger(wsName, "projecttabs"))
		mgr = m
	case "tabbed", "Tabbed":
		t := layout.NewTabbed(h.client)
		t.SetLogger(h.managerLogger(wsName, "tabbed"))
		mgr = t
	case "none", "":
		return nil
	default:
		h.logger.Warn("unknown layout", "layout", layoutName, "workspace", wsName)
		return nil
	}

	if mgr != nil {
		h.logger.Debug("manager created",
			"workspace", wsName, "layout", mgr.Name(), "configLayout", layoutName)
	}
	return mgr
}

// managerLogger returns a logger tagged with component=layout.<kind> and
// workspace=<wsName>, so every log line from a manager is already scoped.
func (h *Hub) managerLogger(wsName, kind string) *slog.Logger {
	return logging.Component(h.logger, "layout."+kind).With("workspace", wsName)
}

func (h *Hub) createMasterStackManager(wsCfg config.WorkspaceConfig) *layout.MasterStack {
	cfg := layout.DefaultMasterStackConfig()

	if wsCfg.MasterWidth > 0 {
		cfg.MasterWidth = wsCfg.MasterWidth
	} else if h.cfg.General.MasterWidth > 0 {
		cfg.MasterWidth = h.cfg.General.MasterWidth
	}

	if wsCfg.StackLayout != "" {
		cfg.StackLayout = layout.ParseStackLayout(wsCfg.StackLayout)
	} else if h.cfg.General.StackLayout != "" {
		cfg.StackLayout = layout.ParseStackLayout(h.cfg.General.StackLayout)
	}

	if wsCfg.StackSide != "" {
		cfg.StackSide = layout.ParseSide(wsCfg.StackSide)
	} else if h.cfg.General.StackSide != "" {
		cfg.StackSide = layout.ParseSide(h.cfg.General.StackSide)
	}

	if wsCfg.VisibleStackLimit > 0 {
		cfg.VisibleStackLimit = wsCfg.VisibleStackLimit
	} else if h.cfg.General.VisibleStackLimit > 0 {
		cfg.VisibleStackLimit = h.cfg.General.VisibleStackLimit
	}

	return layout.NewMasterStackManager(h.client, cfg)
}

func (h *Hub) createProjectTabsManager(wsCfg config.WorkspaceConfig) *layout.ProjectTabs {
	cfg := layout.ProjectTabsConfig{
		AutoDetect: true,
	}
	if wsCfg.SplitRatio > 0 {
		cfg.SplitRatio = wsCfg.SplitRatio
	}
	if wsCfg.TerminalSide != "" {
		cfg.TerminalSide = wsCfg.TerminalSide
	}
	if wsCfg.DefaultMode != "" {
		cfg.DefaultMode = wsCfg.DefaultMode
	}
	return layout.NewProjectTabs(h.client, cfg)
}
