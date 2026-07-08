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

func TestTabbedForwardsNavWhenWorkspaceHasWindow(t *testing.T) {
	mock := sway.NewMock()
	tb := NewTabbed(mock)
	ws := &sway.Node{ID: 1, Type: "workspace", Name: "7", Layout: "tabbed"}
	win := &sway.Node{ID: 100, Type: "con", Parent: ws}
	ws.Nodes = []*sway.Node{win}

	if err := tb.Command("move left", ws); err != nil {
		t.Fatalf("Command: %v", err)
	}
	if !mock.HasCommand("move left") {
		t.Errorf("expected `move left` forwarded when ws has a window, got %v", mock.Commands)
	}
}
