package layout

import (
	"fmt"
	"slices"
	"testing"

	"github.com/mschulkind-oss/tilekeeper/internal/sway"
)

func newTestMasterStack(cfg ...MasterStackConfig) (*MasterStack, *sway.Mock) {
	mock := sway.NewMock()
	c := DefaultMasterStackConfig()
	if len(cfg) > 0 {
		c = cfg[0]
	}
	return NewMasterStackManager(mock, c), mock
}

func TestMasterStackName(t *testing.T) {
	ms, _ := newTestMasterStack()
	if ms.Name() != "MasterStack" {
		t.Errorf("Name() = %q, want %q", ms.Name(), "MasterStack")
	}
}

func TestMasterStackWindowIDs(t *testing.T) {
	ms, _ := newTestMasterStack()
	if ids := ms.WindowIDs(); len(ids) != 0 {
		t.Errorf("initial WindowIDs() = %v, want empty", ids)
	}
}

// --- ArrangeAll tests ---

func TestArrangeAllEmpty(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 0)
	mock.Tree = ws

	if err := ms.ArrangeAll(ws); err != nil {
		t.Fatalf("ArrangeAll: %v", err)
	}
	if len(ms.WindowIDs()) != 0 {
		t.Errorf("windowIDs = %v, want empty", ms.WindowIDs())
	}
}

func TestArrangeAllSingleWindow(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 1)
	mock.Tree = ws

	if err := ms.ArrangeAll(ws); err != nil {
		t.Fatalf("ArrangeAll: %v", err)
	}
	if len(ms.WindowIDs()) != 1 {
		t.Errorf("windowIDs len = %d, want 1", len(ms.WindowIDs()))
	}
}

func TestArrangeAllTwoWindows(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 2)
	mock.Tree = ws

	if err := ms.ArrangeAll(ws); err != nil {
		t.Fatalf("ArrangeAll: %v", err)
	}

	ids := ms.WindowIDs()
	if len(ids) != 2 {
		t.Fatalf("windowIDs len = %d, want 2", len(ids))
	}

	// Should have issued splith, splitv, and resize commands
	if !mock.HasCommand("[con_id=100] splith") {
		t.Error("missing splith command for master")
	}
	if mock.CommandCount() < 3 {
		t.Errorf("expected at least 3 commands, got %d", mock.CommandCount())
	}
}

func TestArrangeAllThreeWindows(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	mock.Tree = ws

	if err := ms.ArrangeAll(ws); err != nil {
		t.Fatalf("ArrangeAll: %v", err)
	}

	ids := ms.WindowIDs()
	if len(ids) != 3 {
		t.Fatalf("windowIDs len = %d, want 3", len(ids))
	}

	// Master width should be set
	resizeCmds := mock.CommandsMatching("resize set width")
	if len(resizeCmds) == 0 {
		t.Error("no master width resize command issued")
	}
}

func TestArrangeAllFocusedBecomesMaster(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	// Set the last window as focused
	lastWindow := ws.Nodes[2]
	lastWindow.Focused = true
	mock.Tree = ws

	if err := ms.ArrangeAll(ws); err != nil {
		t.Fatalf("ArrangeAll: %v", err)
	}

	ids := ms.WindowIDs()
	if ids[0] != lastWindow.ID {
		t.Errorf("master = %d, want focused window %d", ids[0], lastWindow.ID)
	}
}

// --- WindowAdded tests ---

func TestWindowAddedFirst(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 0)
	mock.Tree = ws

	// Sway attaches the view to the tree BEFORE emitting window::new, so
	// the workspace always contains the container by the time WindowAdded
	// runs — WindowAdded verifies that (fresh-tree gate) and skips
	// otherwise.
	window := sway.CreateWindow("test")
	ws.Nodes = append(ws.Nodes, window)
	window.Parent = ws
	if err := ms.WindowAdded(ws, window); err != nil {
		t.Fatalf("WindowAdded: %v", err)
	}

	ids := ms.WindowIDs()
	if len(ids) != 1 || ids[0] != window.ID {
		t.Errorf("windowIDs = %v, want [%d]", ids, window.ID)
	}
}

// TestWindowAddedSkipsWhenGoneFromTree pins the fresh-tree gate: a
// window::new whose container closed (or floated away to another
// workspace) before the daemon processed the event must not be admitted.
// The event payload is a stale snapshot; the workspace tree is the truth.
func TestWindowAddedSkipsWhenGoneFromTree(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 0)
	mock.Tree = ws

	ghost := sway.CreateWindow("closed-before-processing")
	if err := ms.WindowAdded(ws, ghost); err != nil {
		t.Fatalf("WindowAdded: %v", err)
	}
	if ids := ms.WindowIDs(); len(ids) != 0 {
		t.Errorf("windowIDs = %v, want empty (ghost window admitted)", ids)
	}
}

// TestWindowAddedSkipsStaleTiledPayloadForFloatingWindow pins the
// 2026-06-12 ctrl-s fix at the unit level: the event payload claims
// tiled, but the fresh tree shows the window floating (sway floated it
// between emission and processing). The floating state in the TREE wins;
// the window is not admitted.
func TestWindowAddedSkipsStaleTiledPayloadForFloatingWindow(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 2)
	mock.Tree = ws

	dialog := sway.CreateWindow("portal-save-dialog")
	stalePayload := dialog.Snapshot() // tiled at emission time
	dialog.Floating = "auto_on"       // sway floats it before we process
	ws.FloatingNodes = append(ws.FloatingNodes, dialog)
	dialog.Parent = ws

	if err := ms.WindowAdded(ws, stalePayload); err != nil {
		t.Fatalf("WindowAdded: %v", err)
	}
	for _, id := range ms.WindowIDs() {
		if id == dialog.ID {
			t.Errorf("floating dialog %d admitted into tracking via stale tiled payload", dialog.ID)
		}
	}
}

// TestWindowAddedSkipsStaleWindowedPayloadForFullscreenWindow is the
// fullscreen twin of the floating gate test: the payload claims a normal
// windowed state but the live tree shows FullscreenMode=1 (the window
// fullscreened itself between emission and processing). The gate must
// read fullscreen from the LIVE node — and this variant has no corrective
// event at all, since the daemon filters window::fullscreen_mode.
func TestWindowAddedSkipsStaleWindowedPayloadForFullscreenWindow(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 2)
	mock.Tree = ws

	win := sway.CreateWindow("goes-fullscreen-immediately")
	ws.Nodes = append(ws.Nodes, win)
	win.Parent = ws
	stalePayload := win.Snapshot() // windowed at emission time
	win.FullscreenMode = 1         // fullscreens before we process

	if err := ms.WindowAdded(ws, stalePayload); err != nil {
		t.Fatalf("WindowAdded: %v", err)
	}
	for _, id := range ms.WindowIDs() {
		if id == win.ID {
			t.Errorf("fullscreen window %d admitted into tracking via stale windowed payload", win.ID)
		}
	}
}

func TestWindowAddedSecond(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 1)
	mock.Tree = ws

	// Add first window
	first := ws.Nodes[0]
	ms.WindowAdded(ws, first)

	// Add second window — it becomes master (positionAtIndex=0)
	second := sway.CreateWindow("second")
	ws.Nodes = append(ws.Nodes, second)
	second.Parent = ws
	if err := ms.WindowAdded(ws, second); err != nil {
		t.Fatalf("WindowAdded: %v", err)
	}

	ids := ms.WindowIDs()
	if len(ids) != 2 {
		t.Fatalf("windowIDs len = %d, want 2", len(ids))
	}

	// New window becomes master → splith on it (SideRight default)
	expected := fmt.Sprintf("[con_id=%d] splith", second.ID)
	if !mock.HasCommand(expected) {
		t.Errorf("missing splith command; want %s, got %v", expected, mock.Commands)
	}
}

func TestWindowAddedThird(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 2)
	mock.Tree = ws

	// Add first two
	ms.WindowAdded(ws, ws.Nodes[0])
	mock.ClearCommands()
	ms.WindowAdded(ws, ws.Nodes[1])
	mock.ClearCommands()

	// Add third window
	third := sway.CreateWindow("third")
	ws.Nodes = append(ws.Nodes, third)
	third.Parent = ws
	if err := ms.WindowAdded(ws, third); err != nil {
		t.Fatalf("WindowAdded: %v", err)
	}

	ids := ms.WindowIDs()
	if len(ids) != 3 {
		t.Fatalf("windowIDs len = %d, want 3", len(ids))
	}
}

func TestWindowAddedExcluded(t *testing.T) {
	ms, _ := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 0)

	// Floating window should be excluded
	floatingWin := &sway.Node{Type: "floating_con", ID: 999}
	if err := ms.WindowAdded(ws, floatingWin); err != nil {
		t.Fatalf("WindowAdded: %v", err)
	}

	if len(ms.WindowIDs()) != 0 {
		t.Error("floating window should be excluded")
	}
}

// --- WindowRemoved tests ---

func TestWindowRemovedFromStack(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	ids := ms.WindowIDs()
	removedID := ids[2] // Remove last stack window

	mock.ClearCommands()
	removed := &sway.Node{ID: removedID, Type: "con"}
	if err := ms.WindowRemoved(ws, removed); err != nil {
		t.Fatalf("WindowRemoved: %v", err)
	}

	newIDs := ms.WindowIDs()
	if len(newIDs) != 2 {
		t.Errorf("windowIDs len = %d, want 2", len(newIDs))
	}
}

func TestWindowRemovedMaster(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	ids := ms.WindowIDs()
	masterID := ids[0]

	mock.ClearCommands()
	removed := &sway.Node{ID: masterID, Type: "con", Rect: sway.Rect{Width: 800}}
	if err := ms.WindowRemoved(ws, removed); err != nil {
		t.Fatalf("WindowRemoved: %v", err)
	}

	newIDs := ms.WindowIDs()
	if len(newIDs) != 2 {
		t.Errorf("windowIDs len = %d, want 2", len(newIDs))
	}

	// New master should have been promoted
	if newIDs[0] == masterID {
		t.Error("old master should not still be master")
	}

	// Should have move command for promotion
	moveCmds := mock.CommandsMatching("move")
	if len(moveCmds) == 0 {
		t.Error("no move commands for master promotion")
	}
}

func TestWindowRemovedNotTracked(t *testing.T) {
	ms, _ := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 1)
	ms.ArrangeAll(ws)

	unknown := &sway.Node{ID: 9999, Type: "con"}
	if err := ms.WindowRemoved(ws, unknown); err != nil {
		t.Fatalf("WindowRemoved: %v", err)
	}
}

func TestWindowRemovedFloating(t *testing.T) {
	ms, _ := newTestMasterStack()

	// A floating window was never tracked (WindowAdded excludes floats),
	// so removing it must be a no-op — popWindow's indexOf<0 guard.
	floated := &sway.Node{ID: 42, Type: "con", Floating: "user_on"}
	if err := ms.WindowRemoved(nil, floated); err != nil {
		t.Fatalf("WindowRemoved floating: %v", err)
	}
	if len(ms.WindowIDs()) != 0 {
		t.Errorf("tracking should stay empty, got %v", ms.WindowIDs())
	}
}

// --- WindowFocused tests ---

func TestWindowFocused(t *testing.T) {
	ms, _ := newTestMasterStack()

	window := &sway.Node{ID: 42, Type: "con"}
	if err := ms.WindowFocused(nil, window); err != nil {
		t.Fatalf("WindowFocused: %v", err)
	}

	if ms.lastFocusedID == nil || *ms.lastFocusedID != 42 {
		t.Errorf("lastFocusedID = %v, want 42", ms.lastFocusedID)
	}
}

// --- Command tests ---

func TestSwapMaster(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	ids := ms.WindowIDs()
	originalMaster := ids[0]
	stackWindow := ids[1]

	// Set stack window as focused
	ws.Nodes[1].Focused = true

	mock.ClearCommands()
	if err := ms.Command("swap-master", ws); err != nil {
		t.Fatalf("swap-master: %v", err)
	}

	newIDs := ms.WindowIDs()
	if newIDs[0] == originalMaster {
		t.Error("master should have changed after swap")
	}
	if newIDs[0] != stackWindow {
		t.Errorf("new master = %d, want %d", newIDs[0], stackWindow)
	}

	// Should have swap command
	swapCmds := mock.CommandsMatching("swap container")
	if len(swapCmds) == 0 {
		t.Error("no swap command issued")
	}
}

// focusOnly makes id the sole focused leaf in ws, so a command sees the
// focus a user's keypress would have left behind.
func focusOnly(t *testing.T, ws *sway.Node, id int64) {
	t.Helper()
	for _, n := range ws.Nodes {
		n.Focused = false
	}
	target := ws.FindByID(id)
	if target == nil {
		t.Fatalf("window %d not in tree", id)
	}
	target.Focused = true
}

// swap-master promotes with MRU (alt-tab) ordering: the old master lands
// at the top of the stack and everything it passed shifts down one, so it
// does not trade places with the promoted window.
func TestSwapMasterMRUOrder(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 4)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	ids := slices.Clone(ms.WindowIDs())
	a, b, c, d := ids[0], ids[1], ids[2], ids[3]

	// Promote the bottom stack window.
	focusOnly(t, ws, d)
	if err := ms.Command("swap-master", ws); err != nil {
		t.Fatalf("swap-master: %v", err)
	}

	want := []int64{d, a, b, c}
	if got := ms.WindowIDs(); !slices.Equal(got, want) {
		t.Errorf("after promoting bottom window: got %v, want %v (old master to top of stack)", got, want)
	}
}

// Promoting the top of the stack repeatedly alternates between the same
// two windows, and the rest of the stack keeps its order — the property
// that makes $mod+o / $mod+Return behave like alt-tab.
func TestSwapMasterAltTabCycle(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 4)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	ids := slices.Clone(ms.WindowIDs())
	a, b, c, d := ids[0], ids[1], ids[2], ids[3]

	// Seed the cycle by promoting the bottom window: [d a b c].
	focusOnly(t, ws, d)
	if err := ms.Command("swap-master", ws); err != nil {
		t.Fatalf("seed swap-master: %v", err)
	}

	// Each round focuses the top of the stack and promotes it, which is
	// what $mod+o followed by $mod+Return does.
	for round, want := range [][]int64{
		{a, d, b, c},
		{d, a, b, c},
		{a, d, b, c},
		{d, a, b, c},
	} {
		top := ms.WindowIDs()[1]
		focusOnly(t, ws, top)
		if err := ms.Command("swap-master", ws); err != nil {
			t.Fatalf("round %d swap-master: %v", round, err)
		}
		if got := ms.WindowIDs(); !slices.Equal(got, want) {
			t.Fatalf("round %d: got %v, want %v", round, got, want)
		}
	}

	// The untouched tail never moved.
	if got := ms.WindowIDs()[2:]; !slices.Equal(got, []int64{b, c}) {
		t.Errorf("tail = %v, want %v (cycling must not disturb the rest of the stack)", got, []int64{b, c})
	}
}

// Focusing from master toward the stack must land on the TOP of the stack,
// which under MRU promotion is the window just demoted from master — the
// other half of the $mod+o / $mod+Return alt-tab cycle.
//
// The assertion is on the emitted command, and that is the whole point:
// delegating to sway's native `focus right` is exactly the bug. Sway
// descends into a container via its focus history (seat_get_focus_inactive),
// NOT its first child, so it lands on whichever stack window was touched
// last — "somewhere in the middle". Verified against real headless sway:
// with master=5 and column=[6 top, 7 middle, 8 bottom], touching 7 and then
// running `focus right` from master lands on 7, not 6. So the fix is to
// stop delegating and name the target con_id explicitly.
//
// This cannot be asserted against the sim: sim.directionalSibling descends
// with `leaf.Nodes[0]`, so the sim lands on the TOP and would pass even on
// the unfixed code. See the divergence note in internal/harness/sim/apply.go.
func TestFocusTowardStackLandsOnTop(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 4)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	ids := slices.Clone(ms.WindowIDs())
	master, top := ids[0], ids[1]

	// Stack is on the right by default, so $mod+o (focus right) from the
	// master heads into the stack.
	focusOnly(t, ws, master)
	mock.ClearCommands()
	if err := ms.Command("focus right", ws); err != nil {
		t.Fatalf("focus right: %v", err)
	}

	want := fmt.Sprintf("[con_id=%d] focus", top)
	if !mock.HasCommand(want) {
		t.Errorf("focus right from master issued %v, want %q — focus must name the\n"+
			"top of the stack, not delegate to sway's focus-history descent",
			mock.Commands, want)
	}
	for _, c := range mock.Commands {
		if c == "focus right" {
			t.Errorf("issued a bare %q: sway would descend by focus history and land on\n"+
				"the last-touched stack window instead of the top", c)
		}
	}
}

// Focusing away from the stack still falls through to sway, which is what
// keeps focus able to cross to another output — the master column is not
// the edge of the world.
func TestFocusAwayFromStackDelegatesToSway(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 4)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	focusOnly(t, ws, ms.WindowIDs()[0])
	mock.ClearCommands()
	if err := ms.Command("focus left", ws); err != nil {
		t.Fatalf("focus left: %v", err)
	}

	if !mock.HasCommand("focus left") {
		t.Errorf("focus left from master issued %v, want a native %q to fall through\n"+
			"to sway (cross-output navigation)", mock.Commands, "focus left")
	}
}

// With the stack on the left, the direction that heads into it flips.
func TestFocusTowardStackHonorsStackSide(t *testing.T) {
	cfg := DefaultMasterStackConfig()
	cfg.StackSide = SideLeft
	ms, mock := newTestMasterStack(cfg)
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 4)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	ids := slices.Clone(ms.WindowIDs())
	focusOnly(t, ws, ids[0])
	mock.ClearCommands()
	if err := ms.Command("focus left", ws); err != nil {
		t.Fatalf("focus left: %v", err)
	}

	want := fmt.Sprintf("[con_id=%d] focus", ids[1])
	if !mock.HasCommand(want) {
		t.Errorf("with stackSide=left, focus left from master issued %v, want %q",
			mock.Commands, want)
	}
}

func TestSwapMasterTooFewWindows(t *testing.T) {
	ms, _ := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 1)
	ms.ArrangeAll(ws)

	if err := ms.Command("swap-master", ws); err != nil {
		t.Fatalf("swap-master with 1 window: %v", err)
	}
}

func TestRotateCW(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	ids := ms.WindowIDs()
	lastID := ids[2]

	mock.ClearCommands()
	if err := ms.Command("rotate cw", ws); err != nil {
		t.Fatalf("rotate cw: %v", err)
	}

	newIDs := ms.WindowIDs()
	// CW rotation: last becomes first
	if newIDs[0] != lastID {
		t.Errorf("after CW rotation, first = %d, want %d (was last)", newIDs[0], lastID)
	}
}

func TestRotateCCW(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	ids := ms.WindowIDs()
	firstID := ids[0]

	mock.ClearCommands()
	if err := ms.Command("rotate ccw", ws); err != nil {
		t.Fatalf("rotate ccw: %v", err)
	}

	newIDs := ms.WindowIDs()
	// CCW rotation: first becomes last
	if newIDs[len(newIDs)-1] != firstID {
		t.Errorf("after CCW rotation, last = %d, want %d (was first)",
			newIDs[len(newIDs)-1], firstID)
	}
}

func TestToggleStackLayout(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	// Default is splitv, toggle should cycle
	mock.ClearCommands()
	ms.Command("stack toggle", ws)

	if ms.config.StackLayout != StackSplitH {
		t.Errorf("after toggle, stackLayout = %v, want StackSplitH", ms.config.StackLayout)
	}
}

func TestToggleStackSide(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	originalSide := ms.config.StackSide

	mock.ClearCommands()
	ms.Command("stack side-toggle", ws)

	if ms.config.StackSide == originalSide {
		t.Error("side should have changed after toggle")
	}
}

func TestMaximize(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	mock.ClearCommands()
	ms.Command("maximize", ws)

	if !ms.maximized {
		t.Error("should be maximized after toggle")
	}

	// Should have set tabbed layout
	tabbedCmds := mock.CommandsMatching("layout tabbed")
	if len(tabbedCmds) == 0 {
		t.Error("no tabbed layout command for maximize")
	}

	// Toggle back
	mock.ClearCommands()
	ms.Command("maximize", ws)
	if ms.maximized {
		t.Error("should be unmaximized after second toggle")
	}
}

func TestFocusMaster(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	masterID := ms.WindowIDs()[0]
	mock.ClearCommands()
	ms.Command("focus master", ws)

	expected := fmt.Sprintf("[con_id=%d] focus", masterID)
	if !mock.HasCommand(expected) {
		t.Errorf("missing focus master command: %s", expected)
	}
}

func TestFocusRelative(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	ws.Nodes[0].Focused = true
	mock.Tree = ws
	ms.ArrangeAll(ws)

	mock.ClearCommands()
	ms.Command("focus down", ws)

	// Should focus the next window
	focusCmds := mock.CommandsMatching("focus")
	if len(focusCmds) == 0 {
		t.Error("no focus command issued")
	}
}

func TestMoveRelative(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	ids := ms.WindowIDs()
	// IDs are [100, 101, 102]. Focus window at index 1 (a stack window).
	ws.Nodes[1].Focused = true
	focusedID := ids[1]

	mock.ClearCommands()
	ms.Command("move down", ws)

	newIDs := ms.WindowIDs()
	// Focused window (index 1) should have moved to index 2
	if newIDs[2] != focusedID {
		t.Errorf("after move down, position 2 = %d, want %d", newIDs[2], focusedID)
	}
}

func TestGrowShrinkMasterWidth(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 2)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	// Grow
	mock.ClearCommands()
	ms.Command("master grow", ws)
	if ms.config.MasterWidth != 55 {
		t.Errorf("after grow, masterWidth = %d, want 55", ms.config.MasterWidth)
	}

	// Shrink
	mock.ClearCommands()
	ms.Command("master shrink", ws)
	if ms.config.MasterWidth != 50 {
		t.Errorf("after shrink, masterWidth = %d, want 50", ms.config.MasterWidth)
	}
}

func TestMasterWidthClampedMax(t *testing.T) {
	ms, mock := newTestMasterStack(MasterStackConfig{
		MasterWidth: 89,
		StackLayout: StackSplitV,
		StackSide:   SideRight,
		MasterCount: 1,
	})
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 2)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	ms.Command("master grow", ws)
	if ms.config.MasterWidth > 90 {
		t.Errorf("masterWidth = %d, should be clamped to 90", ms.config.MasterWidth)
	}
}

func TestMasterWidthClampedMin(t *testing.T) {
	ms, mock := newTestMasterStack(MasterStackConfig{
		MasterWidth: 11,
		StackLayout: StackSplitV,
		StackSide:   SideRight,
		MasterCount: 1,
	})
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 2)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	ms.Command("master shrink", ws)
	if ms.config.MasterWidth < 10 {
		t.Errorf("masterWidth = %d, should be clamped to 10", ms.config.MasterWidth)
	}
}

func TestUnknownCommand(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 1)
	mock.Tree = ws

	err := ms.Command("unknown-cmd", ws)
	if err == nil {
		t.Error("expected error for unknown command")
	}
}

// --- StackLayout tests ---

func TestStackLayoutCycle(t *testing.T) {
	tests := []struct {
		from StackLayout
		to   StackLayout
	}{
		{StackSplitV, StackSplitH},
		{StackSplitH, StackStacking},
		{StackStacking, StackTabbed},
		{StackTabbed, StackSplitV},
	}
	for _, tt := range tests {
		got := tt.from.NextStackLayout()
		if got != tt.to {
			t.Errorf("%v.NextStackLayout() = %v, want %v", tt.from, got, tt.to)
		}
	}
}

func TestStackLayoutString(t *testing.T) {
	tests := []struct {
		layout StackLayout
		want   string
	}{
		{StackSplitV, "splitv"},
		{StackSplitH, "splith"},
		{StackStacking, "stacking"},
		{StackTabbed, "tabbed"},
		{StackLayout(99), "splitv"}, // default
	}
	for _, tt := range tests {
		if got := tt.layout.String(); got != tt.want {
			t.Errorf("%v.String() = %q, want %q", tt.layout, got, tt.want)
		}
	}
}

func TestParseStackLayout(t *testing.T) {
	tests := []struct {
		input string
		want  StackLayout
	}{
		{"splitv", StackSplitV},
		{"splith", StackSplitH},
		{"stacking", StackStacking},
		{"tabbed", StackTabbed},
		{"SPLITH", StackSplitH},
		{"unknown", StackSplitV},
	}
	for _, tt := range tests {
		if got := ParseStackLayout(tt.input); got != tt.want {
			t.Errorf("ParseStackLayout(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

// --- Side tests ---

func TestSideOpposite(t *testing.T) {
	if SideRight.Opposite() != SideLeft {
		t.Error("Right.Opposite() != Left")
	}
	if SideLeft.Opposite() != SideRight {
		t.Error("Left.Opposite() != Right")
	}
}

func TestSideMoveDir(t *testing.T) {
	if SideRight.MoveDir() != "right" {
		t.Error("Right.MoveDir() != right")
	}
	if SideLeft.MoveDir() != "left" {
		t.Error("Left.MoveDir() != left")
	}
}

func TestParseSide(t *testing.T) {
	if ParseSide("left") != SideLeft {
		t.Error("ParseSide(left) != SideLeft")
	}
	if ParseSide("LEFT") != SideLeft {
		t.Error("ParseSide(LEFT) != SideLeft")
	}
	if ParseSide("right") != SideRight {
		t.Error("ParseSide(right) != SideRight")
	}
	if ParseSide("anything") != SideRight {
		t.Error("ParseSide(anything) != SideRight")
	}
}

// --- Left stack side tests ---

func TestArrangeAllLeftSide(t *testing.T) {
	cfg := DefaultMasterStackConfig()
	cfg.StackSide = SideLeft
	ms, mock := newTestMasterStack(cfg)
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 2)
	mock.Tree = ws

	if err := ms.ArrangeAll(ws); err != nil {
		t.Fatalf("ArrangeAll: %v", err)
	}

	ids := ms.WindowIDs()
	if len(ids) != 2 {
		t.Fatalf("windowIDs len = %d, want 2", len(ids))
	}

	// For left side, the stack window gets splith, then master moves to it
	stackID := ids[1]
	expected := fmt.Sprintf("[con_id=%d] splith", stackID)
	if !mock.HasCommand(expected) {
		t.Error("missing left-side splith on stack window")
	}
}

// --- Substack tests ---

func TestSubstackCreated(t *testing.T) {
	cfg := DefaultMasterStackConfig()
	cfg.VisibleStackLimit = 2
	ms, mock := newTestMasterStack(cfg)
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 5)
	mock.Tree = ws

	if err := ms.ArrangeAll(ws); err != nil {
		t.Fatalf("ArrangeAll: %v", err)
	}

	if !ms.substackExists {
		t.Error("substack should exist with 5 windows and limit 2")
	}

	ids := ms.WindowIDs()
	if len(ids) != 5 {
		t.Errorf("windowIDs len = %d, want 5", len(ids))
	}

	// Should have stacking layout command for substack
	stackingCmds := mock.CommandsMatching("layout stacking")
	if len(stackingCmds) == 0 {
		t.Error("no stacking layout command for substack")
	}
}

func TestSubstackNotCreatedUnderLimit(t *testing.T) {
	cfg := DefaultMasterStackConfig()
	cfg.VisibleStackLimit = 5
	ms, mock := newTestMasterStack(cfg)
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	mock.Tree = ws

	ms.ArrangeAll(ws)

	if ms.substackExists {
		t.Error("substack should not exist when under limit")
	}
}

func TestSubstackNoLimitDisabled(t *testing.T) {
	cfg := DefaultMasterStackConfig()
	cfg.VisibleStackLimit = 0
	ms, mock := newTestMasterStack(cfg)
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 10)
	mock.Tree = ws

	ms.ArrangeAll(ws)

	if ms.substackExists {
		t.Error("substack should not exist when limit=0")
	}
}

// --- Config validation tests ---

func TestNewMasterStackManagerInvalidWidth(t *testing.T) {
	mock := sway.NewMock()
	ms := NewMasterStackManager(mock, MasterStackConfig{
		MasterWidth: 0,
		MasterCount: 0,
	})
	if ms.config.MasterWidth != 50 {
		t.Errorf("invalid width should default to 50, got %d", ms.config.MasterWidth)
	}
	if ms.config.MasterCount != 1 {
		t.Errorf("invalid count should default to 1, got %d", ms.config.MasterCount)
	}
}

// --- Mark-based movement verification ---

func TestMarkMovementPattern(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	mock.Tree = ws
	ms.ArrangeAll(ws)
	mock.ClearCommands()

	// Trigger a move to verify mark pattern
	ms.moveWindow(100, 101)

	cmds := mock.Commands
	if len(cmds) != 3 {
		t.Fatalf("expected 3 mark-based commands, got %d: %v", len(cmds), cmds)
	}
	if cmds[0] != "[con_id=101] mark --add move_target" {
		t.Errorf("cmd[0] = %q, want mark command", cmds[0])
	}
	if cmds[1] != "[con_id=100] move window to mark move_target" {
		t.Errorf("cmd[1] = %q, want move to mark", cmds[1])
	}
	if cmds[2] != "[con_id=101] unmark move_target" {
		t.Errorf("cmd[2] = %q, want unmark", cmds[2])
	}
}

func TestSwapWindowsPattern(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	mock.ClearCommands()

	ms.swapWindows(100, 200)

	if len(mock.Commands) != 1 {
		t.Fatalf("expected 1 swap command, got %d", len(mock.Commands))
	}
	if mock.Commands[0] != "[con_id=100] swap container with con_id 200" {
		t.Errorf("swap cmd = %q", mock.Commands[0])
	}
}

// --- NextStackLayout edge case ---

func TestNextStackLayoutDefault(t *testing.T) {
	s := StackLayout(99)
	if s.NextStackLayout() != StackSplitV {
		t.Error("invalid StackLayout should default to SplitV")
	}
}

// --- MoveRelative edge cases ---

func TestMoveUpAtTop(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	ws.Nodes[0].Focused = true
	mock.Tree = ws
	ms.ArrangeAll(ws)

	idsBeforeMove := ms.WindowIDs()
	mock.ClearCommands()
	ms.Command("move up", ws)

	// Should be no-op since we're at position 0
	idsAfterMove := ms.WindowIDs()
	for i, id := range idsBeforeMove {
		if idsAfterMove[i] != id {
			t.Error("move up at top should be no-op")
			break
		}
	}
}

func TestMoveDownAtBottom(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	// Set focus on last window (index 2 in windowIDs)
	ids := ms.WindowIDs()
	lastID := ids[2]
	// Find the actual sway node for this ID and set it focused
	for _, n := range ws.Nodes {
		n.Focused = (n.ID == lastID)
	}

	idsBeforeMove := ms.WindowIDs()
	mock.ClearCommands()
	ms.Command("move down", ws)

	// Should be no-op since we're at the last position
	idsAfterMove := ms.WindowIDs()
	for i, id := range idsBeforeMove {
		if idsAfterMove[i] != id {
			t.Error("move down at bottom should be no-op")
			break
		}
	}
}

// --- User's actual config tests ---

func TestUserConfigMasterStackRight(t *testing.T) {
	// User's workspace 6,7: MasterStack, right side, masterWidth=75, visibleStackLimit=3
	cfg := MasterStackConfig{
		MasterWidth:       75,
		StackLayout:       StackSplitV,
		StackSide:         SideRight,
		VisibleStackLimit: 3,
		MasterCount:       1,
	}
	ms, mock := newTestMasterStack(cfg)
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("6", 4)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	ids := ms.WindowIDs()
	if len(ids) != 4 {
		t.Fatalf("windowIDs len = %d, want 4", len(ids))
	}

	// Verify master width was set to 75
	resizeCmds := mock.CommandsMatching("resize set width 75 ppt")
	if len(resizeCmds) == 0 {
		t.Error("master width not set to 75ppt")
	}
}

func TestUserConfigMasterStackLeft(t *testing.T) {
	// User's workspace 4,9: MasterStack, left side
	cfg := MasterStackConfig{
		MasterWidth:       75,
		StackLayout:       StackSplitV,
		StackSide:         SideLeft,
		VisibleStackLimit: 3,
		MasterCount:       1,
	}
	ms, mock := newTestMasterStack(cfg)
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("4", 3)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	ids := ms.WindowIDs()
	if len(ids) != 3 {
		t.Fatalf("windowIDs len = %d, want 3", len(ids))
	}
}

// --- Rotate with too few windows ---

func TestRotateWithOneWindow(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 1)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	if err := ms.Command("rotate cw", ws); err != nil {
		t.Fatalf("rotate with 1 window: %v", err)
	}
}

// --- Focus with no focused window ---

func TestFocusRelativeNoFocused(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	// No window has Focused=true
	mock.Tree = ws
	ms.ArrangeAll(ws)

	mock.ClearCommands()
	ms.Command("focus down", ws)

	// Should be no-op
	focusCmds := mock.CommandsMatching("focus")
	if len(focusCmds) != 0 {
		t.Error("focus down with no focused window should be no-op")
	}
}

// --- Coverage for every binding the user has bound in their sway config ---
//
// Each of these names a sway config binding that goes through
// ParseNopCommand → MasterStack.Command. They must all return nil
// (no "unknown command" error) when the manager is in a sane state.

func TestBindingVerbsAllResolve(t *testing.T) {
	verbs := []string{
		"focus down", "focus up", "focus left", "focus right",
		"move up", "move down", "move left", "move right",
		"swap-master", "focus master", "focus previous",
		"rotate cw", "rotate ccw",
		"stack toggle", "stack side-toggle",
		"maximize",
		"master add", "master remove",
		"master grow", "master shrink",
	}
	for _, v := range verbs {
		t.Run(v, func(t *testing.T) {
			ms, mock := newTestMasterStack()
			sway.ResetIDCounter()
			ws := sway.CreateWorkspace("1", 4)
			ws.Nodes[1].Focused = true
			mock.Tree = ws
			if err := ms.ArrangeAll(ws); err != nil {
				t.Fatalf("ArrangeAll: %v", err)
			}
			if err := ms.Command(v, ws); err != nil {
				t.Errorf("Command(%q) returned %v — binding would no-op in production", v, err)
			}
		})
	}
}

func TestStackToggleAdvancesCycle(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	mock.Tree = ws
	ms.ArrangeAll(ws)
	before := ms.config.StackLayout
	if err := ms.Command("stack toggle", ws); err != nil {
		t.Fatalf("stack toggle: %v", err)
	}
	if ms.config.StackLayout == before {
		t.Errorf("stack toggle did not advance layout (still %v)", before)
	}
}

func TestStackSideToggleFlipsSide(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	mock.Tree = ws
	ms.ArrangeAll(ws)
	before := ms.config.StackSide
	if err := ms.Command("stack side-toggle", ws); err != nil {
		t.Fatalf("stack side-toggle: %v", err)
	}
	if ms.config.StackSide == before {
		t.Errorf("stack side-toggle did not flip side (still %v)", before)
	}
}

func TestMasterAddIncrementsCount(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 4)
	mock.Tree = ws
	ms.ArrangeAll(ws)
	before := ms.config.MasterCount
	if err := ms.Command("master add", ws); err != nil {
		t.Fatalf("master add: %v", err)
	}
	if ms.config.MasterCount != before+1 {
		t.Errorf("MasterCount = %d, want %d", ms.config.MasterCount, before+1)
	}
}

func TestMasterAddClampedToWindowCount(t *testing.T) {
	// With only 2 windows, MasterCount can never exceed 1 — there must
	// always be at least one stack window to keep the layout coherent.
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 2)
	mock.Tree = ws
	ms.ArrangeAll(ws)
	if err := ms.Command("master add", ws); err != nil {
		t.Fatalf("master add: %v", err)
	}
	if ms.config.MasterCount != 1 {
		t.Errorf("MasterCount = %d, want 1 (clamped)", ms.config.MasterCount)
	}
}

func TestMasterRemoveClampedToOne(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	mock.Tree = ws
	ms.ArrangeAll(ws)
	if err := ms.Command("master remove", ws); err != nil {
		t.Fatalf("master remove: %v", err)
	}
	if ms.config.MasterCount < 1 {
		t.Errorf("MasterCount = %d, want at least 1", ms.config.MasterCount)
	}
}

// Horizontal-move semantics: master toward stack side →
// top of stack; stack window away from stack side → becomes master;
// otherwise no-op. With StackSide=Right (default), "move right" on the
// master promotes it into the stack, and "move left" on a stack window
// promotes it to master.

func TestMoveHorizontalMasterTowardStack(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3) // StackSide=Right default
	mock.Tree = ws
	ms.ArrangeAll(ws)

	originalMaster := ms.WindowIDs()[0]
	for _, n := range ws.Nodes {
		n.Focused = (n.ID == originalMaster)
	}

	if err := ms.Command("move right", ws); err != nil {
		t.Fatalf("move right: %v", err)
	}
	if ms.WindowIDs()[0] == originalMaster {
		t.Errorf("master moved right should demote itself; still at idx 0")
	}
	if ms.WindowIDs()[1] != originalMaster {
		t.Errorf("original master should be at idx 1, got %d", ms.WindowIDs()[1])
	}
}

func TestMoveHorizontalStackAwayFromStack(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	originalMaster := ms.WindowIDs()[0]
	stackID := ms.WindowIDs()[1]
	for _, n := range ws.Nodes {
		n.Focused = (n.ID == stackID)
	}

	if err := ms.Command("move left", ws); err != nil {
		t.Fatalf("move left: %v", err)
	}
	if ms.WindowIDs()[0] != stackID {
		t.Errorf("stack window moved left should become master; got idx0=%d, want %d",
			ms.WindowIDs()[0], stackID)
	}
	if ms.WindowIDs()[0] == originalMaster {
		t.Errorf("original master should have been displaced")
	}
}

func TestMoveHorizontalMasterAwayFromStackIsNoOp(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	before := append([]int64(nil), ms.WindowIDs()...)
	master := ms.WindowIDs()[0]
	for _, n := range ws.Nodes {
		n.Focused = (n.ID == master)
	}

	if err := ms.Command("move left", ws); err != nil {
		t.Fatalf("move left: %v", err)
	}
	for i, id := range before {
		if ms.WindowIDs()[i] != id {
			t.Errorf("master moved away from stack should be no-op; positions changed: before=%v after=%v",
				before, ms.WindowIDs())
			break
		}
	}
}

func TestMoveHorizontalStackTowardStackIsNoOp(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	before := append([]int64(nil), ms.WindowIDs()...)
	stackID := ms.WindowIDs()[1]
	for _, n := range ws.Nodes {
		n.Focused = (n.ID == stackID)
	}

	if err := ms.Command("move right", ws); err != nil {
		t.Fatalf("move right: %v", err)
	}
	for i, id := range before {
		if ms.WindowIDs()[i] != id {
			t.Errorf("stack moved toward stack should be no-op; before=%v after=%v",
				before, ms.WindowIDs())
			break
		}
	}
}

func TestMoveHorizontalLeftSideConfig(t *testing.T) {
	// With StackSide=Left the directions flip: master moves LEFT toward
	// stack, stack window moves RIGHT to become master.
	cfg := DefaultMasterStackConfig()
	cfg.StackSide = SideLeft
	ms, mock := newTestMasterStack(cfg)
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	master := ms.WindowIDs()[0]
	for _, n := range ws.Nodes {
		n.Focused = (n.ID == master)
	}

	if err := ms.Command("move left", ws); err != nil {
		t.Fatalf("move left: %v", err)
	}
	if ms.WindowIDs()[1] != master {
		t.Errorf("master moved left (toward left-side stack) should land at idx 1; got %v",
			ms.WindowIDs())
	}
}

// TestFlattenWorkspace_UnwindsDeepSingletonChain guards the live ws7 bug:
// MasterStack.arrangeWindows used to issue `move to workspace <self>` per
// leaf to "flatten" prior wrappers, but real sway (container_move_to_workspace,
// sway/commands/move.c:200-202) early-returns on same-workspace moves and
// does nothing. Wrappers from every previous 2-window rebuild accumulated
// — the journal showed a 12-deep singleton chain on ws7.
//
// Fix: emit `split none` once per singleton ancestor. split none is sway's
// real flatten primitive (do_unsplit, sway/commands/split.c:35-50).
func TestFlattenWorkspace_UnwindsDeepSingletonChain(t *testing.T) {
	ms, mock := newTestMasterStack()

	// Build ws → w1 → w2 → w3 → w4 → leaf (4 singleton wrappers deep)
	// plus a separate top-level leaf so the tree has multiple children
	// and the inner chain isn't trivially collapsed by a single call.
	leaf := &sway.Node{ID: 101, Type: "con", Name: "deep"}
	w4 := &sway.Node{ID: 204, Type: "con", Layout: "splitv", Nodes: []*sway.Node{leaf}}
	w3 := &sway.Node{ID: 203, Type: "con", Layout: "splith", Nodes: []*sway.Node{w4}}
	w2 := &sway.Node{ID: 202, Type: "con", Layout: "splitv", Nodes: []*sway.Node{w3}}
	w1 := &sway.Node{ID: 201, Type: "con", Layout: "splith", Nodes: []*sway.Node{w2}}
	other := &sway.Node{ID: 102, Type: "con", Name: "other"}
	ws := &sway.Node{
		ID: 1, Name: "7", Type: "workspace", Layout: "splith",
		Nodes: []*sway.Node{w1, other},
	}
	ws.SetParents()
	mock.Tree = ws

	ms.flattenWorkspace(ws, []*sway.Node{leaf, other})

	// New flattener walks upward from each leaf. For every ancestor whose
	// parent is a singleton, it issues `split none` on *that ancestor*,
	// not on the leaf. So we expect one split none per level:
	//   leaf(101) — parent w4 has 1 child
	//   w4(204)   — parent w3 has 1 child
	//   w3(203)   — parent w2 has 1 child
	//   w2(202)   — parent w1 has 1 child
	// w1(201) stops the walk because its parent is the workspace.
	for _, id := range []int64{101, 204, 203, 202} {
		cmd := fmt.Sprintf("[con_id=%d] split none", id)
		if !mock.HasCommand(cmd) {
			t.Errorf("expected %q; commands=%v", cmd, mock.Commands)
		}
	}
	if mock.HasCommand("[con_id=201] split none") {
		t.Errorf("w1 sits directly under the workspace (not a singleton child); "+
			"should not emit split none. commands=%v", mock.Commands)
	}
	if mock.HasCommand("[con_id=102] split none") {
		t.Errorf("`other` has no singleton ancestors but got split none; commands=%v", mock.Commands)
	}
}

// TestRemoveExtraNesting_SkipsWhenMasterHasSiblings covers the case
// where the master is nested alongside a sibling: real sway rejects
// `split none` on a target that has siblings
// ("Can only flatten a child container with no siblings"), so
// removeExtraNesting must not emit one. The guard requires both extra
// nesting *and* `len(master.Parent.Children) == 1`.
func TestRemoveExtraNesting_SkipsWhenMasterHasSiblings(t *testing.T) {
	ms, mock := newTestMasterStack()

	master := &sway.Node{ID: 101, Type: "con"}
	sibling := &sway.Node{ID: 102, Type: "con"}
	inner := &sway.Node{ID: 103, Type: "con", Layout: "splitv",
		Nodes: []*sway.Node{master, sibling}}
	outer := &sway.Node{ID: 104, Type: "con", Layout: "splith",
		Nodes: []*sway.Node{inner}}
	ws := &sway.Node{ID: 105, Name: "7", Type: "workspace", Layout: "splith",
		Nodes: []*sway.Node{outer}}
	ws.SetParents()
	mock.Tree = ws

	ms.windowIDs = []int64{101, 102}

	ms.removeExtraNesting(ws)

	if mock.HasCommand("[con_id=101] split none") {
		t.Errorf("emitted `[con_id=101] split none` on a master with a sibling; "+
			"sway would reject. commands=%v", mock.Commands)
	}
}

// TestArrangeAllSkipsFloatingLeaves guards Bug D: fuzzer seed=1 step=31
// showed a floating con_id (1014) ended up in MasterStack.windowIDs after
// a binding-triggered ArrangeAll. The bug was that arrangeWindows iterated
// ws.Leaves() without IsExcluded filtering, so floating leaves (which in
// the sim still live under .Nodes with Floating="user_on") got pushed
// into the layout and tracking list.
func TestArrangeAllSkipsFloatingLeaves(t *testing.T) {
	ms, mock := newTestMasterStack()
	tiled1 := &sway.Node{ID: 101, Type: "con", Name: "tiled1"}
	tiled2 := &sway.Node{ID: 102, Type: "con", Name: "tiled2"}
	floater := &sway.Node{ID: 103, Type: "con", Name: "float", Floating: "user_on"}
	ws := &sway.Node{
		ID: 1, Name: "7", Type: "workspace", Layout: "splith",
		Nodes: []*sway.Node{tiled1, tiled2, floater},
	}
	ws.SetParents()
	mock.Tree = ws

	if err := ms.ArrangeAll(ws); err != nil {
		t.Fatalf("ArrangeAll: %v", err)
	}

	for _, id := range ms.WindowIDs() {
		if id == 103 {
			t.Errorf("floating leaf 103 ended up in windowIDs=%v (should be filtered)", ms.WindowIDs())
		}
	}
}

// TestArrangeAllDoesNotPromoteFloatingFocused guards Bug D (second half):
// arrangeWindows unconditionally prepended ws.FindFocused() as the master
// candidate, re-introducing a floating leaf into tracking even though the
// earlier IsExcluded filter had skipped it. Fuzzer seed=1 step=273 caught
// this after a focused leaf toggled floating.
func TestArrangeAllDoesNotPromoteFloatingFocused(t *testing.T) {
	ms, mock := newTestMasterStack()
	t1 := &sway.Node{ID: 101, Type: "con", Name: "t1"}
	t2 := &sway.Node{ID: 102, Type: "con", Name: "t2"}
	floatFocus := &sway.Node{ID: 103, Type: "con", Name: "float-focus",
		Floating: "user_on", Focused: true}
	ws := &sway.Node{
		ID: 1, Name: "7", Type: "workspace", Layout: "splith",
		Nodes: []*sway.Node{t1, t2, floatFocus},
	}
	ws.SetParents()
	mock.Tree = ws

	if err := ms.ArrangeAll(ws); err != nil {
		t.Fatalf("ArrangeAll: %v", err)
	}

	for _, id := range ms.WindowIDs() {
		if id == 103 {
			t.Errorf("floating focused leaf 103 ended up in windowIDs=%v", ms.WindowIDs())
		}
	}
}

// TestArrangeAllIncludesWindowsUnderStackedWrapper guards Bug G:
// arrangeWindows used to pre-filter via IsExcluded, which discards leaves
// whose parent is tabbed/stacked. But mid-loop cascadeFlatten can "free"
// such a leaf from its wrapper during the flatten-pass (if its siblings
// move out first), leaving it visible at the workspace root — untracked
// by MasterStack. Fuzzer: ~140 residual missed-leaf violations per sweep
// traced to this. Fix: include all non-floating non-fullscreen leaves
// regardless of parent layout; the flatten `move to workspace` calls
// unconditionally re-parent them anyway.
func TestArrangeAllIncludesWindowsUnderStackedWrapper(t *testing.T) {
	ms, mock := newTestMasterStack()
	sibling := &sway.Node{ID: 101, Type: "con", Name: "direct"}
	stackedChild := &sway.Node{ID: 102, Type: "con", Name: "in-stack"}
	wrapper := &sway.Node{ID: 200, Type: "con", Layout: "stacked",
		Nodes: []*sway.Node{stackedChild}}
	ws := &sway.Node{
		ID: 1, Name: "7", Type: "workspace", Layout: "splith",
		Nodes: []*sway.Node{sibling, wrapper},
	}
	ws.SetParents()
	mock.Tree = ws

	if err := ms.ArrangeAll(ws); err != nil {
		t.Fatalf("ArrangeAll: %v", err)
	}

	want := map[int64]bool{101: true, 102: true}
	got := map[int64]bool{}
	for _, id := range ms.WindowIDs() {
		got[id] = true
	}
	for id := range want {
		if !got[id] {
			t.Errorf("windowIDs=%v missing leaf %d (stacked-parented leaves must be included)",
				ms.WindowIDs(), id)
		}
	}
}

// TestWindowAddedAlreadyTrackedRearranges guards two bugs at once.
//
// Bug B (seed=5 step=109): re-adding a tracked window must never reach
// insertAtIndex, which can compute a self-targeting moveWindow (mark and
// mover the same con_id) — nil-deref in the sim, nonsense command in
// production. The original guard was a pure skip.
//
// 2026-06-12 L8: a pure skip is also wrong — WindowAdded fires for a
// tracked id when the user re-tiles a window that floated without an
// event (swap-transferred floatingness), and sway re-attaches it at an
// arbitrary tree position. Skipping leaves the layout scrambled; the
// correct response is a full re-arrange (which rebuilds tracking from
// the tree and cannot self-move).
func TestWindowAddedAlreadyTrackedRearranges(t *testing.T) {
	ms, mock := newTestMasterStack()
	win1 := &sway.Node{ID: 101, Type: "con", Name: "w1"}
	win2 := &sway.Node{ID: 102, Type: "con", Name: "w2"}
	ws := &sway.Node{
		ID: 1, Name: "7", Type: "workspace", Layout: "splith",
		Nodes: []*sway.Node{win1, win2},
	}
	ws.SetParents()
	mock.Tree = ws

	if err := ms.ArrangeAll(ws); err != nil {
		t.Fatalf("ArrangeAll: %v", err)
	}
	preIDs := append([]int64(nil), ms.WindowIDs()...)

	if err := ms.WindowAdded(ws, win2); err != nil {
		t.Fatalf("WindowAdded (duplicate): %v", err)
	}

	// Tracking must stay duplicate-free and the same size (re-arrange,
	// not re-insert).
	got := ms.WindowIDs()
	if len(got) != len(preIDs) {
		t.Errorf("duplicate WindowAdded changed windowIDs: before=%v after=%v", preIDs, got)
	}
	seen := map[int64]bool{}
	for _, id := range got {
		if seen[id] {
			t.Errorf("duplicate id %d in windowIDs %v", id, got)
		}
		seen[id] = true
	}
	// And no self-targeting move command may have been issued (Bug B's
	// signature: mark --add and move-to-mark naming the same con_id).
	for i := 0; i+1 < len(mock.Commands); i++ {
		var markID, moveID int64
		if _, err := fmt.Sscanf(mock.Commands[i], "[con_id=%d] mark --add move_target", &markID); err != nil {
			continue
		}
		if _, err := fmt.Sscanf(mock.Commands[i+1], "[con_id=%d] move window to mark move_target", &moveID); err != nil {
			continue
		}
		if markID == moveID {
			t.Errorf("self-targeting move: %q then %q", mock.Commands[i], mock.Commands[i+1])
		}
	}
}

// TestPopWindowPreservesMasterPxWhenEventRectIsZero covers Bug I: sway
// zeroes container.pending.width/height before firing window::move for a
// cross-workspace move (sway/commands/move.c:227), so when the outgoing
// master's WindowRemoved handler fires, window.Rect.Width is 0. Older
// code tried `resize set width window.Rect.Width px` — which becomes
// `resize set width 0 px`, a silent no-op in real sway (see
// cmd_resize_set in sway/commands/resize.c:452). The fix: snapshot the
// master's pixel width during every arrangeWindows pass and use it here.
//
// The snapshot derives the px from config.MasterWidth% of the workspace
// rect (not the master leaf's Rect.Width — see
// TestArrangeMasterPxFromConfigNotStaleRect for why the leaf rect is
// unreliable). With the default 50% config and a 2560-wide workspace,
// that's 1280px.
func TestPopWindowPreservesMasterPxWhenEventRectIsZero(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3) // 2560 wide; default config 50%
	mock.Tree = ws

	if err := ms.ArrangeAll(ws); err != nil {
		t.Fatalf("ArrangeAll: %v", err)
	}
	// The arrange pass stashes config% of the workspace width: 50% of 2560.
	wantPx := 2560 * 50 / 100
	if ms.lastKnownMasterPx != wantPx {
		t.Fatalf("arrange did not stash master px: lastKnownMasterPx=%d, want %d", ms.lastKnownMasterPx, wantPx)
	}

	master := ws.Nodes[0]
	mock.ClearCommands()

	// Mimic a window::move event payload from sway: pending.width/height
	// have been zeroed before the event fired.
	master.Rect.Width = 0
	master.Rect.Height = 0

	if err := ms.WindowRemoved(ws, master); err != nil {
		t.Fatalf("WindowRemoved: %v", err)
	}

	for _, cmd := range mock.Commands {
		if cmd == fmt.Sprintf("[con_id=%d] resize set width 0 px", ms.WindowIDs()[0]) {
			t.Fatalf("emitted nonsensical zero-width resize: %q", cmd)
		}
	}
	wantResize := fmt.Sprintf("[con_id=%d] resize set width %d px", ms.WindowIDs()[0], wantPx)
	if !mock.HasCommand(wantResize) {
		t.Fatalf("missing %q; got commands=%v", wantResize, mock.Commands)
	}
}

// TestArrangeMasterPxFromConfigNotStaleRect pins the 2026-06-14 "master
// resized to 50%" bug. arrangeWindows used to snapshot lastKnownMasterPx
// from the master leaf's Rect.Width in the PASSED ws — but that ws is
// stale (our splith/move/resize commands run after the caller captured
// it), so a container-moved-in master shows its pre-arrange default width
// (50%) instead of the 75% setMasterWidth applies. popWindow then restored
// the next master to that wrong width. The snapshot must derive from
// config.MasterWidth% of the workspace rect.
func TestArrangeMasterPxFromConfigNotStaleRect(t *testing.T) {
	cfg := DefaultMasterStackConfig()
	cfg.MasterWidth = 75
	ms, mock := newTestMasterStack(cfg)
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("7", 4) // workspace rect is 2560 wide
	// Stale/default leaf widths — windows that moved in at 50%.
	for _, n := range ws.Nodes {
		n.Rect.Width = 1280
	}
	mock.Tree = ws

	if err := ms.ArrangeAll(ws); err != nil {
		t.Fatalf("ArrangeAll: %v", err)
	}
	want := ws.Rect.Width * 75 / 100 // 1920 = 75% of 2560
	if ms.lastKnownMasterPx != want {
		t.Errorf("lastKnownMasterPx=%d (stale 50%% leaf rect), want %d (75%% of workspace)",
			ms.lastKnownMasterPx, want)
	}

	// And popWindow on the master must restore the NEXT master to that
	// configured width, not the stale 1280 (50%).
	master := ws.FindByID(ms.WindowIDs()[0])
	master.Rect.Width = 0 // sway zeroes pending width on the move
	mock.ClearCommands()
	if err := ms.WindowRemoved(ws, master); err != nil {
		t.Fatalf("WindowRemoved: %v", err)
	}
	newMaster := ms.WindowIDs()[0]
	if !mock.HasCommand(fmt.Sprintf("[con_id=%d] resize set width %d px", newMaster, want)) {
		t.Errorf("expected restore to %d px (75%%); got %v", want, mock.Commands)
	}
	if mock.HasCommand(fmt.Sprintf("[con_id=%d] resize set width 1280 px", newMaster)) {
		t.Errorf("restored new master to stale 50%% width (1280px): %v", mock.Commands)
	}
}

// TestPopWindowFallsBackToPptBeforeAnyArrange covers the cold-start
// scenario: the master is popped before any arrangeWindows has had a
// chance to stash a pixel width. In that case the only reasonable fallback
// is the configured ppt — a `resize set width 0 px` would be emitted
// otherwise and silently ignored by sway.
func TestPopWindowFallsBackToPptBeforeAnyArrange(t *testing.T) {
	cfg := DefaultMasterStackConfig()
	cfg.MasterWidth = 60
	ms, mock := newTestMasterStack(cfg)
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	mock.Tree = ws

	// Populate tracking without ArrangeAll (WindowAdded only). No arrange
	// means lastKnownMasterPx is still zero — the intended fallback path.
	for _, n := range ws.Nodes {
		if err := ms.WindowAdded(ws, n); err != nil {
			t.Fatalf("WindowAdded: %v", err)
		}
	}
	if ms.lastKnownMasterPx != 0 {
		t.Fatalf("lastKnownMasterPx=%d, want 0 (no arrange called)", ms.lastKnownMasterPx)
	}

	// Identify the current master and locate its node in the tree.
	masterID := ms.WindowIDs()[0]
	var master *sway.Node
	for _, n := range ws.Nodes {
		if n.ID == masterID {
			master = n
			break
		}
	}
	if master == nil {
		t.Fatalf("master id %d not found in ws.Nodes", masterID)
	}
	mock.ClearCommands()

	master.Rect.Width = 0
	if err := ms.WindowRemoved(ws, master); err != nil {
		t.Fatalf("WindowRemoved: %v", err)
	}

	for _, cmd := range mock.Commands {
		if cmd == fmt.Sprintf("[con_id=%d] resize set width 0 px", ms.WindowIDs()[0]) {
			t.Fatalf("emitted nonsensical zero-width resize: %q", cmd)
		}
	}
	wantResize := fmt.Sprintf("[con_id=%d] resize set width 60 ppt", ms.WindowIDs()[0])
	if !mock.HasCommand(wantResize) {
		t.Fatalf("missing %q; got commands=%v", wantResize, mock.Commands)
	}
}

// The maximized flag must not survive the shape it describes. toggleMaximize
// early-returns below 2 windows, and only arrangeWindows ever clears the
// flag, so closing the stack away while maximized used to strand
// maximized=true on a workspace that is plainly not folded. The next
// maximize press then read the stale flag, decided it was UNmaximizing, and
// replayed the restore against a tree that was never folded — the key did
// the opposite of what it says.
//
// Confirmed against real headless sway before fixing: maximize with 3,
// close down to 1 (`splith(leaf)`, flag still true), reopen 2 (tree back to
// a normal master/stack, flag STILL true), press maximize → it unmaximizes
// and leaves a splitv(splitv(...)) chain.
func TestMaximizedFlagClearedWhenFoldCollapses(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	if err := ms.Command("maximize", ws); err != nil {
		t.Fatalf("maximize: %v", err)
	}
	if !ms.Maximized() {
		t.Fatal("should be maximized after the toggle")
	}

	// Close the stack away, leaving a single window: the fold cannot exist.
	for _, n := range []*sway.Node{ws.Nodes[2], ws.Nodes[1]} {
		if err := ms.WindowRemoved(ws, n); err != nil {
			t.Fatalf("WindowRemoved: %v", err)
		}
	}
	if got := len(ms.WindowIDs()); got != 1 {
		t.Fatalf("tracked %d windows, want 1", got)
	}
	if ms.Maximized() {
		t.Errorf("still claims maximized with %d tracked window(s) — a fold needs two "+
			"containers to share a parent, so the flag is now a lie that suppresses "+
			"the master-width and master-stack-split checks", len(ms.WindowIDs()))
	}
}

// The user-visible half: after the fold has collapsed, pressing maximize
// must MAXIMIZE (fold the master into a tabbed stack), not silently run the
// unmaximize restore.
func TestMaximizePressAfterFoldCollapseMaximizes(t *testing.T) {
	ms, mock := newTestMasterStack()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 3)
	mock.Tree = ws
	ms.ArrangeAll(ws)

	if err := ms.Command("maximize", ws); err != nil {
		t.Fatalf("maximize: %v", err)
	}
	// Collapse to one window, then grow back to three.
	for _, n := range []*sway.Node{ws.Nodes[2], ws.Nodes[1]} {
		if err := ms.WindowRemoved(ws, n); err != nil {
			t.Fatalf("WindowRemoved: %v", err)
		}
	}
	for _, n := range []*sway.Node{ws.Nodes[1], ws.Nodes[2]} {
		if err := ms.WindowAdded(ws, n); err != nil {
			t.Fatalf("WindowAdded: %v", err)
		}
	}
	if got := len(ms.WindowIDs()); got != 3 {
		t.Fatalf("tracked %d windows, want 3 (%v)", got, ms.WindowIDs())
	}

	mock.ClearCommands()
	if err := ms.Command("maximize", ws); err != nil {
		t.Fatalf("maximize press: %v", err)
	}

	if !ms.Maximized() {
		t.Errorf("pressing maximize un-maximized instead of maximizing — the stale flag "+
			"made the toggle run backwards; commands=%v", mock.Commands)
	}
	if len(mock.CommandsMatching("layout tabbed")) == 0 {
		t.Errorf("no `layout tabbed` issued, so nothing was folded; commands=%v", mock.Commands)
	}
}
