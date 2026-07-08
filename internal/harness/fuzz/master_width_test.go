package fuzz

import (
	"testing"

	"github.com/mschulkind-oss/tilekeeper/internal/config"
	"github.com/mschulkind-oss/tilekeeper/internal/harness/sim"
	"github.com/mschulkind-oss/tilekeeper/internal/layout"
	"github.com/mschulkind-oss/tilekeeper/internal/sway"
	"github.com/mschulkind-oss/tilekeeper/internal/workspace"
)

// TestMasterWidth_FreshArrange_NineWindows mirrors the live ws7 cold-start
// shape (master + splitv stack with substack of 6 leaves). Master must
// still end at 0.75 of its parent.
func TestMasterWidth_FreshArrange_NineWindows(t *testing.T) {
	masterWidthN(t, 9)
}

// TestMasterWidth_FreshArrange_FiveWindows is enough windows to exercise
// substack creation (visibleStackLimit=3 + master = 4, so 5 triggers it).
func TestMasterWidth_FreshArrange_FiveWindows(t *testing.T) {
	masterWidthN(t, 5)
}

func masterWidthN(t *testing.T, n int) {
	t.Helper()
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

	for i := range n {
		w := &sway.Node{ID: int64(2000 + i), Type: "con", Rect: sway.Rect{Width: 1280, Height: 720}, Name: "w"}
		ws.Nodes = append(ws.Nodes, w)
		w.Parent = ws
		hub.HandleEvent(sway.Event{Type: "window", Change: "new", Container: w})
	}

	mgr := hub.Manager("7").(*layout.MasterStack)
	ids := mgr.WindowIDs()
	if len(ids) < 2 {
		t.Fatalf("expected ≥2 tracked, got %v", ids)
	}
	tree, _ := s.GetTree()
	wsNode := tree.Workspaces()[0]
	master := wsNode.FindByID(ids[0])
	if master == nil {
		t.Fatalf("master id=%d not in tree", ids[0])
	}
	want := 0.75
	if master.Percent < want-0.02 || master.Percent > want+0.02 {
		t.Errorf("master.Percent = %.3f, want %.3f (±0.02) after %d adds; tracked=%v",
			master.Percent, want, n, ids)
	}
}

// TestMasterWidth_PreExistingMasterStackTree mirrors the live ws7 cold-start
// scenario the user hit on 2026-04-25: tilekeeper starts against a
// workspace whose tree already carries a master/stack shape from a
// previous session (because sway preserves the tree across a tilekeeper
// restart). arrangeWindows must rebuild from this state and still leave
// master at 0.75 — the bug was master ending at exactly half (0.375).
func TestMasterWidth_PreExistingMasterStackTree(t *testing.T) {
	ws := sway.CreateWorkspace("7", 0)
	ws.Rect = sway.Rect{Width: 2560, Height: 1440}

	// Build a master/stack/substack tree that mirrors what a previous
	// tilekeeper session would have left behind: 1 master (id=2000, leftmost) +
	// splitv stack column (id=2100) containing 2 leaves (2001, 2002)
	// and a stacked substack (id=2200) with 6 more leaves. Total: 9.
	master := &sway.Node{ID: 2000, Type: "con", Rect: sway.Rect{Width: 1920, Height: 1440}, Percent: 0.75, Name: "master"}
	stackCol := &sway.Node{ID: 2100, Type: "con", Layout: "splitv", Rect: sway.Rect{Width: 640, Height: 1440}, Percent: 0.25}
	stackCol.Nodes = []*sway.Node{
		{ID: 2001, Type: "con", Percent: 1.0 / 3, Rect: sway.Rect{Width: 640, Height: 480}, Name: "stack0"},
		{ID: 2002, Type: "con", Percent: 1.0 / 3, Rect: sway.Rect{Width: 640, Height: 480}, Name: "stack1"},
	}
	sub := &sway.Node{ID: 2200, Type: "con", Layout: "stacked", Percent: 1.0 / 3, Rect: sway.Rect{Width: 640, Height: 480}}
	for i := range 6 {
		sub.Nodes = append(sub.Nodes, &sway.Node{
			ID: int64(2003 + i), Type: "con", Percent: 1.0, Rect: sway.Rect{Width: 640, Height: 480}, Name: "sub",
		})
	}
	stackCol.Nodes = append(stackCol.Nodes, sub)
	ws.Nodes = []*sway.Node{master, stackCol}

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

	// arrangeExisting equivalent: get tree, run ArrangeAll on the workspace.
	tree, _ := s.GetTree()
	wsNode := tree.Workspaces()[0]
	mgr := hub.Manager("7").(*layout.MasterStack)
	if err := mgr.ArrangeAll(wsNode); err != nil {
		t.Fatalf("ArrangeAll: %v", err)
	}

	tree2, _ := s.GetTree()
	wsNode2 := tree2.Workspaces()[0]
	ids := mgr.WindowIDs()
	if len(ids) < 2 {
		t.Fatalf("tracked < 2: %v", ids)
	}
	masterAfter := wsNode2.FindByID(ids[0])
	if masterAfter == nil {
		t.Fatalf("master id=%d not in tree", ids[0])
	}
	want := 0.75
	if masterAfter.Percent < want-0.02 || masterAfter.Percent > want+0.02 {
		t.Errorf("master.Percent = %.3f, want %.3f (±0.02); tracked=%v",
			masterAfter.Percent, want, ids)
	}
}

// TestMasterWidth_FreshArrange_TwoWindows is the canonical baseline: two
// windows on a MasterStack workspace, ArrangeAll runs once. Master must
// end at MasterWidth/100 = 0.75.
func TestMasterWidth_FreshArrange_TwoWindows(t *testing.T) {
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

	// Add two windows. Each WindowAdded triggers pushWindow inside the manager.
	for i := range 2 {
		w := &sway.Node{ID: int64(2000 + i), Type: "con", Rect: sway.Rect{Width: 1280, Height: 720}, Name: "w"}
		ws.Nodes = append(ws.Nodes, w)
		w.Parent = ws
		hub.HandleEvent(sway.Event{Type: "window", Change: "new", Container: w})
	}

	mgr := hub.Manager("7").(*layout.MasterStack)
	ids := mgr.WindowIDs()
	if len(ids) < 2 {
		t.Fatalf("expected ≥2 tracked, got %v", ids)
	}
	tree, _ := s.GetTree()
	wsNode := tree.Workspaces()[0]
	master := wsNode.FindByID(ids[0])
	if master == nil {
		t.Fatalf("master id=%d not in tree", ids[0])
	}
	want := 0.75
	if master.Percent < want-0.02 || master.Percent > want+0.02 {
		t.Errorf("master.Percent = %.3f, want %.3f (±0.02)", master.Percent, want)
	}
}
