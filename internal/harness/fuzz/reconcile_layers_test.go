package fuzz

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/mschulkind-oss/tilekeeper/internal/config"
	"github.com/mschulkind-oss/tilekeeper/internal/harness/sim"
	"github.com/mschulkind-oss/tilekeeper/internal/layout"
	"github.com/mschulkind-oss/tilekeeper/internal/sway"
	"github.com/mschulkind-oss/tilekeeper/internal/workspace"
)

// silentlyFloatTracked swaps a floating helper node with the tracked
// window `victim`, transferring floatingness with NO floating event —
// the sway swap-bleed. Returns the helper (now tiled, in victim's slot).
func silentlyFloatTracked(t *testing.T, s *sim.SimSwayClient, st *fuzzState, ws *sway.Node, victim int64) *sway.Node {
	t.Helper()
	helper := &sway.Node{ID: s.AllocID(), Type: "con", Name: "floating-helper",
		Floating: "user_on", Rect: sway.Rect{Width: 600, Height: 400}}
	ws.FloatingNodes = append(ws.FloatingNodes, helper)
	helper.Parent = ws
	st.windows[helper.ID] = helper
	if err := s.RunCommand(fmt.Sprintf("[con_id=%d] swap container with con_id %d", helper.ID, victim)); err != nil {
		t.Fatalf("swap: %v", err)
	}
	victimNode := st.windows[victim]
	if victimNode == nil || !victimNode.IsFloating() {
		t.Fatalf("setup: victim %d should be floating after swap", victim)
	}
	return helper
}

// TestReconcileWindows_DropsSilentlyFloated pins the MANAGER-level
// defense independently of the Hub's reconcileFloatingStale (which is
// bypassed here by calling the manager directly). Mutation-tested: with
// reconcileWindows' floating check reverted to FindByID-only, popWindow's
// substack promote marks the floated id (`mark --add move_target` on it)
// and the next WindowAdded swaps against it — the exact op=115 vector.
func TestReconcileWindows_DropsSilentlyFloated(t *testing.T) {
	s := sim.New()
	hub := newDialogHub(s)
	state := newFuzzState([]string{"7"})
	hub.HandleEvent(state.initWorkspace(s, "7"))
	for range 5 {
		hub.HandleEvent(one(state.genNew(s, state.workspaces["7"], 100))[0])
	}
	ms := hub.Manager("7").(*layout.MasterStack)
	ids := ms.WindowIDs()
	if len(ids) != 5 {
		t.Fatalf("tracked=%v, want 5", ids)
	}
	floatedID := ids[1] // top of visible stack
	silentlyFloatTracked(t, s, state, state.workspaces["7"], floatedID)

	// Record every sway command from here on.
	var cmds []string
	s.TraceSink = func(cmd string) { cmds = append(cmds, cmd) }

	// Direct manager calls (bypassing the Hub): close a neighbor, then
	// add a fresh window. Neither dance may touch the floated id.
	neighbor := ids[2]
	neighborLive := state.windows[neighbor]
	closeSnap := neighborLive.Snapshot()
	s.CloseLeaf(neighborLive)
	delete(state.windows, neighbor)
	freshTree, _ := s.GetTree()
	freshTree.SetParents()
	wsNode := findWorkspace(freshTree, "7")
	if err := ms.WindowRemoved(wsNode, closeSnap); err != nil {
		t.Fatalf("WindowRemoved: %v", err)
	}
	for _, id := range ms.WindowIDs() {
		if id == floatedID {
			t.Errorf("WindowRemoved kept silently-floated id=%d in tracking: %v", floatedID, ms.WindowIDs())
		}
	}

	newWin := one(state.genNew(s, state.workspaces["7"], 100))[0]
	freshTree, _ = s.GetTree()
	freshTree.SetParents()
	wsNode = findWorkspace(freshTree, "7")
	if err := ms.WindowAdded(wsNode, newWin.Container); err != nil {
		t.Fatalf("WindowAdded: %v", err)
	}

	needle1 := fmt.Sprintf("con_id=%d]", floatedID)
	needle2 := fmt.Sprintf("con_id %d", floatedID)
	for _, cmd := range cmds {
		if strings.Contains(cmd, needle1) || strings.Contains(cmd, needle2) {
			t.Errorf("command targets silently-floated window %d: %q", floatedID, cmd)
		}
	}
}

// warnCapture records Warn-level log messages.
type warnCapture struct {
	mu   sync.Mutex
	msgs []string
}

func (h *warnCapture) Enabled(_ context.Context, lvl slog.Level) bool {
	return lvl >= slog.LevelWarn
}

func (h *warnCapture) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value.Any())
		return true
	})
	h.mu.Lock()
	h.msgs = append(h.msgs, b.String())
	h.mu.Unlock()
	return nil
}

func (h *warnCapture) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *warnCapture) WithGroup(_ string) slog.Handler      { return h }

func (h *warnCapture) drain() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.msgs
	h.msgs = nil
	return out
}

func newWarnHub(s *sim.SimSwayClient) (*workspace.Hub, *warnCapture) {
	wc := &warnCapture{}
	hub := workspace.NewHub(s, config.Config{
		General: config.GeneralConfig{
			DefaultLayout:     "none",
			MasterWidth:       75,
			VisibleStackLimit: 3,
		},
		Workspaces: map[string]config.WorkspaceConfig{
			"7": {DefaultLayout: "MasterStack"},
		},
	}, slog.New(wc))
	hub.Initialize()
	return hub, wc
}

// TestHubReconcileFloatingStale_HealsOnFocus pins the HUB-level heal
// independently of MasterStack.reconcileWindows: a focus event takes no
// path through the manager's reconcile (WindowFocused only records
// lastFocusedID), so tracking can only heal via the Hub synthesizing the
// missed WindowRemoved. Deleting reconcileFloatingStale fails this test;
// deleting only the manager-level floating-drop does not.
func TestHubReconcileFloatingStale_HealsOnFocus(t *testing.T) {
	s := sim.New()
	hub, wc := newWarnHub(s)
	state := newFuzzState([]string{"7"})
	hub.HandleEvent(state.initWorkspace(s, "7"))
	for range 4 {
		hub.HandleEvent(one(state.genNew(s, state.workspaces["7"], 100))[0])
	}
	ms := hub.Manager("7").(*layout.MasterStack)
	ids := ms.WindowIDs()
	floatedID := ids[1]
	silentlyFloatTracked(t, s, state, state.workspaces["7"], floatedID)
	wc.drain()

	// An unrelated focus event on another tracked window. The Hub's
	// reconcile must synthesize the missed floating removal.
	target := state.windows[ids[0]]
	clearAllFocus(state.root)
	target.Focused = true
	hub.HandleEvent(sway.Event{Type: "window", Change: "focus", Container: target.Snapshot()})

	for _, id := range ms.WindowIDs() {
		if id == floatedID {
			t.Errorf("focus op did not heal silently-floated id=%d (tracked=%v)", floatedID, ms.WindowIDs())
		}
	}
	healed := false
	for _, msg := range wc.drain() {
		if strings.Contains(msg, "synthesizing missed floating event") &&
			strings.Contains(msg, fmt.Sprintf("con_id=%d", floatedID)) {
			healed = true
		}
	}
	if !healed {
		t.Errorf("no 'synthesizing missed floating event' WARN for id=%d — heal came from elsewhere or not at all", floatedID)
	}
}

// TestHealthySequenceEmitsNoDivergenceWarn pins checkDivergence's
// "silent in the healthy case" contract: routine new/close/focus ops must
// not WARN. Before the in-flight-container exemption, every window::new
// WARNed missed=[id] (sway attaches before emitting) and every close
// WARNed stale=[id] (sway destroys before emitting), drowning the
// forensic breadcrumb the check exists to provide.
func TestHealthySequenceEmitsNoDivergenceWarn(t *testing.T) {
	s := sim.New()
	hub, wc := newWarnHub(s)
	state := newFuzzState([]string{"7"})
	hub.HandleEvent(state.initWorkspace(s, "7"))

	for range 4 {
		hub.HandleEvent(one(state.genNew(s, state.workspaces["7"], 100))[0])
	}
	// Focus each window once.
	for _, id := range append([]int64(nil), hub.Manager("7").WindowIDs()...) {
		live := state.windows[id]
		clearAllFocus(state.root)
		live.Focused = true
		hub.HandleEvent(sway.Event{Type: "window", Change: "focus", Container: live.Snapshot()})
	}
	// Close one (sway order: destroy, then event).
	ids := hub.Manager("7").WindowIDs()
	victim := state.windows[ids[len(ids)-1]]
	closeSnap := victim.Snapshot()
	s.CloseLeaf(victim)
	delete(state.windows, victim.ID)
	hub.HandleEvent(sway.Event{Type: "window", Change: "close", Container: closeSnap})

	for _, msg := range wc.drain() {
		if strings.Contains(msg, "tracking diverged") {
			t.Errorf("healthy sequence produced divergence WARN: %s", msg)
		}
	}
}

// TestStaleDialogNew_SmallWorkspaces covers the 0- and 1-window
// pre-states: the dialog as the first window on an empty workspace, and
// the pushWindow 2-window branch (which issues `splith` on the existing
// window) with a dialog second.
func TestStaleDialogNew_SmallWorkspaces(t *testing.T) {
	for _, preWindows := range []int{0, 1} {
		t.Run(fmt.Sprintf("pre=%d", preWindows), func(t *testing.T) {
			s := sim.New()
			hub := newDialogHub(s)
			state := newFuzzState([]string{"7"})
			hub.HandleEvent(state.initWorkspace(s, "7"))
			for range preWindows {
				hub.HandleEvent(one(state.genNew(s, state.workspaces["7"], 100))[0])
			}
			ms := hub.Manager("7").(*layout.MasterStack)

			burst := state.genDialogNew(s, state.workspaces["7"], 100)
			if len(burst) != 3 {
				t.Fatalf("genDialogNew returned %d events, want 3", len(burst))
			}
			dialogID := burst[0].Container.ID
			for _, ev := range burst {
				hub.HandleEvent(ev)
			}
			tree, _ := s.GetTree()
			ws := findWorkspace(tree, "7")
			assertNoTrackedFloats(t, "after burst", ms, ws)
			for _, id := range ms.WindowIDs() {
				if id == dialogID {
					t.Errorf("floating dialog id=%d tracked (pre=%d)", dialogID, preWindows)
				}
			}
			if got := len(ms.WindowIDs()); got != preWindows {
				t.Errorf("tracked count = %d, want %d", got, preWindows)
			}
		})
	}
}
