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

// TestArrangeAllSkipsFullscreenWorkspace reproduces the live ws7 bug: when
// `just deploy` restarts tilekeeper while a window on ws7 is fullscreen,
// arrangeExisting→ArrangeAll rebuilds the tiling layout using the
// non-fullscreen siblings and leaves the fullscreen leaf stranded. The result
// on live ws7 was a 4-deep chain of splith/splitv wrappers —
// H[H[Chromium H[Chromium V[Chromium Chromium S[7×Chromium]]]]].
//
// The intended behavior: ArrangeAll should be a no-op (or minimally invasive)
// when the workspace has a fullscreen leaf — the user is "in" that window and
// reshuffling siblings is both pointless (they aren't visible) and dangerous
// (it creates the bad layout the user sees the moment they exit fullscreen).
func TestArrangeAllSkipsFullscreenWorkspace(t *testing.T) {
	s := sim.New()
	logger := slog.New(slog.NewTextHandler(&nopW{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))

	hub := workspace.NewHub(s, config.Config{
		General:    config.GeneralConfig{DefaultLayout: "none", MasterWidth: 75},
		Workspaces: map[string]config.WorkspaceConfig{"7": {DefaultLayout: "MasterStack"}},
	}, logger)
	hub.Initialize()

	state := newFuzzState([]string{"7"})
	hub.HandleEvent(state.initWorkspace(s, "7"))

	// Grow ws7 to 5 windows under MasterStack — master plus 4 stack entries.
	for range 5 {
		hub.HandleEvent(state.genNew(s, state.workspaces["7"], 100))
	}

	// Capture the healthy layout shape.
	tree, _ := s.GetTree()
	ws := findWorkspace(tree, "7")
	if ws == nil {
		t.Fatalf("ws7 not found in tree after grow")
	}
	healthyDepth, healthyPath := longestSingletonChain(tree)
	t.Logf("healthy layout depth=%d path=%s", healthyDepth, healthyPath)

	// Fullscreen the master. The master is tracked as windowIDs[0].
	mgr := hub.Manager("7")
	ms, ok := mgr.(*layout.MasterStack)
	if !ok {
		t.Fatalf("manager is %T, want *layout.MasterStack", mgr)
	}
	masterID := ms.WindowIDs()[0]
	masterNode := ws.FindByID(masterID)
	if masterNode == nil {
		t.Fatalf("master %d not in ws7", masterID)
	}
	masterNode.FullscreenMode = 1
	hub.HandleEvent(sway.Event{Type: "window", Change: "fullscreen_mode", Container: masterNode})

	beforeWrappers := countStructuralCons(ws)
	t.Log("BEFORE arrange (fullscreen master):")
	dumpTree(t, ws)

	// Simulate daemon restart: `just deploy` → daemon.arrangeExisting →
	// ArrangeAll on every workspace. On ws7 this triggers the bug.
	if err := mgr.ArrangeAll(ws); err != nil {
		t.Fatalf("ArrangeAll: %v", err)
	}

	tree, _ = s.GetTree()
	ws = findWorkspace(tree, "7")
	afterWrappers := countStructuralCons(ws)

	t.Log("AFTER arrange (post-restart simulation):")
	dumpTree(t, ws)

	// Primary invariant: arrangeAll on a workspace with a fullscreen leaf
	// must not grow the structural wrapper count. Rebuilding the tiling
	// layout while a window is fullscreen either strands the fullscreen
	// leaf as a sibling of the rebuilt splith (live ws7 bug) or wraps the
	// remaining siblings in a new splith while the fullscreen leaf keeps
	// its original parent. Both create a tree the user sees the moment
	// they exit fullscreen and does not resemble either layout.
	if afterWrappers > beforeWrappers {
		t.Errorf("arrangeAll grew wrappers on fullscreen workspace: before=%d after=%d",
			beforeWrappers, afterWrappers)
	}

	// Secondary invariant: the fullscreen leaf is still in the workspace
	// and still fullscreen. Losing its fullscreen state would flicker the
	// user's view on restart.
	fsLeaf := ws.FindByID(masterID)
	if fsLeaf == nil {
		t.Fatalf("fullscreen leaf %d missing from ws7 after arrange", masterID)
	}
	if fsLeaf.FullscreenMode != 1 {
		t.Errorf("fullscreen leaf %d lost FullscreenMode (got %d, want 1)",
			masterID, fsLeaf.FullscreenMode)
	}
}

func countStructuralCons(ws *sway.Node) int {
	count := 0
	var walk func(n *sway.Node)
	walk = func(n *sway.Node) {
		if n.Type == "con" && len(n.Nodes) > 0 {
			count++
		}
		for _, c := range n.Nodes {
			walk(c)
		}
	}
	for _, child := range ws.Nodes {
		walk(child)
	}
	return count
}

func findWorkspace(root *sway.Node, name string) *sway.Node {
	for _, ws := range root.Workspaces() {
		if ws.Name == name {
			return ws
		}
	}
	return nil
}
