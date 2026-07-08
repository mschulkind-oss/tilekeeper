package fuzz

import (
	"fmt"
	"log/slog"
	"testing"

	"github.com/mschulkind-oss/tilekeeper/internal/config"
	"github.com/mschulkind-oss/tilekeeper/internal/harness/sim"
	"github.com/mschulkind-oss/tilekeeper/internal/layout"
	"github.com/mschulkind-oss/tilekeeper/internal/sway"
	"github.com/mschulkind-oss/tilekeeper/internal/workspace"
)

// newTwoWsHub builds a hub managing both "9" and "7" as MasterStack,
// matching the live config that produced the 2026-06-13 ws7 desync.
func newTwoWsHub(s *sim.SimSwayClient) *workspace.Hub {
	logger := slog.New(slog.NewTextHandler(&nopW{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	hub := workspace.NewHub(s, config.Config{
		General: config.GeneralConfig{DefaultLayout: "none", MasterWidth: 75, VisibleStackLimit: 3},
		Workspaces: map[string]config.WorkspaceConfig{
			"9": {DefaultLayout: "MasterStack"},
			"7": {DefaultLayout: "MasterStack"},
		},
	}, logger)
	hub.Initialize()
	return hub
}

// TestContainerMove_AdoptsAllFellowTravelers reproduces the 2026-06-13
// ws7 desync: holding the move-to-workspace key relocated a multi-window
// MasterStack subtree from ws9 to ws7, but sway emitted a SINGLE
// window::move event (for one representative leaf), not one per window.
// The other windows rode along in the tree with no event of their own.
//
// Before the fix, handleWindowMove adopts only the event's con on the
// destination, leaving the fellow-travelers physically on ws7 but
// untracked forever (logged as missed=[...] every op, never acted on).
//
// Invariants after the move:
//   - every relocated leaf is tracked by the DESTINATION manager.
//   - none are tracked by the SOURCE manager.
//   - destination tracking == its tiled leaves (no missed/stale).
func TestContainerMove_AdoptsAllFellowTravelers(t *testing.T) {
	s := sim.New()
	hub := newTwoWsHub(s)

	state := newFuzzState([]string{"9", "7"})
	hub.HandleEvent(state.initWorkspace(s, "9"))
	hub.HandleEvent(state.initWorkspace(s, "7"))

	// Populate ws9 with 6 windows (master + 2 visible stack + substack).
	for range 6 {
		hub.HandleEvent(one(state.genNew(s, state.workspaces["9"], 100))[0])
	}
	ms9 := hub.Manager("9").(*layout.MasterStack)
	ms7 := hub.Manager("7").(*layout.MasterStack)
	ws9 := state.workspaces["9"]
	ws7 := state.workspaces["7"]

	movedIDs := make([]int64, 0, 6)
	for _, l := range ws9.Leaves() {
		movedIDs = append(movedIDs, l.ID)
	}
	if len(movedIDs) != 6 {
		t.Fatalf("setup: ws9 has %d leaves, want 6", len(movedIDs))
	}
	t.Logf("ws9 tracked before move: %v", ms9.WindowIDs())

	// Container move: relocate ws9's entire tiled subtree (the master/stack
	// wrapper) to ws7 as one tree operation, the way sway moves a focused
	// container with children. Sway zeroes pending geometry and emits a
	// single window::move for the representative leaf.
	subtree := ws9.Nodes[0]
	ws9.Nodes = ws9.Nodes[1:]
	ws7.Nodes = append(ws7.Nodes, subtree)
	subtree.Parent = ws7
	for _, l := range subtree.Leaves() {
		l.Rect.Width, l.Rect.Height = 0, 0
	}
	rep := subtree.FindByID(movedIDs[0]) // representative leaf, now under ws7
	clearAllFocus(state.root)
	rep.Focused = true

	hub.HandleEvent(sway.Event{Type: "window", Change: "move", Container: rep.Snapshot()})

	tree, _ := s.GetTree()
	ws7Node := findWorkspace(tree, "7")
	ws9Node := findWorkspace(tree, "9")
	t.Log("AFTER container move:")
	dumpTree(t, ws7Node)
	t.Logf("ws7 tracked: %v", ms7.WindowIDs())
	t.Logf("ws9 tracked: %v", ms9.WindowIDs())

	tracked7 := map[int64]bool{}
	for _, id := range ms7.WindowIDs() {
		tracked7[id] = true
	}
	tracked9 := map[int64]bool{}
	for _, id := range ms9.WindowIDs() {
		tracked9[id] = true
	}
	for _, id := range movedIDs {
		if !tracked7[id] {
			t.Errorf("moved window %d NOT adopted by destination ws7 (tracked=%v)", id, ms7.WindowIDs())
		}
		if tracked9[id] {
			t.Errorf("moved window %d still tracked by source ws9 (tracked=%v)", id, ms9.WindowIDs())
		}
	}

	// Destination tracking must equal its tiled leaves.
	assertNoTrackedFloats(t, "after container move", ms7, ws7Node)
	if depth, path := longestSingletonChain(tree); depth > 1 {
		t.Errorf("singleton wrapper chain depth=%d path=%s after move-in\n%s", depth, path, dumpTreeStr(tree))
	}
	_ = ws9Node
}

// TestContainerMove_IntoPopulatedDestination is the harder variant: ws7
// already holds a couple tracked windows when the ws9 subtree arrives.
// The adopted fellow-travelers must integrate with the existing master/
// stack, not clobber it.
func TestContainerMove_IntoPopulatedDestination(t *testing.T) {
	s := sim.New()
	hub := newTwoWsHub(s)

	state := newFuzzState([]string{"9", "7"})
	hub.HandleEvent(state.initWorkspace(s, "9"))
	hub.HandleEvent(state.initWorkspace(s, "7"))

	for range 2 {
		hub.HandleEvent(one(state.genNew(s, state.workspaces["7"], 100))[0])
	}
	for range 4 {
		hub.HandleEvent(one(state.genNew(s, state.workspaces["9"], 100))[0])
	}
	ms7 := hub.Manager("7").(*layout.MasterStack)
	ws9 := state.workspaces["9"]
	ws7 := state.workspaces["7"]

	pre7 := append([]int64(nil), ms7.WindowIDs()...)
	movedIDs := make([]int64, 0)
	for _, l := range ws9.Leaves() {
		movedIDs = append(movedIDs, l.ID)
	}

	subtree := ws9.Nodes[0]
	ws9.Nodes = ws9.Nodes[1:]
	ws7.Nodes = append(ws7.Nodes, subtree)
	subtree.Parent = ws7
	for _, l := range subtree.Leaves() {
		l.Rect.Width, l.Rect.Height = 0, 0
	}
	rep := subtree.FindByID(movedIDs[0])
	clearAllFocus(state.root)
	rep.Focused = true
	hub.HandleEvent(sway.Event{Type: "window", Change: "move", Container: rep.Snapshot()})

	tree, _ := s.GetTree()
	ws7Node := findWorkspace(tree, "7")
	t.Logf("ws7 tracked after: %v (pre=%v, moved=%v)", ms7.WindowIDs(), pre7, movedIDs)

	tracked7 := map[int64]bool{}
	for _, id := range ms7.WindowIDs() {
		tracked7[id] = true
	}
	want := append(append([]int64(nil), pre7...), movedIDs...)
	for _, id := range want {
		if !tracked7[id] {
			t.Errorf("window %d missing from ws7 tracking (tracked=%v)", id, ms7.WindowIDs())
		}
	}
	if len(ms7.WindowIDs()) != len(want) {
		t.Errorf("ws7 tracks %d windows, want %d", len(ms7.WindowIDs()), len(want))
	}
	assertNoTrackedFloats(t, "populated dest", ms7, ws7Node)
	master := ws7Node.FindByID(ms7.WindowIDs()[0])
	if master != nil && master.Parent != nil {
		for _, sid := range ms7.WindowIDs()[1:] {
			n := ws7Node.FindByID(sid)
			if n != nil && n.Parent == master.Parent {
				t.Errorf("master %d and stack %d share parent — split missing\n%s",
					ms7.WindowIDs()[0], sid, dumpTreeStr(tree))
				break
			}
		}
	}
	_ = fmt.Sprint
}

// TestContainerMove_RepeatedMasterStackNoChain stresses the fix's
// ArrangeAll-on-arrival path: bounce a multi-window subtree between two
// MasterStack workspaces many times and assert tracking stays exact and
// no singleton wrapper chain ever forms. This isolates the fix from the
// separate cross-layout (MasterStack->Tabbed) wrapper-chain issue the
// fuzz generator also surfaces.
func TestContainerMove_RepeatedMasterStackNoChain(t *testing.T) {
	s := sim.New()
	hub := newTwoWsHub(s)
	state := newFuzzState([]string{"9", "7"})
	hub.HandleEvent(state.initWorkspace(s, "9"))
	hub.HandleEvent(state.initWorkspace(s, "7"))
	for range 6 {
		hub.HandleEvent(one(state.genNew(s, state.workspaces["9"], 100))[0])
	}

	src, dst := "9", "7"
	for iter := 0; iter < 8; iter++ {
		srcWS := state.workspaces[src]
		dstWS := state.workspaces[dst]
		if len(srcWS.Nodes) == 0 {
			t.Fatalf("iter %d: source %s empty", iter, src)
		}
		moved := make([]int64, 0)
		for _, l := range srcWS.Leaves() {
			moved = append(moved, l.ID)
		}
		subtree := srcWS.Nodes[0]
		srcWS.Nodes = srcWS.Nodes[1:]
		dstWS.Nodes = append(dstWS.Nodes, subtree)
		subtree.Parent = dstWS
		for _, l := range subtree.Leaves() {
			l.Rect.Width, l.Rect.Height = 0, 0
		}
		rep := subtree.FindByID(moved[0])
		clearAllFocus(state.root)
		rep.Focused = true
		hub.HandleEvent(sway.Event{Type: "window", Change: "move", Container: rep.Snapshot()})

		tree, _ := s.GetTree()
		dstNode := findWorkspace(tree, dst)
		dms := hub.Manager(dst).(*layout.MasterStack)
		if len(dms.WindowIDs()) != len(moved) {
			t.Errorf("iter %d: dest %s tracks %v, want %d windows", iter, dst, dms.WindowIDs(), len(moved))
		}
		assertNoTrackedFloats(t, fmt.Sprintf("iter %d dest %s", iter, dst), dms, dstNode)
		if depth, path := longestSingletonChain(tree); depth > 1 {
			t.Errorf("iter %d: singleton chain depth=%d path=%s\n%s", iter, depth, path, dumpTreeStr(tree))
		}
		src, dst = dst, src
	}
}
