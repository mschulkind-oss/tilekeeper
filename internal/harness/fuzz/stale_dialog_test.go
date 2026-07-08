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

// newDialogHub builds a MasterStack ws7 hub matching the live config.
func newDialogHub(s *sim.SimSwayClient) *workspace.Hub {
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
	return hub
}

// assertNoTrackedFloats fails if any tracked id sits in ws.FloatingNodes
// or is absent from the workspace's tiled leaves.
func assertNoTrackedFloats(t *testing.T, label string, ms *layout.MasterStack, ws *sway.Node) {
	t.Helper()
	tiled := map[int64]bool{}
	for _, l := range ws.Leaves() {
		if l != nil && l.Type == "con" && !l.IsFloating() {
			tiled[l.ID] = true
		}
	}
	floating := map[int64]bool{}
	for _, fn := range ws.FloatingNodes {
		floating[fn.ID] = true
	}
	for _, id := range ms.WindowIDs() {
		if floating[id] {
			t.Errorf("%s: tracked id=%d is FLOATING (silent floatingness bleed)", label, id)
		} else if !tiled[id] {
			t.Errorf("%s: tracked id=%d not among tiled leaves", label, id)
		}
	}
	for id := range tiled {
		found := false
		for _, tid := range ms.WindowIDs() {
			if tid == id {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: tiled leaf id=%d not tracked", label, id)
		}
	}
}

// TestStaleDialogNew_DoesNotBleedFloating replays the 2026-06-12 ctrl-s
// incident deterministically:
//
//	op=114  window::new con=78 (portal save dialog) — STALE payload says
//	        tiled, but sway has already floated the dialog. MasterStack
//	        admitted it and ran the master-insert dance; the dance's
//	        `swap 78<->77` transferred floatingness to the dialog's swap
//	        partner (sway swap exchanges positions INCLUDING floating
//	        membership and emits no events), and the follow-up
//	        `swap 77<->17` bled it onto tracked window 17.
//	op=115  the queued window::floating(78) — popWindow's substack promote
//	        then moved 18 onto a mark held by silently-floating 17,
//	        floating 18 too (container_add_sibling on floating dest).
//
// After the burst, NO tracked window may be floating and tracking must
// equal the tiled leaf set. The dialog itself must never be tracked.
func TestStaleDialogNew_DoesNotBleedFloating(t *testing.T) {
	s := sim.New()
	hub := newDialogHub(s)

	state := newFuzzState([]string{"7"})
	hub.HandleEvent(state.initWorkspace(s, "7"))

	// Steady state: 5 tiled windows → master + 2 visible stack + substack,
	// matching live ws7's shape class. (12 windows live; 5 is the minimum
	// with a substack under VisibleStackLimit=3.)
	for range 5 {
		hub.HandleEvent(one(state.genNew(s, state.workspaces["7"], 100))[0])
	}

	mgr := hub.Manager("7")
	ms := mgr.(*layout.MasterStack)
	tree, _ := s.GetTree()
	ws := findWorkspace(tree, "7")
	t.Log("STEADY:")
	dumpTree(t, ws)
	t.Logf("  tracked=%v", ms.WindowIDs())
	assertNoTrackedFloats(t, "steady", ms, ws)

	// The dialog: tiled at window::new emission, floated by sway before
	// the daemon processes the event. genDialogNew models exactly this —
	// the new event carries the stale tiled snapshot, the live node is
	// already in ws.FloatingNodes, and the floating event follows.
	burst := state.genDialogNew(s, state.workspaces["7"], 100)
	if len(burst) != 3 {
		t.Fatalf("genDialogNew returned %d events, want 3 (new + floating + focus)", len(burst))
	}
	dialogID := burst[0].Container.ID
	if burst[0].Container.IsFloating() {
		t.Fatalf("test setup: stale payload must claim tiled")
	}
	if live := state.windows[dialogID]; live == nil || !live.IsFloating() {
		t.Fatalf("test setup: live dialog node must already be floating")
	}

	for i, ev := range burst {
		hub.HandleEvent(ev)
		tree, _ = s.GetTree()
		ws = findWorkspace(tree, "7")
		label := fmt.Sprintf("after burst[%d] %s", i, describe(ev))
		t.Logf("%s tracked=%v", label, ms.WindowIDs())
		assertNoTrackedFloats(t, label, ms, ws)
		for _, id := range ms.WindowIDs() {
			if id == dialogID {
				t.Errorf("%s: floating dialog id=%d is tracked", label, dialogID)
			}
		}
	}

	t.Log("AFTER dialog burst:")
	dumpTree(t, ws)

	// The dialog closes (user saves or cancels). Layout must stay intact.
	if live := state.windows[dialogID]; live != nil {
		closeEv := sway.Event{Type: "window", Change: "close", Container: live.Snapshot()}
		s.CloseLeaf(live)
		delete(state.windows, dialogID)
		hub.HandleEvent(closeEv)
	}
	tree, _ = s.GetTree()
	ws = findWorkspace(tree, "7")
	assertNoTrackedFloats(t, "after dialog close", ms, ws)

	// Structure: master and stack must not share a parent (master-stack
	// split survives), per the master-stack-split invariant.
	ids := ms.WindowIDs()
	if len(ids) < 2 {
		t.Fatalf("tracked=%v, want >= 2", ids)
	}
	master := ws.FindByID(ids[0])
	if master == nil || master.Parent == nil {
		t.Fatalf("master missing")
	}
	for _, sid := range ids[1:] {
		n := ws.FindByID(sid)
		if n == nil || n.Parent == nil {
			t.Fatalf("stack id=%d missing", sid)
		}
		if n.Parent == master.Parent {
			t.Errorf("master %d and stack %d share parent %d — stack column destroyed\n%s",
				ids[0], sid, master.Parent.ID, dumpTreeStr(tree))
		}
	}
}

// TestStaleDialogNew_SecondDialogNoRecurrence replays the incident's
// op=124: a SECOND ctrl-s after the first dialog closed. Live, this
// re-broke 17/18 freshly via the identical mechanism. With the fresh-tree
// gate the dance never runs, so no recurrence.
func TestStaleDialogNew_SecondDialogNoRecurrence(t *testing.T) {
	s := sim.New()
	hub := newDialogHub(s)

	state := newFuzzState([]string{"7"})
	hub.HandleEvent(state.initWorkspace(s, "7"))
	for range 5 {
		hub.HandleEvent(one(state.genNew(s, state.workspaces["7"], 100))[0])
	}
	ms := hub.Manager("7").(*layout.MasterStack)

	for round := range 2 {
		burst := state.genDialogNew(s, state.workspaces["7"], 100)
		if len(burst) != 3 {
			t.Fatalf("round %d: genDialogNew returned %d events, want 3", round, len(burst))
		}
		dialogID := burst[0].Container.ID
		for _, ev := range burst {
			hub.HandleEvent(ev)
		}
		if live := state.windows[dialogID]; live != nil {
			closeEv := sway.Event{Type: "window", Change: "close", Container: live.Snapshot()}
			s.CloseLeaf(live)
			delete(state.windows, dialogID)
			hub.HandleEvent(closeEv)
		}
		tree, _ := s.GetTree()
		ws := findWorkspace(tree, "7")
		assertNoTrackedFloats(t, fmt.Sprintf("round %d", round), ms, ws)
	}
}

// TestFloatReturn_RearrangesLayout pins the L8 link: after a tracked
// window is re-tiled (user toggles floating off), the layout must be
// rebuilt — not skipped. Live, the "already tracked, skipping" path left
// re-tiled windows at arbitrary tree positions, so the user's manual
// rescue never fixed anything.
func TestFloatReturn_RearrangesLayout(t *testing.T) {
	s := sim.New()
	hub := newDialogHub(s)

	state := newFuzzState([]string{"7"})
	hub.HandleEvent(state.initWorkspace(s, "7"))
	for range 4 {
		hub.HandleEvent(one(state.genNew(s, state.workspaces["7"], 100))[0])
	}
	ms := hub.Manager("7").(*layout.MasterStack)
	ws := state.workspaces["7"]

	// Silently float the master via a swap with a floating window —
	// modeling the incident's bleed (no floating event fires).
	dialog := &sway.Node{ID: s.AllocID(), Type: "con", Name: "dialog",
		Floating: "user_on", Rect: sway.Rect{Width: 600, Height: 400}}
	ws.FloatingNodes = append(ws.FloatingNodes, dialog)
	dialog.Parent = ws
	state.windows[dialog.ID] = dialog
	masterID := ms.WindowIDs()[0]
	if err := s.RunCommand(fmt.Sprintf("[con_id=%d] swap container with con_id %d", dialog.ID, masterID)); err != nil {
		t.Fatalf("swap: %v", err)
	}
	live := state.windows[masterID]
	if live == nil {
		// master came from genNew; must be registered
		t.Fatalf("master %d not in state.windows", masterID)
	}
	if !live.IsFloating() {
		t.Fatalf("setup: master should be floating after swap with floating dialog")
	}

	// User re-tiles the master: sway emits window::floating with the
	// post-change (tiled) state.
	if err := s.RunCommand(fmt.Sprintf("[con_id=%d] floating disable", masterID)); err != nil {
		t.Fatalf("floating disable: %v", err)
	}
	hub.HandleEvent(sway.Event{Type: "window", Change: "floating", Container: live.Snapshot()})

	tree, _ := s.GetTree()
	wsNode := findWorkspace(tree, "7")
	assertNoTrackedFloats(t, "after re-tile", ms, wsNode)

	// The layout must be REBUILT: master/stack split intact, master width
	// honored (75% within tolerance, mirroring master-width-honored).
	ids := ms.WindowIDs()
	if len(ids) < 2 {
		t.Fatalf("tracked=%v, want >= 2", ids)
	}
	master := wsNode.FindByID(ids[0])
	if master == nil || master.Parent == nil {
		t.Fatalf("master %d missing after re-tile\n%s", ids[0], dumpTreeStr(tree))
	}
	for _, sid := range ids[1:] {
		n := wsNode.FindByID(sid)
		if n != nil && n.Parent == master.Parent {
			t.Errorf("master %d and stack %d share parent — no re-arrange happened\n%s",
				ids[0], sid, dumpTreeStr(tree))
		}
	}
	// Unconditional: the rebuild always issues `resize set width 75 ppt`,
	// so Percent==0 means the re-arrange never ran (the swap handed the
	// master the dialog's zero Percent and nothing restored it).
	if abs64(master.Percent-0.75) > 0.02 {
		t.Errorf("master width = %.3f, want 0.75±0.02 — re-arrange skipped", master.Percent)
	}
}
