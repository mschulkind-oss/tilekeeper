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

// TestPopupFloatClose_RestoresMasterStackSplit pins the 2026-05-31 live
// ws7 bug: a 1Password popup spawned on a 12-window MasterStack workspace,
// was treated as a `new` tiled window (pushWindow swapped it into the
// master slot), then sway re-classified it as floating ~16ms later, then
// it closed. The popWindow path on master removal only moved the new
// master back out of the stack column and (substack rebalance) moved one
// substack member up, but never put the displaced visible-stack siblings
// BACK into the stack column. End state: master + 2 visible-stack leaves
// shared the workspace's outer splith wrapper, with the substack as a
// 4th sibling — three "stripes" of leaves above the substack instead of
// "1 master, 1 stack column".
//
// The minimal repro: 5 windows (1 master + 2 visible-stack + 2 substack;
// VisibleStackLimit=3 actually keeps 2 in the visible stack on this code),
// then one popup goes new → floating → close.
//
// Invariants checked after the sequence:
//   - master-stack-split: master's parent ≠ any stack window's parent.
//   - tracked-matches-leaves: manager.WindowIDs() == ws's tiled leaves.
//   - no orphan singleton splith wrapper above the master/stack split.
func TestPopupFloatClose_RestoresMasterStackSplit(t *testing.T) {
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

	// Steady state: 5 tiled windows. With VisibleStackLimit=3 the substack
	// kicks in once len(windowIDs) > VisibleStackLimit+MasterCount=4, so 5
	// is the minimum that gives us a substack — the live ws7 shape.
	const steady = 5
	for range steady {
		hub.HandleEvent(state.genNew(s, state.workspaces["7"], 100))
	}

	mgr := hub.Manager("7")
	ms, ok := mgr.(*layout.MasterStack)
	if !ok {
		t.Fatalf("manager is %T, want *layout.MasterStack", mgr)
	}

	tree, _ := s.GetTree()
	ws := findWorkspace(tree, "7")
	t.Log("STEADY (before popup):")
	dumpTree(t, ws)
	t.Logf("  tracked=%v", ms.WindowIDs())

	// 1. Spawn the popup as a new tiled window. genNew matches sway's
	//    spawn-as-sibling-of-focused behavior and fires the `new` event.
	popupEv := state.genNew(s, state.workspaces["7"], 100)
	if popupEv.Container == nil {
		t.Fatal("genNew returned empty event — workspace too full?")
	}
	popupID := popupEv.Container.ID
	hub.HandleEvent(popupEv)

	// 2. Sway re-classifies it as floating. genFloating detaches from the
	//    tiled tree, moves it to FloatingNodes, sets Floating="user_on",
	//    and emits the `floating` event.
	floatEv := state.genFloating(state.workspaces["7"])
	if floatEv.Container == nil || floatEv.Container.ID != popupID {
		t.Fatalf("expected genFloating to target popup id=%d, got container=%v", popupID, floatEv.Container)
	}
	hub.HandleEvent(floatEv)

	// 3. The popup closes. Match the production fuzz driver order: detach
	//    then dispatch (real sway destroys the container before the event
	//    arrives, so subscribers see a parent-less container).
	closeEv := sway.Event{Type: "window", Change: "close", Container: floatEv.Container}
	s.CloseLeaf(closeEv.Container)
	delete(state.windows, popupID)
	hub.HandleEvent(closeEv)

	tree, _ = s.GetTree()
	ws = findWorkspace(tree, "7")
	t.Log("AFTER popup new→floating→close:")
	dumpTree(t, ws)
	t.Logf("  tracked=%v", ms.WindowIDs())

	ids := ms.WindowIDs()
	if len(ids) != steady {
		t.Errorf("tracked count = %d, want %d (popup should be gone)", len(ids), steady)
	}

	// Invariant 1: master and stack windows must not share a direct parent.
	// If they do, the stack-column wrapper has been destroyed and the
	// outer splith holds them all as leaves — the live ws7 "3 stripes" shape.
	master := ws.FindByID(ids[0])
	if master == nil || master.Parent == nil {
		t.Fatalf("master id=%d missing or has no parent\n%s", ids[0], dumpTreeStr(tree))
	}
	masterParent := master.Parent
	for _, sid := range ids[1:] {
		stackNode := ws.FindByID(sid)
		if stackNode == nil || stackNode.Parent == nil {
			t.Fatalf("stack id=%d missing or has no parent\n%s", sid, dumpTreeStr(tree))
		}
		if stackNode.Parent == masterParent {
			t.Errorf("BUG: master id=%d and stack id=%d share parent id=%d (layout=%s); tracked=%v\n%s",
				ids[0], sid, masterParent.ID, masterParent.Layout, ids, dumpTreeStr(tree))
		}
	}

	// Invariant 2: tracked windowIDs matches workspace leaves. Matches
	// the production `tracked-matches-leaves` invariant: missed checks
	// against non-excluded leaves (manager forgot a window it owns);
	// stale checks against ALL leaves (manager kept an id whose container
	// is gone — closed/relocated).
	wantTracked := map[int64]bool{}
	for _, l := range ws.Leaves() {
		if layout.IsExcluded(l) {
			continue
		}
		wantTracked[l.ID] = true
	}
	allLeaves := map[int64]bool{}
	for _, l := range ws.Leaves() {
		if l == nil || l.Type != "con" {
			continue
		}
		allLeaves[l.ID] = true
	}
	got := map[int64]bool{}
	for _, id := range ids {
		got[id] = true
	}
	for id := range wantTracked {
		if !got[id] {
			t.Errorf("tracking missed leaf id=%d (tracked=%v)", id, ids)
		}
	}
	for id := range got {
		if !allLeaves[id] {
			t.Errorf("tracking has stale id=%d (leaves=%v)", id, leafIDs(ws))
		}
	}

	// Invariant 3: no orphan singleton wrapper sitting between the
	// workspace and the master/stack split. ws.Nodes should have exactly
	// one direct child (the outer splith holding master + stack column).
	if len(ws.Nodes) != 1 {
		t.Errorf("ws has %d direct children, want 1\n%s", len(ws.Nodes), dumpTreeStr(tree))
	} else {
		outer := ws.Nodes[0]
		if outer.Type == "con" && len(outer.Nodes) == 1 {
			t.Errorf("orphan singleton outer wrapper id=%d layout=%s\n%s",
				outer.ID, outer.Layout, dumpTreeStr(tree))
		}
	}

	// Invariant 4: no singleton wrapper chain longer than 1 (the
	// global fuzz invariant). One wrapper is OK (stack-column wraps a
	// single visible-stack window); two stacked is a regression.
	if depth, path := longestSingletonChain(tree); depth > 1 {
		t.Errorf("singleton chain depth=%d path=%s\n%s", depth, path, dumpTreeStr(tree))
	}
}
