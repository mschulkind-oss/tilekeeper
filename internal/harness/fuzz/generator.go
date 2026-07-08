package fuzz

import (
	"fmt"
	"math/rand/v2"
	"sort"

	"github.com/mschulkind-oss/tilekeeper/internal/harness/sim"
	"github.com/mschulkind-oss/tilekeeper/internal/sway"
)

// fuzzState tracks enough sim-side context to generate valid events
// (close events for containers that exist, focus for present leaves,
// etc.).
type fuzzState struct {
	workspaces map[string]*sway.Node
	output     *sway.Node
	root       *sway.Node
	windows    map[int64]*sway.Node // con_id → leaf node
}

func newFuzzState(workspaces []string) *fuzzState {
	_ = workspaces
	return &fuzzState{
		workspaces: map[string]*sway.Node{},
		windows:    map[int64]*sway.Node{},
	}
}

// initWorkspace creates the workspace sub-tree in s and returns the
// corresponding workspace:init event. Idempotent.
func (st *fuzzState) initWorkspace(s *sim.SimSwayClient, name string) sway.Event {
	tree, _ := s.GetTree()
	if tree == nil {
		return sway.Event{}
	}
	st.root = tree
	if st.output == nil {
		for _, c := range tree.Nodes {
			if c.Type == "output" {
				st.output = c
				break
			}
		}
		if st.output == nil {
			st.output = &sway.Node{
				ID:     s.AllocID(),
				Type:   "output",
				Name:   "fuzz-output",
				Parent: tree,
			}
			tree.Nodes = append(tree.Nodes, st.output)
		}
	}
	if ws, ok := st.workspaces[name]; ok {
		return sway.Event{Type: "workspace", Change: "init", Workspace: ws}
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
	// Keep the sim's workspace list in sync so findFocusedWorkspace
	// (used by binding dispatch) resolves the focused workspace. The
	// first registered workspace is initially focused.
	st.syncWorkspaces(s)
	return sway.Event{Type: "workspace", Change: "init", Workspace: ws}
}

// syncWorkspaces rewrites the sim's workspace list from the tree.
// Exactly one workspace is marked focused — preserving the previously
// focused name if still present, otherwise the first.
func (st *fuzzState) syncWorkspaces(s *sim.SimSwayClient) {
	wss := make([]sway.Workspace, 0, len(st.workspaces))
	prev, _ := s.GetWorkspaces()
	focusedName := ""
	for _, w := range prev {
		if w.Focused {
			focusedName = w.Name
			break
		}
	}
	names := make([]string, 0, len(st.workspaces))
	for name := range st.workspaces {
		names = append(names, name)
	}
	sort.Strings(names) // map range order is non-deterministic
	first := true
	for _, name := range names {
		focused := false
		switch {
		case focusedName != "" && name == focusedName:
			focused = true
		case focusedName == "" && first:
			focused = true
		}
		first = false
		wss = append(wss, sway.Workspace{
			Name:    name,
			Focused: focused,
		})
	}
	s.SetWorkspaces(wss)
}

// generateEvents picks a random event kind weighted toward the steady-
// state cases, respecting current sim state (e.g. don't issue close on
// an empty workspace). Returns nil if nothing is generatable.
//
// Most kinds produce a single event; dialognew produces a BURST of two
// (window::new with a stale tiled snapshot + window::floating), modeling
// sway emitting back-to-back events faster than the daemon's
// single-threaded dispatch consumes them. The driver must dispatch burst
// members in order with no tree mutation in between.
func (st *fuzzState) generateEvents(rng *rand.Rand, s *sim.SimSwayClient, maxWindows int) []sway.Event {
	if len(st.workspaces) == 0 {
		return nil
	}
	wsNames := make([]string, 0, len(st.workspaces))
	for n := range st.workspaces {
		wsNames = append(wsNames, n)
	}
	sort.Strings(wsNames) // map range order is non-deterministic
	ws := st.workspaces[wsNames[rng.IntN(len(wsNames))]]

	// Weights: new=4, close=2, focus=3, binding=1, floating=1, move=2,
	// wsmove=1, fullscreen=1, dialognew=1, containermove=1.
	const total = 4 + 2 + 3 + 1 + 1 + 2 + 1 + 1 + 1 + 1
	n := rng.IntN(total)
	switch {
	case n < 4:
		return one(st.genNew(s, ws, maxWindows))
	case n < 6:
		return one(st.genClose(rng, ws))
	case n < 9:
		return one(st.genFocus(rng, ws))
	case n < 10:
		return one(st.genBinding(rng))
	case n < 11:
		return one(st.genFloating(ws))
	case n < 13:
		return one(st.genMove(ws))
	case n < 14:
		return one(st.genWorkspaceMove(rng, ws, wsNames))
	case n < 15:
		return one(st.genFullscreen(rng, ws))
	case n < 16:
		return one(st.genContainerWorkspaceMove(rng, ws, wsNames))
	default:
		return st.genDialogNew(s, ws, maxWindows)
	}
}

// genContainerWorkspaceMove models the 2026-06-13 ws7 incident: holding
// the move-to-workspace key while focused on a multi-window container
// relocates the WHOLE subtree to the destination, but sway emits a single
// window::move for the representative leaf — the siblings ride along with
// no event of their own. Moving ws.Nodes[0] takes the source workspace's
// entire managed layout (MasterStack keeps everything under one outer
// wrapper), the maximal version of the bug.
func (st *fuzzState) genContainerWorkspaceMove(rng *rand.Rand, ws *sway.Node, wsNames []string) sway.Event {
	if len(ws.Nodes) == 0 || len(wsNames) < 2 {
		return sway.Event{}
	}
	subtree := ws.Nodes[0]
	leaves := subtree.Leaves()
	if len(leaves) < 2 {
		// Single-window subtree is just genWorkspaceMove; skip so this kind
		// always exercises the multi-window fellow-traveler path.
		return sway.Event{}
	}
	var dest *sway.Node
	for range 8 {
		name := wsNames[rng.IntN(len(wsNames))]
		if name == ws.Name {
			continue
		}
		dest = st.workspaces[name]
		break
	}
	if dest == nil {
		return sway.Event{}
	}
	// Relocate the entire subtree to the destination as one tree op.
	ws.Nodes = ws.Nodes[1:]
	dest.Nodes = append(dest.Nodes, subtree)
	subtree.Parent = dest
	// Sway zeroes pending geometry on a cross-workspace move.
	for _, l := range leaves {
		l.Rect.Width, l.Rect.Height = 0, 0
	}
	rep := leaves[0]
	clearAllFocus(st.root)
	rep.Focused = true
	return sway.Event{Type: "window", Change: "move", Container: rep.Snapshot()}
}

// one wraps a single event as a burst, dropping zero events.
func one(ev sway.Event) []sway.Event {
	if ev.Type == "" {
		return nil
	}
	return []sway.Event{ev}
}

// genDialogNew models a dialog-style window (XDG portal file chooser,
// 1Password popup): sway's view_map attaches it TILED, emits window::new,
// then immediately floats it (wants_floating → container_set_floating)
// and emits window::floating — all before the daemon processes anything.
// The window::new payload is therefore a STALE snapshot claiming the
// window is tiled while the live tree already has it floating.
//
// This is the 2026-06-12 ctrl-s incident shape: MasterStack admitted the
// stale-tiled dialog into its master-insert dance, and the dance's swap
// against the actually-floating dialog silently transferred floatingness
// onto tracked windows (sway swap exchanges positions including floating
// membership and emits no events).
func (st *fuzzState) genDialogNew(s *sim.SimSwayClient, ws *sway.Node, maxWindows int) []sway.Event {
	newEv := st.genNew(s, ws, maxWindows)
	if newEv.Type == "" {
		return nil
	}
	// genNew snapshots the tiled state — that's the stale payload.
	live := st.windows[newEv.Container.ID]
	if live == nil {
		return nil
	}
	// Sway floats the dialog before the daemon reacts: move the LIVE node
	// to the floating list now, mirroring genFloating's tiled→float branch.
	live.Floating = "user_on"
	detachAndCascade(live)
	ws.FloatingNodes = append(ws.FloatingNodes, live)
	live.Parent = ws
	// view_map's real emission order: new (unfocused payload) → floating
	// → focus. The focus tail matters — production MasterStack records the
	// untracked dialog as lastFocusedID, which feeds the next pushWindow's
	// position fallback.
	return []sway.Event{newEv,
		{Type: "window", Change: "floating", Container: live.Snapshot()},
		{Type: "window", Change: "focus", Container: live.Snapshot()}}
}

func (st *fuzzState) genNew(s *sim.SimSwayClient, ws *sway.Node, maxWindows int) sway.Event {
	if len(ws.Leaves()) >= maxWindows {
		return sway.Event{}
	}
	id := s.AllocID()
	leaf := &sway.Node{
		ID:   id,
		Type: "con",
		Name: fmt.Sprintf("fuzz-win-%d", id),
		Rect: sway.Rect{Width: 1280, Height: 720},
	}
	// Sway attaches a newly-mapped view as a sibling of the focused
	// container (sway/tree/view.c:850-871 chooses target_sibling from
	// seat_get_focus_inactive; view.c:898-902 calls container_add_sibling).
	// If focus is on the workspace itself (empty workspace) — no sibling
	// exists yet — it attaches workspace-direct via workspace_add_tiling.
	focused := ws.FindFocused()
	if focused != nil && focused.Type == "con" && !focused.IsFloating() && focused.Parent != nil {
		parent := focused.Parent
		leaf.Parent = parent
		idx := len(parent.Nodes)
		for i, c := range parent.Nodes {
			if c == focused {
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
	st.windows[id] = leaf
	// The window::new payload is snapshotted BEFORE focusing: real sway's
	// view_map emits the new event right after attach and focuses only
	// afterwards (should_focus → input_manager_set_focus), so the payload
	// carries focused=false. The live tree still gets the focus —
	// scopeless binding commands (`move left`, etc.) need a focus target.
	snap := leaf.Snapshot()
	clearAllFocus(st.root)
	leaf.Focused = true
	return sway.Event{Type: "window", Change: "new", Container: snap}
}

func (st *fuzzState) genClose(rng *rand.Rand, ws *sway.Node) sway.Event {
	leaves := ws.Leaves()
	if len(leaves) == 0 {
		return sway.Event{}
	}
	leaf := leaves[rng.IntN(len(leaves))]
	if leaf == nil {
		return sway.Event{}
	}
	return sway.Event{Type: "window", Change: "close", Container: leaf.Snapshot()}
	// Note: leaf is NOT removed from the tree until after HandleEvent
	// — managers may read siblings. Our replay driver detaches after;
	// for the fuzzer, we rely on managers issuing correct commands and
	// the sim's close-related state ending up consistent.
}

func (st *fuzzState) genFocus(rng *rand.Rand, ws *sway.Node) sway.Event {
	leaves := ws.Leaves()
	if len(leaves) == 0 {
		return sway.Event{}
	}
	leaf := leaves[rng.IntN(len(leaves))]
	if leaf == nil {
		return sway.Event{}
	}
	clearAllFocus(st.root)
	leaf.Focused = true
	return sway.Event{Type: "window", Change: "focus", Container: leaf.Snapshot()}
}

// bindingCorpus mirrors every `nop tilekeeper ...` verb the user has bound
// in their sway config. Each entry must round-trip through ParseNopCommand
// and reach a real handler — the fuzzer's no-invalid-cmd / no-sway-reject
// invariants assert that holds across random sequences.
var bindingCorpus = []string{
	// Window navigation
	"nop tilekeeper focus down",
	"nop tilekeeper focus up",
	"nop tilekeeper focus master",
	"nop tilekeeper swap-master",
	// Window movement
	"nop tilekeeper move left",
	"nop tilekeeper move right",
	"nop tilekeeper move up",
	"nop tilekeeper move down",
	// Stack toggles
	"nop tilekeeper stack toggle",
	"nop tilekeeper stack side-toggle",
	// Master count
	"nop tilekeeper master add",
	"nop tilekeeper master remove",
	// Layout switch + layout-level command
	"nop tilekeeper maximize",
	"nop tilekeeper layout MasterStack",
	"nop tilekeeper layout tabbed",
	"nop tilekeeper layout none",
}

func (st *fuzzState) genBinding(rng *rand.Rand) sway.Event {
	return sway.Event{
		Type:    "binding",
		Change:  "run",
		Binding: &sway.Binding{Command: bindingCorpus[rng.IntN(len(bindingCorpus))]},
	}
}

// genMove fires a window::move event for a leaf that stays on the same
// workspace. Sway echoes a move event for every internal reorder — our
// own swap/move commands, `move to workspace <self>` flattens, manual
// drags — so the hub must not treat these as close+add. A same-workspace
// move event arriving between bindings would otherwise silently erase
// the leaf from tracking.
func (st *fuzzState) genMove(ws *sway.Node) sway.Event {
	if len(ws.Nodes) == 0 {
		return sway.Event{}
	}
	leaf := firstLeaf(ws.Nodes[0])
	if leaf == nil {
		return sway.Event{}
	}
	return sway.Event{Type: "window", Change: "move", Container: leaf.Snapshot()}
}

// genWorkspaceMove fires a window::move that relocates a leaf from ws to
// another configured workspace. It mirrors real sway's semantics:
// container_move_to_workspace (sway/commands/move.c:222-235) detaches the
// container, cascade-flattens the former parent chain, attaches it to dest,
// zeroes pending.width/height, then fires ipc_event_window(container,
// "move"). The event JSON has no "old workspace" field — the IPC consumer
// must remember it independently.
//
// We route the actual move through the sim's own `move container to
// workspace` command so that cascade-flatten / parent re-linking matches
// what real sway would produce — bypassing that path (e.g. by popping
// ws.Nodes[0] directly) orphaned every sibling of the chosen leaf when the
// master sat inside a splith wrapper.
func (st *fuzzState) genWorkspaceMove(rng *rand.Rand, ws *sway.Node, wsNames []string) sway.Event {
	if len(ws.Nodes) == 0 || len(wsNames) < 2 {
		return sway.Event{}
	}
	var dest *sway.Node
	for range 8 {
		name := wsNames[rng.IntN(len(wsNames))]
		if name == ws.Name {
			continue
		}
		dest = st.workspaces[name]
		break
	}
	if dest == nil {
		return sway.Event{}
	}
	leaf := firstLeaf(ws.Nodes[0])
	if leaf == nil {
		return sway.Event{}
	}
	leaf.Rect.Width = 0
	leaf.Rect.Height = 0
	detachAndCascade(leaf)
	dest.Nodes = append(dest.Nodes, leaf)
	leaf.Parent = dest
	clearAllFocus(st.root)
	leaf.Focused = true
	return sway.Event{Type: "window", Change: "move", Container: leaf.Snapshot()}
}

// detachAndCascade removes leaf from its parent's Nodes list and walks
// upward, pruning EMPTY wrappers only — mirroring sway's
// container_reap_empty (and the sim's cascadeFlatten, which was corrected
// to match): sway destroys a container only when it has zero children
// and never auto-collapses singletons (those persist until an explicit
// `split none`). An earlier version of this helper also flattened
// single-child wrappers, manufacturing tree shapes real sway cannot
// produce — e.g. floating the lone stack window collapsed the stack
// column, making the master-stack-split invariant fire on a phantom.
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

// genFullscreen toggles a random leaf's fullscreen state and emits the
// window:fullscreen_mode event. Sway enforces "at most one fullscreen
// per workspace" (sway/tree/container.c container_set_fullscreen), so
// enabling on a new target first clears any existing fullscreen leaf on
// the same workspace. Disabling is straight — clear the target.
//
// The mutation is direct tree state, not a sim command: real sway toggles
// fullscreen from the client side (xdg-shell request, F11 in Chromium,
// etc.), not via a tilekeeper-issued sway command. The event is what
// tilekeeper would see on the bus.
func (st *fuzzState) genFullscreen(rng *rand.Rand, ws *sway.Node) sway.Event {
	leaves := ws.Leaves()
	if len(leaves) == 0 {
		return sway.Event{}
	}
	leaf := leaves[rng.IntN(len(leaves))]
	if leaf == nil {
		return sway.Event{}
	}
	if leaf.FullscreenMode == 1 {
		leaf.FullscreenMode = 0
	} else {
		for _, l := range leaves {
			if l != nil && l.FullscreenMode == 1 {
				l.FullscreenMode = 0
			}
		}
		leaf.FullscreenMode = 1
	}
	return sway.Event{Type: "window", Change: "fullscreen_mode", Container: leaf.Snapshot()}
}

func (st *fuzzState) genFloating(ws *sway.Node) sway.Event {
	// Prefer toggling an already-floating leaf back to tiled (so the
	// fuzzer doesn't accumulate floating windows forever). Otherwise pick
	// the first tiled leaf.
	var leaf *sway.Node
	for _, fn := range ws.FloatingNodes {
		if fn.Type == "con" && fn.IsFloating() {
			leaf = fn
			break
		}
	}
	if leaf == nil {
		if len(ws.Nodes) == 0 {
			return sway.Event{}
		}
		leaf = firstLeaf(ws.Nodes[0])
	}
	if leaf == nil {
		return sway.Event{}
	}
	// Real sway moves the container between the tiled tree and the
	// workspace's floating list on each transition (sway/desktop/transaction.c
	// → container_set_floating). Mirror that here so IsExcluded sees the
	// post-toggle parent — otherwise toggling tiled→float→tiled leaves the
	// container parented under a stacked wrapper and managers can't re-add it.
	if leaf.IsFloating() {
		// float → tiled
		leaf.Floating = "user_off"
		for i, fn := range ws.FloatingNodes {
			if fn == leaf {
				ws.FloatingNodes = append(ws.FloatingNodes[:i], ws.FloatingNodes[i+1:]...)
				break
			}
		}
		ws.Nodes = append(ws.Nodes, leaf)
		leaf.Parent = ws
	} else {
		// tiled → float
		leaf.Floating = "user_on"
		detachAndCascade(leaf)
		ws.FloatingNodes = append(ws.FloatingNodes, leaf)
		leaf.Parent = ws
	}
	return sway.Event{Type: "window", Change: "floating", Container: leaf.Snapshot()}
}

func firstLeaf(n *sway.Node) *sway.Node {
	if len(n.Nodes) == 0 {
		return n
	}
	return firstLeaf(n.Nodes[0])
}

func clearAllFocus(ws *sway.Node) {
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
	walk(ws)
}
