package replay

import (
	"fmt"

	"github.com/mschulkind-oss/tilekeeper/internal/harness/sim"
	"github.com/mschulkind-oss/tilekeeper/internal/sway"
	"github.com/mschulkind-oss/tilekeeper/internal/workspace"
)

// replayState maintains the reconstructed live sim tree as the captured
// event stream advances. It is the replay analogue of fuzz.fuzzState: it
// owns the workspace nodes and a con_id→node index so each event can mutate
// the tree the way real sway would have.
type replayState struct {
	root       *sway.Node
	output     *sway.Node
	workspaces map[string]*sway.Node
	windows    map[int64]*sway.Node // con_id → live leaf node

	// pendingWS is the capture's resolved workspace (EventRecord.WS) for the
	// window event currently being applied. The driver sets it just before
	// applyPreDispatch so the window mutators (new/move) know which
	// workspace the event landed on without changing every helper's
	// signature. For window::move it is the DESTINATION workspace.
	pendingWS string
}

func newReplayState() *replayState {
	return &replayState{
		workspaces: map[string]*sway.Node{},
		windows:    map[int64]*sway.Node{},
	}
}

// adoptTree wires replayState onto an already-populated seed tree (from a
// capture meta get_tree snapshot), indexing its workspaces and leaves so
// subsequent events mutate the right nodes.
func (st *replayState) adoptTree(tree *sway.Node) {
	st.root = tree
	for _, c := range tree.Nodes {
		if c.Type == "output" {
			st.output = c
			break
		}
	}
	for _, ws := range tree.Workspaces() {
		st.workspaces[ws.Name] = ws
		for _, leaf := range ws.Leaves() {
			if leaf != nil && leaf.Type == "con" {
				st.windows[leaf.ID] = leaf
			}
		}
		for _, f := range ws.FloatingNodes {
			if f != nil && f.Type == "con" {
				st.windows[f.ID] = f
			}
		}
	}
}

// ensureWorkspace creates the workspace sub-tree in s if absent and returns
// the matching workspace::init event (empty event if it already existed).
// Mirrors fuzz.fuzzState.initWorkspace.
func (st *replayState) ensureWorkspace(s *sim.SimSwayClient, name string) sway.Event {
	if st.root == nil {
		tree, _ := s.GetTree()
		st.root = tree
	}
	if st.output == nil {
		for _, c := range st.root.Nodes {
			if c.Type == "output" {
				st.output = c
				break
			}
		}
		if st.output == nil {
			st.output = &sway.Node{
				ID:     s.AllocID(),
				Type:   "output",
				Name:   "replay-output",
				Parent: st.root,
			}
			st.root.Nodes = append(st.root.Nodes, st.output)
		}
	}
	if _, ok := st.workspaces[name]; ok {
		return sway.Event{}
	}
	ws := &sway.Node{
		ID:     s.AllocID(),
		Type:   "workspace",
		Name:   name,
		Layout: "splith",
		Parent: st.output,
	}
	st.output.Nodes = append(st.output.Nodes, ws)
	st.workspaces[name] = ws
	st.syncWorkspaces(s)
	return sway.Event{Type: "workspace", Change: "init", Workspace: ws}
}

// syncWorkspaces rewrites the sim's workspace list, marking the first as
// focused if none currently is. Keeps binding dispatch's
// findFocusedWorkspace resolvable.
func (st *replayState) syncWorkspaces(s *sim.SimSwayClient) {
	prev, _ := s.GetWorkspaces()
	focusedName := ""
	for _, w := range prev {
		if w.Focused {
			focusedName = w.Name
			break
		}
	}
	var wss []sway.Workspace
	first := true
	// Deterministic order: iterate the tree (output insertion order).
	for _, ws := range st.root.Workspaces() {
		focused := false
		if focusedName != "" && ws.Name == focusedName {
			focused = true
		} else if focusedName == "" && first {
			focused = true
		}
		first = false
		wss = append(wss, sway.Workspace{Name: ws.Name, Focused: focused})
	}
	s.SetWorkspaces(wss)
}

// ensureEventWorkspace makes sure the workspace an event references exists.
// For workspace events that's the event's own name; for window events it's
// the capture's resolved WS (wsName). The created workspace::init is
// dispatched to the hub so a manager is installed if it is in the managed
// set.
func (st *replayState) ensureEventWorkspace(s *sim.SimSwayClient, hub *workspace.Hub, ev sway.Event, wsName string) {
	name := wsName
	if ev.Type == "workspace" && ev.Workspace != nil {
		name = ev.Workspace.Name
	}
	if name == "" {
		return
	}
	if initEv := st.ensureWorkspace(s, name); initEv.Type != "" {
		hub.HandleEvent(initEv)
	}
}

// applyPreDispatch mutates the reconstructed tree to reflect the event,
// BEFORE the hub processes it — mirroring real sway, which has already moved
// the tree by the time it emits the event. window::close detaches the leaf
// here (sway fires window::close AFTER the container is destroyed, so the
// subscriber sees the post-close tree; this matches the fuzz driver, which
// also closes before dispatch). Returns an error when the event cannot be
// reconstructed; the caller records it as a Warning and skips the step.
func (st *replayState) applyPreDispatch(s *sim.SimSwayClient, ev sway.Event) error {
	switch ev.Type {
	case "workspace":
		// init/focus handled by ensureEventWorkspace + focus marker; no tree
		// shape change needed beyond ensuring the node exists.
		return nil
	case "binding":
		return nil
	case "window":
		return st.applyWindow(s, ev)
	default:
		return nil
	}
}

// applyPostDispatch is a hook for tree mutations that must happen AFTER the
// hub processes an event. Currently empty: close detaches pre-dispatch (sway
// destroys the container before emitting the event), matching the fuzz
// driver. Kept as a seam so a future event whose tree effect genuinely lands
// after the daemon reacts has a home.
func (st *replayState) applyPostDispatch(s *sim.SimSwayClient, ev sway.Event) {
	_ = s
	_ = ev
}

func (st *replayState) applyWindow(s *sim.SimSwayClient, ev sway.Event) error {
	c := ev.Container
	if c == nil {
		return fmt.Errorf("window:%s event has no container", ev.Change)
	}
	switch ev.Change {
	case "new":
		return st.applyNew(c)
	case "close":
		// Detach the leaf now: sway destroys the container before emitting
		// window::close, so the subscriber's tree is already post-close.
		if live := st.windows[c.ID]; live != nil {
			s.CloseLeaf(live)
			delete(st.windows, c.ID)
		}
		// Unknown window (lossy journal): tolerate; the hub's wsForCon
		// fallback handles a close it can't resolve.
		return nil
	case "focus":
		return st.applyFocus(c)
	case "floating":
		return st.applyFloating(c)
	case "move":
		return st.applyMove(c, st.pendingWS)
	case "fullscreen_mode":
		st.applyFullscreen(c)
		return nil
	default:
		// Unknown window change: harmless to the tree; let the hub ignore it.
		return nil
	}
}

func (st *replayState) applyNew(c *sway.Node) error {
	wsName := st.pendingWS
	ws := st.workspaces[wsName]
	if ws == nil {
		// No resolved workspace: attach to the focused leaf's workspace, or
		// the first known workspace. A capture always carries WS, so this is
		// the lossy-journal path.
		ws = st.focusedWorkspace()
		if ws == nil {
			return fmt.Errorf("window:new con=%d: no workspace (ws=%q)", c.ID, wsName)
		}
	}
	leaf := &sway.Node{
		ID:             c.ID,
		Type:           "con",
		Name:           c.Name,
		AppID:          c.AppID,
		Layout:         c.Layout,
		Floating:       c.Floating,
		FullscreenMode: c.FullscreenMode,
		Rect:           c.Rect,
	}
	if leaf.Rect.Width == 0 {
		leaf.Rect = sway.Rect{Width: 1280, Height: 720}
	}
	if c.IsFloating() {
		leaf.Parent = ws
		ws.FloatingNodes = append(ws.FloatingNodes, leaf)
	} else {
		// Attach as sibling of the focused leaf, like sway view_map.
		focused := ws.FindFocused()
		if focused != nil && focused.Type == "con" && !focused.IsFloating() && focused.Parent != nil {
			parent := focused.Parent
			leaf.Parent = parent
			idx := len(parent.Nodes)
			for i, ch := range parent.Nodes {
				if ch == focused {
					idx = i + 1
					break
				}
			}
			parent.Nodes = append(parent.Nodes, nil)
			copy(parent.Nodes[idx+1:], parent.Nodes[idx:])
			parent.Nodes[idx] = leaf
		} else {
			leaf.Parent = ws
			ws.Nodes = append(ws.Nodes, leaf)
		}
	}
	st.windows[c.ID] = leaf
	st.clearFocus()
	leaf.Focused = true
	return nil
}

func (st *replayState) applyFocus(c *sway.Node) error {
	leaf := st.windows[c.ID]
	if leaf == nil {
		return fmt.Errorf("window:focus con=%d: unknown window", c.ID)
	}
	st.clearFocus()
	leaf.Focused = true
	return nil
}

func (st *replayState) applyFloating(c *sway.Node) error {
	leaf := st.windows[c.ID]
	if leaf == nil {
		return fmt.Errorf("window:floating con=%d: unknown window", c.ID)
	}
	ws := leaf.FindWorkspace()
	if ws == nil {
		return fmt.Errorf("window:floating con=%d: no workspace", c.ID)
	}
	// The event snapshot's Floating field is the POST-toggle state (sway
	// emits floating after container_set_floating). Drive the transition
	// from the snapshot, not the live node.
	wantFloating := c.IsFloating()
	if wantFloating && !leaf.IsFloating() {
		// tiled → float
		leaf.Floating = c.Floating
		detachAndCascade(leaf)
		ws.FloatingNodes = append(ws.FloatingNodes, leaf)
		leaf.Parent = ws
	} else if !wantFloating && leaf.IsFloating() {
		// float → tiled
		leaf.Floating = c.Floating
		for i, fn := range ws.FloatingNodes {
			if fn == leaf {
				ws.FloatingNodes = append(ws.FloatingNodes[:i], ws.FloatingNodes[i+1:]...)
				break
			}
		}
		ws.Nodes = append(ws.Nodes, leaf)
		leaf.Parent = ws
	} else {
		// Snapshot agrees with live state — just record the floating string.
		leaf.Floating = c.Floating
	}
	return nil
}

func (st *replayState) applyFullscreen(c *sway.Node) {
	leaf := st.windows[c.ID]
	if leaf == nil {
		return
	}
	leaf.FullscreenMode = c.FullscreenMode
	if c.FullscreenMode == 1 {
		// Enforce one-fullscreen-per-workspace.
		if ws := leaf.FindWorkspace(); ws != nil {
			for _, l := range ws.Leaves() {
				if l != leaf && l.FullscreenMode == 1 {
					l.FullscreenMode = 0
				}
			}
		}
	}
}

// applyMove relocates the leaf identified by c to destWS. If the leaf is the
// root of a multi-window subtree, the WHOLE subtree rides along — this is
// the sway fellow-traveler semantics that produced the ws7 desync (one
// window::move event for a multi-window column). destWS is the capture's
// resolved destination workspace.
func (st *replayState) applyMove(c *sway.Node, destWS string) error {
	leaf := st.windows[c.ID]
	if leaf == nil {
		return fmt.Errorf("window:move con=%d: unknown window", c.ID)
	}
	dest := st.workspaces[destWS]
	if dest == nil {
		// No resolved destination. Treat as same-workspace reorder (no tree
		// change) — the hub's same-ws path will ignore it.
		return nil
	}
	curWS := leaf.FindWorkspace()
	if curWS == dest {
		// Already on the destination: same-workspace move echo. Sway fires
		// window::move for internal reorders too; leave the tree as is.
		return nil
	}
	// Move the leaf's whole subtree. The "subtree" is the leaf when its
	// parent is the workspace or a multi-child container; when the leaf is
	// the sole occupant of a chain of singleton wrappers, sway moves the
	// outermost still-single-child wrapper. We approximate by moving the
	// highest ancestor below the workspace whose subtree the focused leaf
	// fully determines — but the simplest faithful model that reproduces the
	// fellow-traveler bug is: move the focused leaf's TOP-LEVEL ancestor
	// (direct child of the workspace) when that ancestor is what the user's
	// move-to-workspace key relocated. For replay we move the leaf itself by
	// default; a capture that intends a subtree move records the subtree via
	// the seed tree shape + the representative leaf, and moving the leaf's
	// top-of-workspace ancestor reproduces fellow-travelers.
	subtree := topLevelAncestor(leaf, curWS)
	if subtree == nil {
		subtree = leaf
	}
	detachAndCascade(subtree)
	dest.Nodes = append(dest.Nodes, subtree)
	subtree.Parent = dest
	// Sway zeroes pending geometry on a cross-workspace move.
	for _, l := range subtree.Leaves() {
		l.Rect.Width, l.Rect.Height = 0, 0
	}
	st.clearFocus()
	leaf.Focused = true
	return nil
}

// topLevelAncestor returns the direct child of ws that contains n (i.e. n's
// outermost ancestor below the workspace), or n itself if n is a direct
// child. Returns nil if n is not under ws.
func topLevelAncestor(n, ws *sway.Node) *sway.Node {
	if n == nil || ws == nil {
		return nil
	}
	cur := n
	for cur.Parent != nil && cur.Parent != ws {
		cur = cur.Parent
	}
	if cur.Parent == ws {
		return cur
	}
	return nil
}

func (st *replayState) focusedWorkspace() *sway.Node {
	if st.root == nil {
		return nil
	}
	for _, ws := range st.root.Workspaces() {
		if ws.FindFocused() != nil {
			return ws
		}
	}
	for _, ws := range st.root.Workspaces() {
		return ws
	}
	return nil
}

func (st *replayState) clearFocus() {
	if st.root == nil {
		return
	}
	var walk func(n *sway.Node)
	walk = func(n *sway.Node) {
		n.Focused = false
		for _, c := range n.Nodes {
			walk(c)
		}
		for _, c := range n.FloatingNodes {
			walk(c)
		}
	}
	walk(st.root)
}

// detachAndCascade removes n from its parent and prunes empty (zero-child)
// wrappers up the chain, mirroring sway's container_reap_empty (and the fuzz
// generator's identical helper). Singletons are NOT auto-collapsed — sway
// keeps them until an explicit `split none`.
func detachAndCascade(n *sway.Node) {
	p := n.Parent
	if p == nil {
		return
	}
	for i, c := range p.Nodes {
		if c == n {
			p.Nodes = append(p.Nodes[:i], p.Nodes[i+1:]...)
			break
		}
	}
	n.Parent = nil
	for cur := p; cur != nil; {
		if cur.Type == "workspace" || cur.Type == "output" || cur.Type == "root" {
			return
		}
		grand := cur.Parent
		if grand == nil {
			return
		}
		if len(cur.Nodes) != 0 {
			return
		}
		for i, c := range grand.Nodes {
			if c == cur {
				grand.Nodes = append(grand.Nodes[:i], grand.Nodes[i+1:]...)
				break
			}
		}
		cur.Parent = nil
		cur = grand
	}
}
