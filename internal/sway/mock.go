package sway

import (
	"fmt"
	"sync"
)

// Mock is a test double for the sway Client interface.
//
// It records all commands sent via RunCommand for later assertion,
// returns a configurable tree from GetTree, and supports setting
// command results (success/failure).
//
// Usage:
//
//	mock := sway.NewMock()
//	mock.Tree = sway.CreateWorkspace("1", 3)
//	engine.Apply(spec, mock)
//	assert.Contains(t, mock.Commands, `resize set width 50 ppt`)
type Mock struct {
	mu sync.Mutex

	// Tree is returned by GetTree. Set this before calling code under test.
	Tree *Node

	// Commands records every command string passed to RunCommand, in order.
	Commands []string

	// CommandError, if non-nil, is returned by RunCommand for every call.
	CommandError error

	// CommandCallback, if set, is called for each RunCommand. If it returns
	// a non-nil error, that error is used (overrides CommandError).
	CommandCallback func(cmd string) error

	// WorkspaceList is returned by GetWorkspaces.
	WorkspaceList []Workspace

	// Events is a list of events to be delivered when Subscribe is called.
	Events []Event
}

// NewMock creates a Mock with an empty root tree.
func NewMock() *Mock {
	return &Mock{
		Tree: &Node{Type: "root"},
	}
}

func (m *Mock) GetTree() (*Node, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Tree, nil
}

func (m *Mock) RunCommand(cmd string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Commands = append(m.Commands, cmd)

	if m.CommandCallback != nil {
		if err := m.CommandCallback(cmd); err != nil {
			return err
		}
	}
	return m.CommandError
}

func (m *Mock) Subscribe(eventTypes []string, handler EventHandler) error {
	// Deliver any queued events synchronously (useful for unit tests).
	for _, ev := range m.Events {
		handler(ev)
	}
	return nil
}

func (m *Mock) GetWorkspaces() ([]Workspace, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.WorkspaceList, nil
}

// ClearCommands resets the recorded command list.
func (m *Mock) ClearCommands() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Commands = nil
}

// HasCommand returns true if the given command was recorded.
func (m *Mock) HasCommand(cmd string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.Commands {
		if c == cmd {
			return true
		}
	}
	return false
}

// CommandCount returns how many times RunCommand was called.
func (m *Mock) CommandCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Commands)
}

// LastCommand returns the most recently recorded command, or empty string.
func (m *Mock) LastCommand() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.Commands) == 0 {
		return ""
	}
	return m.Commands[len(m.Commands)-1]
}

// CommandsMatching returns all recorded commands that contain substr.
func (m *Mock) CommandsMatching(substr string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []string
	for _, c := range m.Commands {
		if contains(c, substr) {
			result = append(result, c)
		}
	}
	return result
}

func contains(s, substr string) bool {
	return len(substr) == 0 || len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Verify interface compliance at compile time.
var _ Client = (*Mock)(nil)

// --- Factory functions for building test trees ---

var nextID int64 = 100

// ResetIDCounter resets the auto-incrementing ID counter used by factory
// functions. Call this at the start of tests that care about specific IDs.
func ResetIDCounter() {
	nextID = 100
}

func allocID() int64 {
	id := nextID
	nextID++
	return id
}

// CreateWindow creates a leaf container node.
func CreateWindow(name string) *Node {
	return &Node{
		ID:   allocID(),
		Name: name,
		Type: "con",
		Rect: Rect{Width: 800, Height: 600},
	}
}

// CreateWindowWithAppID creates a window with a specific app_id.
func CreateWindowWithAppID(name, appID string) *Node {
	n := CreateWindow(name)
	n.AppID = appID
	return n
}

// CreateFloatingWindow creates a floating container node.
func CreateFloatingWindow(name string) *Node {
	return &Node{
		ID:       allocID(),
		Name:     name,
		Type:     "floating_con",
		Floating: "auto_on",
		Rect:     Rect{Width: 400, Height: 300},
	}
}

// CreateWorkspace builds a workspace node with N tiled windows and
// optional floating windows. Windows are named "Window1", "Window2", etc.
func CreateWorkspace(name string, windowCount int, floatingCount ...int) *Node {
	var windows []*Node
	for i := range windowCount {
		windows = append(windows, CreateWindow(fmt.Sprintf("Window%d", i+1)))
	}

	var floating []*Node
	fc := 0
	if len(floatingCount) > 0 {
		fc = floatingCount[0]
	}
	for i := range fc {
		floating = append(floating, CreateFloatingWindow(fmt.Sprintf("Floating%d", i+1)))
	}

	ws := &Node{
		ID:            allocID(),
		Name:          name,
		Type:          "workspace",
		Layout:        "splith",
		Rect:          Rect{Width: 2560, Height: 1440},
		Nodes:         windows,
		FloatingNodes: floating,
	}
	ws.SetParents()
	return ws
}

// WorkspaceSpec describes a workspace for CreateTree.
type WorkspaceSpec struct {
	Name          string
	WindowCount   int
	FloatingCount int
}

// CreateTree builds a complete sway tree (root → output → workspaces).
func CreateTree(specs ...WorkspaceSpec) *Node {
	var workspaces []*Node
	for _, spec := range specs {
		workspaces = append(workspaces, CreateWorkspace(
			spec.Name, spec.WindowCount, spec.FloatingCount,
		))
	}

	output := &Node{
		ID:    allocID(),
		Name:  "eDP-1",
		Type:  "output",
		Nodes: workspaces,
	}

	root := &Node{
		ID:    allocID(),
		Type:  "root",
		Nodes: []*Node{output},
	}
	root.SetParents()
	return root
}

// CreateWindowEvent creates a window Event for testing.
func CreateWindowEvent(change string, container *Node) Event {
	return Event{
		Type:      "window",
		Change:    change,
		Container: container,
	}
}

// CreateBindingEvent creates a binding Event for testing.
func CreateBindingEvent(command string) Event {
	return Event{
		Type:   "binding",
		Change: "run",
		Binding: &Binding{
			Command: command,
		},
	}
}

// CreateWorkspaceEvent creates a workspace Event for testing.
func CreateWorkspaceEvent(change string, workspace *Node) Event {
	return Event{
		Type:      "workspace",
		Change:    change,
		Workspace: workspace,
	}
}
