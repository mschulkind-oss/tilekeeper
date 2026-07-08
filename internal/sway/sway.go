// Package sway provides a client for Sway's IPC protocol.
//
// The Client interface abstracts sway IPC so that layout engines and
// event handlers can be tested with a mock implementation that records
// commands and provides controllable tree structures.
package sway

// Client is the interface for communicating with Sway via IPC.
//
// Implementations:
//   - conn.go (planned): real IPC over $SWAYSOCK unix socket
//   - mock.go: test double that records commands
type Client interface {
	// GetTree returns the current container tree from sway.
	GetTree() (*Node, error)

	// RunCommand sends a command string to sway (e.g. "move left",
	// "resize set width 50 ppt"). Returns an error if the command fails.
	RunCommand(cmd string) error

	// Subscribe registers for sway events. The handler is called for
	// each event matching the given types (e.g. "window", "workspace",
	// "binding").
	Subscribe(eventTypes []string, handler EventHandler) error

	// GetWorkspaces returns the list of active workspaces.
	GetWorkspaces() ([]Workspace, error)
}

// EventHandler receives sway IPC events.
type EventHandler func(event Event)

// Event represents a sway IPC event.
type Event struct {
	Type   string // "window", "workspace", "binding"
	Change string // "new", "close", "focus", "move", "floating", "init", etc.

	// Seq is a daemon-assigned monotonic sequence number, set in the
	// subscribe callback before the event hits the consumer channel. It
	// gives the log a totally-ordered view of the event stream so a
	// dropped event leaves an explicit gap (seq=N, seq=N+2) rather than
	// an invisible hole. Zero for events constructed outside the daemon
	// (tests, fuzz, initial seeding).
	Seq int64

	// Container is set for window events.
	Container *Node

	// Workspace is set for workspace events.
	Workspace *Node

	// Binding is set for binding events.
	Binding *Binding
}

// Binding represents a sway keybinding from a binding event.
type Binding struct {
	Command string
}

// Workspace represents a sway workspace (from get_workspaces).
type Workspace struct {
	Num     int
	Name    string
	Focused bool
	Output  string
	Rect    Rect
}

// Node represents a container in sway's tree.
// This mirrors the sway IPC get_tree response.
type Node struct {
	ID             int64    `json:"id"`
	Name           string   `json:"name"`
	Type           string   `json:"type"`   // "root", "output", "workspace", "con", "floating_con"
	Layout         string   `json:"layout"` // "splith", "splitv", "tabbed", "stacking", "output"
	Rect           Rect     `json:"rect"`
	AppID          string   `json:"app_id"`
	WindowClass    string   `json:"window_class,omitempty"`
	Focused        bool     `json:"focused"`
	FullscreenMode int      `json:"fullscreen_mode"`
	Floating       string   `json:"floating,omitempty"` // "auto_on", "auto_off", "user_on", "user_off"
	Marks          []string `json:"marks"`
	Nodes          []*Node  `json:"nodes"`
	FloatingNodes  []*Node  `json:"floating_nodes"`

	// Percent is the share of the parent split this container occupies
	// (sway IPC: "percent"). null in JSON for root/output and just-created
	// nodes; sway uses (*float64)(nil) which we model as 0.
	Percent float64 `json:"percent,omitempty"`

	// Parent is set when building trees (not from JSON).
	Parent *Node `json:"-"`
}

// Rect is a rectangle with position and dimensions.
type Rect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// FindByID searches the tree for a node with the given ID.
func (n *Node) FindByID(id int64) *Node {
	if n.ID == id {
		return n
	}
	for _, child := range n.Nodes {
		if found := child.FindByID(id); found != nil {
			return found
		}
	}
	for _, child := range n.FloatingNodes {
		if found := child.FindByID(id); found != nil {
			return found
		}
	}
	return nil
}

// FindFocused returns the focused leaf node, or nil.
func (n *Node) FindFocused() *Node {
	if n.Focused && len(n.Nodes) == 0 {
		return n
	}
	for _, child := range n.Nodes {
		if found := child.FindFocused(); found != nil {
			return found
		}
	}
	for _, child := range n.FloatingNodes {
		if found := child.FindFocused(); found != nil {
			return found
		}
	}
	return nil
}

// Leaves returns all leaf (window) containers in the subtree.
func (n *Node) Leaves() []*Node {
	if len(n.Nodes) == 0 && n.Type == "con" {
		return []*Node{n}
	}
	var result []*Node
	for _, child := range n.Nodes {
		result = append(result, child.Leaves()...)
	}
	return result
}

// Workspaces returns all workspace nodes in the tree.
func (n *Node) Workspaces() []*Node {
	if n.Type == "workspace" {
		return []*Node{n}
	}
	var result []*Node
	for _, child := range n.Nodes {
		result = append(result, child.Workspaces()...)
	}
	return result
}

// IsFloating returns true if this is a floating container.
func (n *Node) IsFloating() bool {
	return n.Floating == "auto_on" || n.Floating == "user_on"
}

// Snapshot returns a detached copy of this node as it would appear in an
// IPC event payload: state frozen at call time, Parent=nil, no children.
// Real sway event containers are JSON snapshots taken at emission time
// (parseWindowEvent) — they can disagree with the live tree by the time
// the event is processed. Test harnesses must use Snapshot for event
// containers; passing live tree pointers hides the stale-payload race
// class (the 2026-06-12 ctrl-s portal-dialog incident).
func (n *Node) Snapshot() *Node {
	c := *n
	c.Parent = nil
	c.Nodes = nil
	c.FloatingNodes = nil
	c.Marks = append([]string(nil), n.Marks...)
	return &c
}

// SetParents recursively sets parent references throughout the tree.
func (n *Node) SetParents() {
	for _, child := range n.Nodes {
		child.Parent = n
		child.SetParents()
	}
	for _, child := range n.FloatingNodes {
		child.Parent = n
		child.SetParents()
	}
}

// FindWorkspace walks up the tree to find the workspace containing this node.
func (n *Node) FindWorkspace() *Node {
	if n.Type == "workspace" {
		return n
	}
	if n.Parent != nil {
		return n.Parent.FindWorkspace()
	}
	return nil
}
