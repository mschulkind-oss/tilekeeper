package sway

import (
	"fmt"
	"testing"
)

// --- Mock tests ---

func TestMockRecordsCommands(t *testing.T) {
	m := NewMock()
	if err := m.RunCommand("move left"); err != nil {
		t.Fatal(err)
	}
	if err := m.RunCommand("resize set width 50 ppt"); err != nil {
		t.Fatal(err)
	}

	if got := m.CommandCount(); got != 2 {
		t.Errorf("CommandCount = %d, want 2", got)
	}
	if !m.HasCommand("move left") {
		t.Error("missing 'move left'")
	}
	if m.LastCommand() != "resize set width 50 ppt" {
		t.Errorf("LastCommand = %q, want 'resize set width 50 ppt'", m.LastCommand())
	}
}

func TestMockCommandError(t *testing.T) {
	m := NewMock()
	m.CommandError = errTest
	if err := m.RunCommand("anything"); err == nil {
		t.Error("expected error from RunCommand")
	}
	// Command should still be recorded even on error
	if m.CommandCount() != 1 {
		t.Errorf("CommandCount = %d, want 1", m.CommandCount())
	}
}

var errTest = fmt.Errorf("test error")

func TestMockCommandCallback(t *testing.T) {
	m := NewMock()
	m.CommandCallback = func(cmd string) error {
		if cmd == "bad" {
			return errTest
		}
		return nil
	}

	if err := m.RunCommand("good"); err != nil {
		t.Errorf("unexpected error for 'good': %v", err)
	}
	if err := m.RunCommand("bad"); err == nil {
		t.Error("expected error for 'bad'")
	}
}

func TestMockClearCommands(t *testing.T) {
	m := NewMock()
	m.RunCommand("x")
	m.ClearCommands()
	if m.CommandCount() != 0 {
		t.Errorf("CommandCount after clear = %d, want 0", m.CommandCount())
	}
}

func TestMockCommandsMatching(t *testing.T) {
	m := NewMock()
	m.RunCommand("resize set width 50 ppt")
	m.RunCommand("move left")
	m.RunCommand("resize set height 30 ppt")

	resizes := m.CommandsMatching("resize")
	if len(resizes) != 2 {
		t.Errorf("got %d resize commands, want 2", len(resizes))
	}
}

func TestMockGetTree(t *testing.T) {
	m := NewMock()
	ws := CreateWorkspace("1", 3)
	m.Tree = ws

	tree, err := m.GetTree()
	if err != nil {
		t.Fatal(err)
	}
	if tree.Name != "1" {
		t.Errorf("tree.Name = %q, want '1'", tree.Name)
	}
}

func TestMockSubscribe(t *testing.T) {
	m := NewMock()
	win := CreateWindow("Test")
	m.Events = []Event{
		CreateWindowEvent("new", win),
		CreateWindowEvent("focus", win),
	}

	var received []string
	m.Subscribe([]string{"window"}, func(ev Event) {
		received = append(received, ev.Change)
	})

	if len(received) != 2 {
		t.Fatalf("received %d events, want 2", len(received))
	}
	if received[0] != "new" || received[1] != "focus" {
		t.Errorf("events = %v, want [new, focus]", received)
	}
}

// --- Tree factory tests ---

func TestCreateWorkspace(t *testing.T) {
	ResetIDCounter()
	ws := CreateWorkspace("1", 3, 1)

	if ws.Type != "workspace" {
		t.Errorf("Type = %q, want 'workspace'", ws.Type)
	}
	if ws.Name != "1" {
		t.Errorf("Name = %q, want '1'", ws.Name)
	}
	if len(ws.Nodes) != 3 {
		t.Errorf("got %d tiled windows, want 3", len(ws.Nodes))
	}
	if len(ws.FloatingNodes) != 1 {
		t.Errorf("got %d floating windows, want 1", len(ws.FloatingNodes))
	}

	// All children should have parent set
	for _, n := range ws.Nodes {
		if n.Parent != ws {
			t.Errorf("window %q parent not set", n.Name)
		}
	}

	// Leaves should return tiled windows only
	leaves := ws.Leaves()
	if len(leaves) != 3 {
		t.Errorf("Leaves() = %d, want 3", len(leaves))
	}
}

func TestCreateWorkspaceNoFloating(t *testing.T) {
	ws := CreateWorkspace("test", 2)
	if len(ws.FloatingNodes) != 0 {
		t.Errorf("expected 0 floating, got %d", len(ws.FloatingNodes))
	}
}

func TestCreateTree(t *testing.T) {
	ResetIDCounter()
	tree := CreateTree(
		WorkspaceSpec{Name: "1", WindowCount: 3},
		WorkspaceSpec{Name: "2", WindowCount: 2, FloatingCount: 1},
		WorkspaceSpec{Name: "coding", WindowCount: 1},
	)

	if tree.Type != "root" {
		t.Errorf("root Type = %q, want 'root'", tree.Type)
	}

	workspaces := tree.Workspaces()
	if len(workspaces) != 3 {
		t.Fatalf("got %d workspaces, want 3", len(workspaces))
	}

	if workspaces[0].Name != "1" {
		t.Errorf("workspace[0].Name = %q, want '1'", workspaces[0].Name)
	}
	if len(workspaces[0].Leaves()) != 3 {
		t.Errorf("workspace '1' leaves = %d, want 3", len(workspaces[0].Leaves()))
	}

	if workspaces[1].Name != "2" {
		t.Errorf("workspace[1].Name = %q, want '2'", workspaces[1].Name)
	}
	if len(workspaces[1].FloatingNodes) != 1 {
		t.Errorf("workspace '2' floating = %d, want 1", len(workspaces[1].FloatingNodes))
	}
}

func TestNodeFindByID(t *testing.T) {
	ResetIDCounter()
	tree := CreateTree(
		WorkspaceSpec{Name: "1", WindowCount: 2},
	)

	// Window1 gets ID 100, Window2 gets ID 101
	found := tree.FindByID(100)
	if found == nil {
		t.Fatal("FindByID(100) = nil")
	}
	if found.Name != "Window1" {
		t.Errorf("FindByID(100).Name = %q, want 'Window1'", found.Name)
	}

	if tree.FindByID(99999) != nil {
		t.Error("FindByID(99999) should be nil")
	}
}

func TestNodeFindFocused(t *testing.T) {
	ResetIDCounter()
	tree := CreateTree(
		WorkspaceSpec{Name: "1", WindowCount: 3},
	)

	// Nothing focused yet
	if tree.FindFocused() != nil {
		t.Error("expected no focused node")
	}

	// Focus a window
	ws := tree.Workspaces()[0]
	ws.Nodes[1].Focused = true

	focused := tree.FindFocused()
	if focused == nil {
		t.Fatal("FindFocused() = nil after setting focus")
	}
	if focused.Name != "Window2" {
		t.Errorf("focused = %q, want 'Window2'", focused.Name)
	}
}

func TestNodeFindWorkspace(t *testing.T) {
	ResetIDCounter()
	tree := CreateTree(
		WorkspaceSpec{Name: "1", WindowCount: 2},
	)

	window := tree.Workspaces()[0].Nodes[0]
	ws := window.FindWorkspace()
	if ws == nil {
		t.Fatal("FindWorkspace() = nil")
	}
	if ws.Name != "1" {
		t.Errorf("FindWorkspace().Name = %q, want '1'", ws.Name)
	}
}

func TestNodeIsFloating(t *testing.T) {
	regular := CreateWindow("regular")
	if regular.IsFloating() {
		t.Error("regular window should not be floating")
	}

	floating := CreateFloatingWindow("float")
	if !floating.IsFloating() {
		t.Error("floating window should be floating")
	}
}

func TestCreateWindowWithAppID(t *testing.T) {
	w := CreateWindowWithAppID("Firefox", "firefox")
	if w.AppID != "firefox" {
		t.Errorf("AppID = %q, want 'firefox'", w.AppID)
	}
}

func TestCreateEvents(t *testing.T) {
	win := CreateWindow("test")

	we := CreateWindowEvent("new", win)
	if we.Type != "window" || we.Change != "new" || we.Container != win {
		t.Error("window event not constructed correctly")
	}

	be := CreateBindingEvent("nop tilekeeper layout MasterStack")
	if be.Type != "binding" || be.Binding.Command != "nop tilekeeper layout MasterStack" {
		t.Error("binding event not constructed correctly")
	}

	ws := CreateWorkspace("1", 0)
	wse := CreateWorkspaceEvent("init", ws)
	if wse.Type != "workspace" || wse.Change != "init" || wse.Workspace != ws {
		t.Error("workspace event not constructed correctly")
	}
}
