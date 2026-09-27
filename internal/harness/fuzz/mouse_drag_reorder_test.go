package fuzz

import (
	"log/slog"
	"reflect"
	"testing"

	"github.com/mschulkind-oss/tilekeeper/internal/config"
	"github.com/mschulkind-oss/tilekeeper/internal/harness/sim"
	"github.com/mschulkind-oss/tilekeeper/internal/layout"
	"github.com/mschulkind-oss/tilekeeper/internal/sway"
	"github.com/mschulkind-oss/tilekeeper/internal/workspace"
)

// TestMouseDragReorder_SubstackWindows reproduces the user's bug report:
// A 5-window MasterStack workspace with VisibleStackLimit=3 has 1 master,
// 2 visible stack windows, and 2 collapsed substack windows at the bottom.
// Reordering one of the collapsed substack windows by mouse reorders it in Sway's
// container tree and emits a same-workspace window::move event.
//
// Before the fix, handleWindowMove treats same-workspace move as a no-op,
// leaving MasterStack.WindowIDs in the stale pre-drag order. Subsequent
// keyboard navigation (e.g. `focus down`) then traverses windows out of visual order.
func TestMouseDragReorder_SubstackWindows(t *testing.T) {
	s := sim.New()
	logger := slog.New(slog.NewTextHandler(&nopW{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))

	hub := workspace.NewHub(s, config.Config{
		General: config.GeneralConfig{
			DefaultLayout:     "none",
			MasterWidth:       75,
			VisibleStackLimit: 3,
		},
		Workspaces: map[string]config.WorkspaceConfig{
			"7": {DefaultLayout: "MasterStack"},
		},
	}, logger)
	hub.Initialize()

	state := newFuzzState([]string{"7"})
	hub.HandleEvent(state.initWorkspace(s, "7"))

	// Create 5 windows: master + 2 visible stack + 2 substack.
	for range 5 {
		hub.HandleEvent(state.genNew(s, state.workspaces["7"], 100))
	}

	ms := hub.Manager("7").(*layout.MasterStack)
	initialIDs := append([]int64(nil), ms.WindowIDs()...)
	if len(initialIDs) != 5 {
		t.Fatalf("expected 5 tracked windows, got %v", initialIDs)
	}
	t.Logf("initial windowIDs: %v", initialIDs)

	tree, err := s.GetTree()
	if err != nil {
		t.Fatalf("GetTree: %v", err)
	}
	tree.SetParents()
	wsNode := findWorkspace(tree, "7")

	// Find the substack container (Layout == "stacked").
	var substack *sway.Node
	for _, n := range wsNode.Nodes {
		var walk func(*sway.Node)
		walk = func(curr *sway.Node) {
			if curr.Layout == "stacked" && len(curr.Nodes) >= 2 {
				substack = curr
				return
			}
			for _, child := range curr.Nodes {
				walk(child)
			}
		}
		walk(n)
	}
	if substack == nil {
		t.Fatalf("substack container not found in tree")
	}
	if len(substack.Nodes) != 2 {
		t.Fatalf("expected 2 substack nodes, got %d", len(substack.Nodes))
	}

	sub0 := substack.Nodes[0]
	sub1 := substack.Nodes[1]
	t.Logf("substack before drag: [%d, %d]", sub0.ID, sub1.ID)

	// Simulate user dragging sub1 before sub0 with mouse:
	// In Sway, the container's children slice is reordered.
	substack.Nodes[0], substack.Nodes[1] = sub1, sub0

	// Sway emits window::move for the moved container.
	moveEv := sway.Event{
		Type:      "window",
		Change:    "move",
		Container: sub1.Snapshot(),
	}
	hub.HandleEvent(moveEv)

	wantIDs := []int64{initialIDs[0], initialIDs[1], initialIDs[2], sub1.ID, sub0.ID}
	gotIDs := ms.WindowIDs()
	t.Logf("windowIDs after mouse drag: got %v, want %v", gotIDs, wantIDs)

	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Errorf("after mouse drag in substack: WindowIDs = %v, want %v", gotIDs, wantIDs)
	}

	// Now verify keyboard navigation: focus the last visible stack window (initialIDs[2]).
	// `focus down` must move to sub1 (the new top of substack), not sub0.
	clearAllFocus(tree)
	wsNode.FindByID(initialIDs[2]).Focused = true

	// Simulate binding `nop tilekeeper focus down`
	hub.HandleEvent(sway.Event{
		Type:   "binding",
		Change: "run",
		Binding: &sway.Binding{
			Command: "nop tilekeeper focus down",
		},
	})

	freshTree, _ := s.GetTree()
	focused := freshTree.FindFocused()
	if focused == nil {
		t.Fatalf("no focused window after focus down")
	}
	if focused.ID != sub1.ID {
		t.Errorf("focus down after mouse drag focused %d (%s), want %d (%s)",
			focused.ID, focused.Name, sub1.ID, sub1.Name)
	}
}

// TestMouseDragReorder_VisibleStackWindows tests dragging a window within the visible stack.
func TestMouseDragReorder_VisibleStackWindows(t *testing.T) {
	s := sim.New()
	logger := slog.New(slog.NewTextHandler(&nopW{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))

	hub := workspace.NewHub(s, config.Config{
		General: config.GeneralConfig{
			DefaultLayout:     "none",
			MasterWidth:       75,
			VisibleStackLimit: 3,
		},
		Workspaces: map[string]config.WorkspaceConfig{
			"7": {DefaultLayout: "MasterStack"},
		},
	}, logger)
	hub.Initialize()

	state := newFuzzState([]string{"7"})
	hub.HandleEvent(state.initWorkspace(s, "7"))

	// Create 3 windows: master + 2 visible stack windows.
	for range 3 {
		hub.HandleEvent(state.genNew(s, state.workspaces["7"], 100))
	}

	ms := hub.Manager("7").(*layout.MasterStack)
	initialIDs := append([]int64(nil), ms.WindowIDs()...)
	if len(initialIDs) != 3 {
		t.Fatalf("expected 3 tracked windows, got %v", initialIDs)
	}

	tree, _ := s.GetTree()
	tree.SetParents()
	wsNode := findWorkspace(tree, "7")

	// Find the stack column wrapper
	win1 := wsNode.FindByID(initialIDs[1])
	win2 := wsNode.FindByID(initialIDs[2])
	if win1 == nil || win2 == nil || win1.Parent == nil || win1.Parent != win2.Parent {
		t.Fatalf("stack windows not siblings")
	}
	stackCol := win1.Parent

	// Drag win2 above win1
	stackCol.Nodes[0], stackCol.Nodes[1] = win2, win1

	hub.HandleEvent(sway.Event{
		Type:      "window",
		Change:    "move",
		Container: win2.Snapshot(),
	})

	wantIDs := []int64{initialIDs[0], win2.ID, win1.ID}
	gotIDs := ms.WindowIDs()
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Errorf("after mouse drag in visible stack: WindowIDs = %v, want %v", gotIDs, wantIDs)
	}
}
