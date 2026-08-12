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

// nestedConCount returns the number of non-leaf "con" containers under n.
// A correctly-flattened tab strip has ZERO below it — every window is a
// direct tab.
func nestedConCount(n *sway.Node) int {
	count := 0
	var walk func(node *sway.Node)
	walk = func(node *sway.Node) {
		for _, c := range node.Nodes {
			if c.Type == "con" && len(c.Nodes) > 0 {
				count++
			}
			walk(c)
		}
	}
	walk(n)
	return count
}

// TestTabbedFlatten_ContainerMoveIn pins the follow-up the container-move
// fuzz generator surfaced: a MasterStack subtree (splitv/stacked wrappers)
// relocated onto a tabbed workspace leaves its wrappers nested inside the
// tabs. Tabbed.ensure only ran `layout tabbed` and — worse — early-returned
// once the workspace was already tabbed, so the nesting persisted and
// fired the no-wrapper-chain invariant.
//
// After the fix every moved window is a direct tab of the STRIP: one tabbed
// container, sitting alone under the workspace, with no nesting below it.
// (The assertions used to demand the windows be direct children of the
// WORKSPACE. That shape was an artifact of the sim resolving
// `[workspace=N]` to the workspace node — real sway wraps the windows in a
// container instead, so the old expectation could never hold in production.
// See docs/sway-model-verification.md §13.)
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

	// Every leaf (3 original + 4 moved) must be a direct tab of the strip.
	if got := len(ws8Node.Leaves()); got != 7 {
		t.Errorf("ws8 has %d leaves, want 7", got)
	}
	strip := tabStripOf(t, s, "8")
	if n := nestedConCount(strip); n != 0 {
		t.Errorf("strip has %d nested containers, want 0 (flat tabs)\n%s", n, dumpTreeStr(tree))
	}
	if got := len(strip.Nodes); got != 7 {
		t.Errorf("strip has %d tabs, want 7\n%s", got, dumpTreeStr(tree))
	}
	if depth, path := longestSingletonChain(tree); depth > 1 {
		t.Errorf("singleton chain depth=%d path=%s\n%s", depth, path, dumpTreeStr(tree))
	}
	_ = fmt.Sprint
}
