package fuzz

import (
	"testing"

	"github.com/mschulkind-oss/tilekeeper/internal/config"
	"github.com/mschulkind-oss/tilekeeper/internal/harness/sim"
	"github.com/mschulkind-oss/tilekeeper/internal/layout"
	"github.com/mschulkind-oss/tilekeeper/internal/sway"
	"github.com/mschulkind-oss/tilekeeper/internal/workspace"
)

// TestMasterStackSplit_StackColumnSurvivesPostFlatten pins the 2026-05-25
// "Chromium relaunch → 3 vertical stripes where the single master should
// be" bug. Reproducer is small: empty workspace, add 2 windows (triggers
// MasterStack's 2-window master/stack split), then add a 3rd.
//
// Bug shape: pushWindow's 2-window branch creates the splitv stack-column
// wrapper, but it has exactly one child (stack[0]), so the post-pushWindow
// flattenFreshForTracked sees a singleton and lifts stack[0] out — the
// stack-column wrapper is destroyed. The outer-splith now holds master
// and stack[0] as direct siblings.
//
// When the 3rd window arrives, insertAtIndex(0)'s `move ex-master to mark
// on stack[0]` lands ex-master as another direct sibling in the outer
// splith, because stack[0] no longer lives in its own wrapper. Result:
// outer splith = [new-master, ex-master, original-stack[0]] — three
// leaves in the master row.
//
// The fix protects the stack-column wrapper from flattenWorkspace's
// singleton-collapse. Assertion: after 3 adds, master.Parent ≠
// stack[0].Parent (the stack column actually exists as a distinct node).
func TestMasterStackSplit_StackColumnSurvivesPostFlatten(t *testing.T) {
	ws := sway.CreateWorkspace("7", 0)
	output := &sway.Node{ID: 10, Name: "eDP-1", Type: "output", Nodes: []*sway.Node{ws}}
	root := &sway.Node{ID: 1, Type: "root", Nodes: []*sway.Node{output}}
	root.SetParents()
	s := sim.NewWithTree(root, []sway.Workspace{{Num: 7, Name: "7", Focused: true}})

	hub := workspace.NewHub(s, config.Config{
		General: config.GeneralConfig{DefaultLayout: "none", MasterWidth: 75},
		Workspaces: map[string]config.WorkspaceConfig{
			"7": {DefaultLayout: "MasterStack"},
		},
	}, nil)
	hub.Initialize()

	// Add 3 windows in sequence, mimicking what sway does on `new`:
	// attach as sibling of focused window (or as workspace-direct child
	// for the first one), then focus the new one.
	addWindow := func(id int64) {
		w := &sway.Node{ID: id, Type: "con", Rect: sway.Rect{Width: 1280, Height: 720}, Name: "w"}
		// Sway attaches new views as siblings of the focused container —
		// or, on an empty workspace, workspace-direct.
		tree, _ := s.GetTree()
		wsNode := findWorkspaceNode(tree, "7")
		focused := wsNode.FindFocused()
		if focused != nil && focused.Type == "con" && focused.Parent != nil {
			parent := focused.Parent
			parent.Nodes = append(parent.Nodes, w)
			w.Parent = parent
		} else {
			wsNode.Nodes = append(wsNode.Nodes, w)
			w.Parent = wsNode
		}
		clearAllFocusInTree(tree)
		w.Focused = true
		hub.HandleEvent(sway.Event{Type: "window", Change: "new", Container: w})
		hub.HandleEvent(sway.Event{Type: "window", Change: "focus", Container: w})
	}

	addWindow(1001)
	addWindow(1002)
	addWindow(1003)

	mgr := hub.Manager("7").(*layout.MasterStack)
	ids := mgr.WindowIDs()
	if len(ids) != 3 {
		t.Fatalf("tracked window count = %d, want 3 (tracked=%v)", len(ids), ids)
	}

	tree, _ := s.GetTree()
	wsNode := findWorkspaceNode(tree, "7")
	master := wsNode.FindByID(ids[0])
	if master == nil || master.Parent == nil {
		t.Fatalf("master id=%d not found or has no parent (tree dump:\n%s)",
			ids[0], dumpTreeStr(tree))
	}
	masterParent := master.Parent

	for _, sid := range ids[1:] {
		stackNode := wsNode.FindByID(sid)
		if stackNode == nil || stackNode.Parent == nil {
			t.Fatalf("stack id=%d not found or has no parent (tree dump:\n%s)",
				sid, dumpTreeStr(tree))
		}
		if stackNode.Parent == masterParent {
			t.Errorf("BUG: master id=%d and stack id=%d share parent id=%d (layout=%s) — "+
				"stack-column wrapper is missing; tracked=%v\ntree dump:\n%s",
				ids[0], sid, masterParent.ID, masterParent.Layout, ids,
				dumpTreeStr(tree))
		}
	}
}

// (The close-then-add variant of this scenario exposes a related bug
// that lives partly in the sim's moveDir: real sway's `move left/right`
// can cross container boundaries — the sim only swaps siblings within
// the current parent. popWindow's master-promotion relies on the
// cross-boundary behavior. Worth a separate fix to the sim, but it
// would shift fuzz counts more broadly than the targeted fix here.)

// findWorkspaceNode returns the named workspace from the tree, or nil.
func findWorkspaceNode(tree *sway.Node, name string) *sway.Node {
	if tree == nil {
		return nil
	}
	for _, ws := range tree.Workspaces() {
		if ws.Name == name {
			return ws
		}
	}
	return nil
}

// clearAllFocusInTree walks the whole tree and clears Focused on every node.
func clearAllFocusInTree(n *sway.Node) {
	if n == nil {
		return
	}
	n.Focused = false
	for _, c := range n.Nodes {
		clearAllFocusInTree(c)
	}
	for _, c := range n.FloatingNodes {
		clearAllFocusInTree(c)
	}
}
