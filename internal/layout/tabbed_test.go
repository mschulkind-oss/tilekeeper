package layout

import (
	"testing"

	"github.com/mschulkind-oss/tilekeeper/internal/sway"
)

func TestTabbedSetsLayoutOnArrange(t *testing.T) {
	mock := sway.NewMock()
	tb := NewTabbed(mock)
	ws := &sway.Node{ID: 1, Type: "workspace", Name: "8", Layout: "splith"}

	if err := tb.ArrangeAll(ws); err != nil {
		t.Fatalf("ArrangeAll: %v", err)
	}
	if !mock.HasCommand(`[workspace=8] layout tabbed`) {
		t.Errorf("expected `[workspace=8] layout tabbed`, got %v", mock.Commands)
	}
}

func TestTabbedNoOpWhenAlreadyTabbed(t *testing.T) {
	mock := sway.NewMock()
	tb := NewTabbed(mock)
	ws := &sway.Node{ID: 1, Type: "workspace", Name: "8", Layout: "tabbed"}

	if err := tb.ArrangeAll(ws); err != nil {
		t.Fatalf("ArrangeAll: %v", err)
	}
	if mock.CommandCount() != 0 {
		t.Errorf("expected no commands when already tabbed, got %v", mock.Commands)
	}
}

func TestTabbedWindowAddedSetsLayout(t *testing.T) {
	mock := sway.NewMock()
	tb := NewTabbed(mock)
	ws := &sway.Node{ID: 1, Type: "workspace", Name: "8", Layout: "splith"}
	win := &sway.Node{ID: 100, Type: "con", Parent: ws}

	if err := tb.WindowAdded(ws, win); err != nil {
		t.Fatalf("WindowAdded: %v", err)
	}
	if !mock.HasCommand(`[workspace=8] layout tabbed`) {
		t.Errorf("expected layout tabbed command on window-added, got %v", mock.Commands)
	}
}

func TestTabbedCommandIsNoOp(t *testing.T) {
	mock := sway.NewMock()
	tb := NewTabbed(mock)
	ws := &sway.Node{ID: 1, Type: "workspace", Name: "8", Layout: "tabbed"}

	// Tabbed deliberately ignores user commands — sway's own bindings
	// cover navigation. Returning an error would surface as a log noise
	// for every binding event the user sends to a tabbed workspace.
	for _, cmd := range []string{"swap-master", "focus master", "stack toggle"} {
		if err := tb.Command(cmd, ws); err != nil {
			t.Errorf("Command(%q) returned %v, want nil", cmd, err)
		}
	}
}

// TestTabbedSkipsNavOnEmptyWorkspace checks that Tabbed does not forward
// `move left/right/up/down` to sway when the workspace has no windows.
// Real sway returns CMD_FAILURE ("Cannot move workspaces in a direction")
// — forwarding it would surface as a spurious command failure in tilekeeper's logs.
// Fuzzer seed=10 step=99 caught this against an empty workspace after a
// rapid close sequence.
func TestTabbedSkipsNavOnEmptyWorkspace(t *testing.T) {
	mock := sway.NewMock()
	tb := NewTabbed(mock)
	ws := &sway.Node{ID: 1, Type: "workspace", Name: "7", Layout: "tabbed"}
	// No child windows.

	for _, cmd := range []string{"move left", "move right", "move up", "move down",
		"focus left", "focus right", "focus up", "focus down"} {
		if err := tb.Command(cmd, ws); err != nil {
			t.Errorf("Command(%q) on empty ws returned %v, want nil", cmd, err)
		}
	}
	if mock.CommandCount() != 0 {
		t.Errorf("expected no commands on empty ws, got %v", mock.Commands)
	}
}

func TestTabbedForwardsFocusWhenWorkspaceHasWindow(t *testing.T) {
	mock := sway.NewMock()
	tb := NewTabbed(mock)
	ws, _ := tabStrip("7", 1)
	ws.Nodes[0].Focused = true

	if err := tb.Command("focus left", ws); err != nil {
		t.Fatalf("Command: %v", err)
	}
	if !mock.HasCommand("focus left") {
		t.Errorf("expected `focus left` forwarded when ws has a window, got %v", mock.Commands)
	}
}

// TestTabbedClampsMoveToStrip is the command-level half of the ws8 fix
// (2026-08-12): a tab at the end of the strip must NOT have its `move`
// forwarded, because sway does not stop there — it promotes the window out
// of the tab container and it ends up tiled beside the tabs. Interior tabs
// must still be forwarded, since reordering tabs is the point.
//
// Structural proof against a simulated sway tree lives in
// internal/harness/fuzz/tabbed_edge_move_test.go; this pins the decision
// itself, which is the part production owns.
func TestTabbedClampsMoveToStrip(t *testing.T) {
	tests := []struct {
		name    string
		focus   int // index of the focused tab
		cmd     string
		forward bool
	}{
		{"first tab moving left stops", 0, "move left", false},
		{"first tab moving right reorders", 0, "move right", true},
		{"middle tab moving left reorders", 1, "move left", true},
		{"middle tab moving right reorders", 1, "move right", true},
		{"last tab moving right stops", 2, "move right", false},
		{"last tab moving left reorders", 2, "move left", true},
		// A tab strip has no vertical axis, so up/down can only tear the
		// window out (sway re-orients the whole workspace to do it).
		{"vertical move stops", 1, "move up", false},
		{"vertical move stops (down)", 1, "move down", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := sway.NewMock()
			tb := NewTabbed(mock)
			ws, tabs := tabStrip("8", 3)
			tabs[tc.focus].Focused = true

			if err := tb.Command(tc.cmd, ws); err != nil {
				t.Fatalf("Command(%q): %v", tc.cmd, err)
			}
			if got := mock.HasCommand(tc.cmd); got != tc.forward {
				t.Errorf("Command(%q) with tab %d focused: forwarded=%v, want %v (commands=%v)",
					tc.cmd, tc.focus, got, tc.forward, mock.Commands)
			}
		})
	}
}

// TestTabbedClampsMoveInStripContainer covers the other tree shape the bug
// report can be in: the tabs live in a tabbed CONTAINER under a splith
// workspace (what a plain `layout tabbed` binding builds via
// workspace_wrap_children), not in the workspace container itself. The clamp
// reads the focused window's own parent, so both shapes behave the same.
func TestTabbedClampsMoveInStripContainer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		focus   int
		cmd     string
		forward bool
	}{
		{"first tab moving left stops", 0, "move left", false},
		{"last tab moving right stops", 2, "move right", false},
		{"middle tab reorders", 1, "move left", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := sway.NewMock()
			tb := NewTabbed(mock)
			ws := &sway.Node{ID: 1, Type: "workspace", Name: "8", Layout: "splith"}
			strip := &sway.Node{ID: 2, Type: "con", Layout: "tabbed", Parent: ws}
			ws.Nodes = []*sway.Node{strip}
			for i := range 3 {
				strip.Nodes = append(strip.Nodes,
					&sway.Node{ID: int64(100 + i), Type: "con", Parent: strip})
			}
			strip.Nodes[tc.focus].Focused = true

			if err := tb.Command(tc.cmd, ws); err != nil {
				t.Fatalf("Command(%q): %v", tc.cmd, err)
			}
			if got := mock.HasCommand(tc.cmd); got != tc.forward {
				t.Errorf("Command(%q) with tab %d focused: forwarded=%v, want %v (commands=%v)",
					tc.cmd, tc.focus, got, tc.forward, mock.Commands)
			}
		})
	}
}

// TestTabbedForwardsFloatingMove pins the floating exemption: sway moves a
// floating window by a pixel delta and never re-parents it, so a floating
// `move left` cannot break the strip and must keep working.
func TestTabbedForwardsFloatingMove(t *testing.T) {
	mock := sway.NewMock()
	tb := NewTabbed(mock)
	ws, _ := tabStrip("8", 2)
	float := &sway.Node{ID: 200, Type: "con", Floating: "user_on", Focused: true, Parent: ws}
	ws.FloatingNodes = []*sway.Node{float}

	if err := tb.Command("move left", ws); err != nil {
		t.Fatalf("Command: %v", err)
	}
	if !mock.HasCommand("move left") {
		t.Errorf("expected `move left` forwarded for a floating window, got %v", mock.Commands)
	}
}

// TestTabbedSkipsMoveWhenFocusIsElsewhere covers `nop tilekeeper move left
// workspace 8` fired while focus lives on another workspace: a bare `move`
// acts on whatever IS focused, which is never what the binding meant.
func TestTabbedSkipsMoveWhenFocusIsElsewhere(t *testing.T) {
	mock := sway.NewMock()
	tb := NewTabbed(mock)
	ws, _ := tabStrip("8", 3) // nothing focused

	if err := tb.Command("move left", ws); err != nil {
		t.Fatalf("Command: %v", err)
	}
	if mock.CommandCount() != 0 {
		t.Errorf("expected no commands when focus is off-workspace, got %v", mock.Commands)
	}
}

// tabStrip builds a workspace whose container IS the tab strip: layout
// tabbed with n leaf children, parents wired.
func tabStrip(name string, n int) (*sway.Node, []*sway.Node) {
	ws := &sway.Node{ID: 1, Type: "workspace", Name: name, Layout: "tabbed"}
	tabs := make([]*sway.Node, 0, n)
	for i := range n {
		tab := &sway.Node{ID: int64(100 + i), Type: "con", Parent: ws}
		ws.Nodes = append(ws.Nodes, tab)
		tabs = append(tabs, tab)
	}
	return ws, tabs
}
