package fuzz

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/mschulkind-oss/tilekeeper/internal/config"
	"github.com/mschulkind-oss/tilekeeper/internal/harness/sim"
	"github.com/mschulkind-oss/tilekeeper/internal/layout"
	"github.com/mschulkind-oss/tilekeeper/internal/sway"
	"github.com/mschulkind-oss/tilekeeper/internal/workspace"
)

// buildLiveWs7Tree reconstructs the tree shape that was observed on live
// ws7 at 2026-04-19 21:19 EDT after the daemon restarted and ran
// arrangeExisting:
//
//	[16] workspace splith n=1
//	  [224] con splith n=1
//	    [235] con splith n=1
//	      [236] con splitv n=4
//	        [29] leaf
//	        [28] leaf
//	        [21] leaf      <- master per tracked_after=[21, ...]
//	        [237] con stacked n=13
//	          [27, 114, 26, 120, 24, 22, 18, 15, 30, 32, 25, 33, 31]
//
// 21 is tracked as windowIDs[0] but lives INSIDE the stack column
// alongside 29, 28, and the stacked substack. Two outer splith
// singleton wrappers (224, 235) remain. This shape is idempotent
// under the current arrangeWindows, which is the bug.
func buildLiveWs7Tree() (root *sway.Node, ws *sway.Node) {
	substack := &sway.Node{ID: 237, Type: "con", Layout: "stacked"}
	for _, id := range []int64{27, 114, 26, 120, 24, 22, 18, 15, 30, 32, 25, 33, 31} {
		substack.Nodes = append(substack.Nodes,
			&sway.Node{ID: id, Type: "con", Rect: sway.Rect{Width: 400, Height: 300}})
	}
	stackCol := &sway.Node{
		ID: 236, Type: "con", Layout: "splitv",
		Nodes: []*sway.Node{
			{ID: 29, Type: "con", Rect: sway.Rect{Width: 400, Height: 300}},
			{ID: 28, Type: "con", Rect: sway.Rect{Width: 400, Height: 300}},
			{ID: 21, Type: "con", Rect: sway.Rect{Width: 400, Height: 1080}},
			substack,
		},
	}
	wrapInner := &sway.Node{ID: 235, Type: "con", Layout: "splith", Nodes: []*sway.Node{stackCol}}
	wrapOuter := &sway.Node{ID: 224, Type: "con", Layout: "splith", Nodes: []*sway.Node{wrapInner}}
	ws = &sway.Node{
		ID: 16, Type: "workspace", Name: "7", Layout: "splith",
		Nodes: []*sway.Node{wrapOuter},
	}
	output := &sway.Node{ID: 238, Type: "output", Name: "DP-3", Nodes: []*sway.Node{ws}}
	root = &sway.Node{ID: 1, Type: "root", Layout: "splith", Nodes: []*sway.Node{output}}
	root.SetParents()
	return root, ws
}

// dumpTree prints an indented walk of n's subtree. Useful for diagnosing
// the arrange outcome when the test fails.
func dumpTree(t *testing.T, n *sway.Node) {
	t.Helper()
	var walk func(n *sway.Node, depth int)
	walk = func(n *sway.Node, depth int) {
		pad := strings.Repeat("  ", depth)
		t.Logf("%s[%d] %s %s n=%d", pad, n.ID, n.Type, n.Layout, len(n.Nodes))
		for _, c := range n.Nodes {
			walk(c, depth+1)
		}
	}
	walk(n, 0)
}

// TestArrangeOnLiveWs7ReproducesLostMaster drives the MasterStack manager
// against the observed pre-arrange ws7 tree. The arranged result must:
//   - put master (con 21) alongside a single stack column, not folded
//     into the stack column as a third visible item.
//   - not leave any outer singleton splith wrappers between the workspace
//     and the new master/stack split.
func TestArrangeOnLiveWs7ReproducesLostMaster(t *testing.T) {
	root, ws := buildLiveWs7Tree()

	// Focus mirrors what the live journal implied: master 21 was the
	// tracked first id, i.e. the focused leaf at restart time.
	master := ws.FindByID(21)
	master.Focused = true

	s := sim.NewWithTree(root, []sway.Workspace{{Name: "7", Focused: true}})
	logger := slog.New(slog.NewTextHandler(&nopW{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))

	hub := workspace.NewHub(s, config.Config{
		General: config.GeneralConfig{
			DefaultLayout: "none", MasterWidth: 75,
		},
		Workspaces: map[string]config.WorkspaceConfig{
			"7": {
				DefaultLayout:     "MasterStack",
				StackLayout:       "splitv",
				StackSide:         "right",
				VisibleStackLimit: 3,
			},
		},
	}, logger)
	hub.Initialize()

	mgr := hub.Manager("7")
	if mgr == nil {
		t.Fatalf("no manager installed for ws7")
	}

	t.Log("BEFORE arrange:")
	dumpTree(t, ws)

	if err := mgr.ArrangeAll(ws); err != nil {
		t.Fatalf("ArrangeAll: %v", err)
	}

	// Re-fetch tree: sim.GetTree returns the same root, but layouts have
	// been mutated in place. The workspace node's descendants show the
	// post-arrange shape.
	tree, _ := s.GetTree()
	ws = tree.FindByID(16)

	t.Log("AFTER arrange:")
	dumpTree(t, ws)

	ms, ok := mgr.(*layout.MasterStack)
	if !ok {
		t.Fatalf("manager is %T, want *layout.MasterStack", mgr)
	}
	ids := ms.WindowIDs()
	t.Logf("tracked windowIDs=%v", ids)

	if len(ids) == 0 {
		t.Fatal("tracked windowIDs empty after arrange")
	}
	masterID := ids[0]
	masterNode := ws.FindByID(masterID)
	if masterNode == nil {
		t.Fatalf("master con_id=%d not found in arranged tree", masterID)
	}

	// Invariant 1: no outer singleton splith wrappers between workspace
	// and the master/stack split. The workspace should have at most one
	// direct child — the "master/stack split" container. If that
	// container is itself a singleton, the arrange left junk behind.
	if len(ws.Nodes) != 1 {
		t.Errorf("workspace has %d direct children, want 1 master/stack split", len(ws.Nodes))
	}
	outer := ws.Nodes[0]
	if outer.Type == "con" && len(outer.Nodes) == 1 {
		t.Errorf("outer container %d is a singleton wrapper (layout=%s)", outer.ID, outer.Layout)
	}

	// Invariant 2: master must be in a column of its own. Master's sibling
	// set should be {stack-column}, not {stack-col, other leaves, substack}.
	mp := masterNode.Parent
	if mp == nil {
		t.Fatalf("master parent is nil")
	}
	// The master's parent should contain exactly 2 children (master, stack).
	// If master is inside the stack column, its parent has > 2 children
	// or the siblings are other leaves/substacks — either way, wrong.
	if len(mp.Nodes) != 2 {
		t.Errorf("master %d's parent %d has %d siblings, want 2 (master + stack column)",
			masterID, mp.ID, len(mp.Nodes))
	}
}
