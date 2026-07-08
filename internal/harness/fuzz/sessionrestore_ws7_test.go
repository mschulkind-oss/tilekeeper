package fuzz

import (
	"log/slog"
	"testing"

	"github.com/mschulkind-oss/tilekeeper/internal/config"
	"github.com/mschulkind-oss/tilekeeper/internal/harness/sim"
	"github.com/mschulkind-oss/tilekeeper/internal/layout"
	"github.com/mschulkind-oss/tilekeeper/internal/sway"
	"github.com/mschulkind-oss/tilekeeper/internal/workspace"
)

// buildLiveWs7TwoColumn reconstructs the live ws7 shape observed
// 2026-06-13 after a session-restore bulk move from ws9:
//
//	ws7 (32, splith)
//	  con 33 (splith)
//	    con 22 (splitv): 27, 28, 26, con31[stacked: 25 24 23 20 19 18 17 15]
//	    con 34 (splitv): 29, 30
//
// 13 leaves total, none floating. The manager tracked only [28 29 30]
// (the 3 windows that arrived via clean per-window ws9->ws7 moves); the
// other 10 were relocated as part of container moves that never produced
// dispatched per-window move events, so they sit on ws7 untracked. The
// two splitv columns are the leftover ws9 master/stack shape.
func buildLiveWs7TwoColumn() (root, ws *sway.Node) {
	leaf := func(id int64, name string) *sway.Node {
		return &sway.Node{ID: id, Type: "con", Name: name, Rect: sway.Rect{Width: 800, Height: 400}}
	}
	stack := &sway.Node{ID: 31, Type: "con", Layout: "stacked"}
	for _, id := range []int64{25, 24, 23, 20, 19, 18, 17, 15} {
		stack.Nodes = append(stack.Nodes, leaf(id, "stack-win"))
	}
	col1 := &sway.Node{ID: 22, Type: "con", Layout: "splitv", Nodes: []*sway.Node{
		leaf(27, "win-a"), leaf(28, "win-b"), leaf(26, "win-c"), stack,
	}}
	col2 := &sway.Node{ID: 34, Type: "con", Layout: "splitv", Nodes: []*sway.Node{
		leaf(29, "win-d"), leaf(30, "win-e"),
	}}
	inner := &sway.Node{ID: 33, Type: "con", Layout: "splith", Nodes: []*sway.Node{col1, col2}}
	ws = &sway.Node{ID: 32, Type: "workspace", Name: "7", Layout: "splith", Nodes: []*sway.Node{inner}}
	output := &sway.Node{ID: 99, Type: "output", Name: "DP-3", Nodes: []*sway.Node{ws}}
	root = &sway.Node{ID: 1, Type: "root", Nodes: []*sway.Node{output}}
	root.SetParents()
	return root, ws
}

// TestLiveWs7RestartRescue proves a daemon restart rescues the live
// 2026-06-13 ws7 desync: arrangeExisting -> ArrangeAll on the populated
// tree rebuilds the broken two-column shape into a single master/stack
// split and adopts ALL 13 windows into tracking. This is what makes
// "restart tilekeeper" a valid live fix once the session-restore
// storm has settled.
func TestLiveWs7RestartRescue(t *testing.T) {
	root, ws := buildLiveWs7TwoColumn()
	s := sim.NewWithTree(root, []sway.Workspace{{Num: 7, Name: "7", Focused: true}})
	logger := slog.New(slog.NewTextHandler(&nopW{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))

	hub := workspace.NewHub(s, config.Config{
		General: config.GeneralConfig{DefaultLayout: "none", MasterWidth: 75, VisibleStackLimit: 3},
		Workspaces: map[string]config.WorkspaceConfig{
			"7": {DefaultLayout: "MasterStack"},
		},
	}, logger)
	hub.Initialize()
	mgr := hub.Manager("7")
	ms := mgr.(*layout.MasterStack)

	t.Log("BEFORE restart-arrange (live two-column desync):")
	dumpTree(t, ws)

	// This is exactly what arrangeExisting does per managed workspace.
	if err := mgr.ArrangeAll(ws); err != nil {
		t.Fatalf("ArrangeAll: %v", err)
	}

	tree, _ := s.GetTree()
	ws = findWorkspace(tree, "7")
	t.Log("AFTER restart-arrange:")
	dumpTree(t, ws)
	t.Logf("tracked=%v", ms.WindowIDs())

	// All 13 windows must now be tracked.
	want := map[int64]bool{15: true, 17: true, 18: true, 19: true, 20: true,
		23: true, 24: true, 25: true, 26: true, 27: true, 28: true, 29: true, 30: true}
	got := map[int64]bool{}
	for _, id := range ms.WindowIDs() {
		got[id] = true
	}
	if len(ms.WindowIDs()) != 13 {
		t.Errorf("tracked %d windows, want 13: %v", len(ms.WindowIDs()), ms.WindowIDs())
	}
	for id := range want {
		if !got[id] {
			t.Errorf("window %d not adopted into tracking after restart-arrange", id)
		}
	}

	// Structure: master and stack must not share a parent — a real
	// master/stack split, not two side-by-side columns.
	ids := ms.WindowIDs()
	master := ws.FindByID(ids[0])
	if master == nil || master.Parent == nil {
		t.Fatalf("master %d missing after arrange", ids[0])
	}
	for _, sid := range ids[1:] {
		n := ws.FindByID(sid)
		if n != nil && n.Parent == master.Parent {
			t.Errorf("master %d and stack %d still share parent %d — not rebuilt\n%s",
				ids[0], sid, master.Parent.ID, dumpTreeStr(tree))
		}
	}

	// No tracked window left floating, no singleton chain > 1.
	assertNoTrackedFloats(t, "after restart-arrange", ms, ws)
	if depth, path := longestSingletonChain(tree); depth > 1 {
		t.Errorf("singleton chain depth=%d path=%s after arrange", depth, path)
	}
}
