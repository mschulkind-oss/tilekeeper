package fuzz

import (
	"slices"
	"testing"

	"github.com/mschulkind-oss/tilekeeper/internal/config"
	"github.com/mschulkind-oss/tilekeeper/internal/harness/sim"
	"github.com/mschulkind-oss/tilekeeper/internal/layout"
	"github.com/mschulkind-oss/tilekeeper/internal/sway"
	"github.com/mschulkind-oss/tilekeeper/internal/workspace"
)

// swap-master promotes with MRU (alt-tab) ordering: the old master lands at
// the top of the stack rather than trading places with the promoted window,
// so that focus-stack-then-promote alternates between the same two windows.
//
// These tests drive the manager against the sim rather than sway.Mock on
// purpose. The mock records commands without restructuring the tree, so a
// mock test can only prove the tracked bookkeeping — it would pass even if
// the emitted `swap container` chain put the old master somewhere else
// entirely. The whole point of MRU order is where the old master ends up
// *on screen*, so the assertion has to be against a tree that actually
// models sway's response to the commands. (The sim's `swap container`
// fidelity is pinned against headless sway by the swap-two-tiled scenario
// in cmd/sway-difftest.)
//
// The fuzzer cannot catch a regression here: its tracked-matches-leaves
// invariant compares tracked and tree as *sets*, so any permutation passes.
// Ordering is a layout-semantics contract, not a structural invariant.
func newMRUHub(t *testing.T, windows int) (*workspace.Hub, *sim.SimSwayClient, *layout.MasterStack) {
	t.Helper()

	ws := sway.CreateWorkspace("7", 0)
	output := &sway.Node{ID: 10, Name: "eDP-1", Type: "output", Nodes: []*sway.Node{ws}}
	root := &sway.Node{ID: 1, Type: "root", Nodes: []*sway.Node{output}}
	root.SetParents()
	s := sim.NewWithTree(root, []sway.Workspace{{Num: 7, Name: "7", Focused: true}})

	hub := workspace.NewHub(s, config.Config{
		General: config.GeneralConfig{DefaultLayout: "none", MasterWidth: 50},
		Workspaces: map[string]config.WorkspaceConfig{
			"7": {DefaultLayout: "MasterStack", StackLayout: "splitv", StackSide: "right"},
		},
	}, nil)
	hub.Initialize()

	for i := range windows {
		id := int64(1001 + i)
		w := &sway.Node{ID: id, Type: "con", Name: "w", Rect: sway.Rect{Width: 1280, Height: 720}}
		tree, _ := s.GetTree()
		wsNode := findWorkspaceNode(tree, "7")
		if focused := wsNode.FindFocused(); focused != nil && focused.Type == "con" && focused.Parent != nil {
			focused.Parent.Nodes = append(focused.Parent.Nodes, w)
			w.Parent = focused.Parent
		} else {
			wsNode.Nodes = append(wsNode.Nodes, w)
			w.Parent = wsNode
		}
		clearAllFocusInTree(tree)
		w.Focused = true
		hub.HandleEvent(sway.Event{Type: "window", Change: "new", Container: w})
		hub.HandleEvent(sway.Event{Type: "window", Change: "focus", Container: w})
	}

	mgr, ok := hub.Manager("7").(*layout.MasterStack)
	if !ok {
		t.Fatalf("manager for ws7 is %T, want *layout.MasterStack", hub.Manager("7"))
	}
	if got := len(mgr.WindowIDs()); got != windows {
		t.Fatalf("tracked window count = %d, want %d", got, windows)
	}
	return hub, s, mgr
}

// promote focuses id and runs swap-master, then returns the tiled-leaf
// order the sim's tree actually ended up in.
func promote(t *testing.T, hub *workspace.Hub, s *sim.SimSwayClient, id int64) []int64 {
	t.Helper()

	tree, _ := s.GetTree()
	wsNode := findWorkspaceNode(tree, "7")
	target := wsNode.FindByID(id)
	if target == nil {
		t.Fatalf("window %d not in tree:\n%s", id, dumpTreeStr(tree))
	}
	clearAllFocusInTree(tree)
	target.Focused = true
	hub.HandleEvent(sway.Event{Type: "window", Change: "focus", Container: target})
	hub.HandleEvent(sway.Event{Type: "binding", Binding: &sway.Binding{Command: "nop tilekeeper swap-master"}})

	tree, _ = s.GetTree()
	return leafIDs(findWorkspaceNode(tree, "7"))
}

// Promoting a window from deep in the stack must shift the old master to
// the top of the stack, not exile it to the promoted window's old slot.
func TestSwapMasterMRUOrderInTree(t *testing.T) {
	hub, s, mgr := newMRUHub(t, 4)

	ids := slices.Clone(mgr.WindowIDs())
	a, b, c, d := ids[0], ids[1], ids[2], ids[3]

	gotTree := promote(t, hub, s, d)
	want := []int64{d, a, b, c}

	if gotTracked := mgr.WindowIDs(); !slices.Equal(gotTracked, want) {
		t.Errorf("tracked order = %v, want %v (old master to top of stack)", gotTracked, want)
	}
	if !slices.Equal(gotTree, want) {
		tree, _ := s.GetTree()
		t.Errorf("tree leaf order = %v, want %v — the emitted swap chain did not\n"+
			"land the old master at the top of the stack:\n%s", gotTree, want, dumpTreeStr(tree))
	}
}

// $mod+o then $mod+Return, repeatedly: focusing the top of the stack and
// promoting it must alternate between the same two windows and leave the
// rest of the stack alone.
func TestSwapMasterAltTabCycleInTree(t *testing.T) {
	hub, s, mgr := newMRUHub(t, 4)

	ids := slices.Clone(mgr.WindowIDs())
	a, b, c, d := ids[0], ids[1], ids[2], ids[3]

	// Seed the cycle from a deep promote so the tail is non-trivial.
	if got := promote(t, hub, s, d); !slices.Equal(got, []int64{d, a, b, c}) {
		t.Fatalf("seed promote: tree = %v, want %v", got, []int64{d, a, b, c})
	}

	for round, want := range [][]int64{
		{a, d, b, c},
		{d, a, b, c},
		{a, d, b, c},
		{d, a, b, c},
	} {
		top := mgr.WindowIDs()[1]
		got := promote(t, hub, s, top)
		if !slices.Equal(got, want) {
			tree, _ := s.GetTree()
			t.Fatalf("round %d: tree = %v, want %v\n%s", round, got, want, dumpTreeStr(tree))
		}
	}

	if got := mgr.WindowIDs()[2:]; !slices.Equal(got, []int64{b, c}) {
		t.Errorf("tail = %v, want %v — cycling disturbed the rest of the stack",
			got, []int64{b, c})
	}
}
