package fuzz

import (
	"fmt"
	"log/slog"
	"testing"

	"github.com/mschulkind-oss/tilekeeper/internal/config"
	"github.com/mschulkind-oss/tilekeeper/internal/harness/sim"
	"github.com/mschulkind-oss/tilekeeper/internal/sway"
	"github.com/mschulkind-oss/tilekeeper/internal/workspace"
)

// newTabbedPlusMasterHub manages "8" as tabbed and "9" as MasterStack.
func newTabbedPlusMasterHub(s *sim.SimSwayClient) *workspace.Hub {
	logger := slog.New(slog.NewTextHandler(&nopW{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	hub := workspace.NewHub(s, config.Config{
		General: config.GeneralConfig{DefaultLayout: "none", MasterWidth: 75, VisibleStackLimit: 3},
		Workspaces: map[string]config.WorkspaceConfig{
			"8": {DefaultLayout: "tabbed"},
			"9": {DefaultLayout: "MasterStack"},
		},
	}, logger)
	hub.Initialize()
	return hub
}

// nestedConCount returns the number of non-leaf "con" containers under ws.
// A correctly-flattened tabbed workspace has ZERO — every leaf is a direct
// tab child.
func nestedConCount(ws *sway.Node) int {
	n := 0
	var walk func(node *sway.Node)
	walk = func(node *sway.Node) {
		for _, c := range node.Nodes {
			if c.Type == "con" && len(c.Nodes) > 0 {
				n++
			}
			walk(c)
		}
	}
	walk(ws)
	return n
}

// TestTabbedFlatten_ContainerMoveIn pins the follow-up the container-move
// fuzz generator surfaced: a MasterStack subtree (splitv/stacked wrappers)
// relocated onto a tabbed workspace leaves its wrappers nested inside the
// tabs. Tabbed.ensure only ran `layout tabbed` and — worse — early-returned
// once the workspace was already tabbed, so the nesting persisted and
// fired the no-wrapper-chain invariant.
//
// After the fix every moved window is a direct tab child: no nested
// containers, no singleton wrapper chain.
func TestTabbedFlatten_ContainerMoveIn(t *testing.T) {
	s := sim.New()
	hub := newTabbedPlusMasterHub(s)

	state := newFuzzState([]string{"8", "9"})
	hub.HandleEvent(state.initWorkspace(s, "8"))
	hub.HandleEvent(state.initWorkspace(s, "9"))

	// ws8 starts as 3 flat tabs.
	for range 3 {
		hub.HandleEvent(one(state.genNew(s, state.workspaces["8"], 100))[0])
	}
	// ws9 is a 4-window MasterStack (master + stack with a substack wrapper).
	for range 4 {
		hub.HandleEvent(one(state.genNew(s, state.workspaces["9"], 100))[0])
	}

	ws8 := state.workspaces["8"]
	ws9 := state.workspaces["9"]
	movedIDs := make([]int64, 0)
	for _, l := range ws9.Leaves() {
		movedIDs = append(movedIDs, l.ID)
	}

	// Container move: ws9's whole subtree (with its wrappers) onto tabbed ws8.
	subtree := ws9.Nodes[0]
	ws9.Nodes = ws9.Nodes[1:]
	ws8.Nodes = append(ws8.Nodes, subtree)
	subtree.Parent = ws8
	for _, l := range subtree.Leaves() {
		l.Rect.Width, l.Rect.Height = 0, 0
	}
	rep := subtree.FindByID(movedIDs[0])
	clearAllFocus(state.root)
	rep.Focused = true
	hub.HandleEvent(sway.Event{Type: "window", Change: "move", Container: rep.Snapshot()})

	tree, _ := s.GetTree()
	ws8Node := findWorkspace(tree, "8")
	t.Log("ws8 after container move-in:")
	dumpTree(t, ws8Node)

	// Every leaf (3 original + 4 moved) must be a direct child of ws8.
	if got := len(ws8Node.Leaves()); got != 7 {
		t.Errorf("ws8 has %d leaves, want 7", got)
	}
	if n := nestedConCount(ws8Node); n != 0 {
		t.Errorf("ws8 has %d nested containers, want 0 (flat tabs)\n%s", n, dumpTreeStr(tree))
	}
	for _, l := range ws8Node.Leaves() {
		if l.Parent != ws8Node {
			t.Errorf("leaf %d is not a direct tab child (parent=%d)", l.ID, l.Parent.ID)
		}
	}
	if depth, path := longestSingletonChain(tree); depth > 1 {
		t.Errorf("singleton chain depth=%d path=%s\n%s", depth, path, dumpTreeStr(tree))
	}
	_ = fmt.Sprint
}
