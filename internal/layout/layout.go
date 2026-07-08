// Package layout defines the data model and interfaces for workspace layout management.
//
// See spec.go for the declarative layout model and snapshot.go for capture/restore.
package layout

import (
	"fmt"

	"github.com/mschulkind-oss/tilekeeper/internal/sway"
)

// Manager manages the window layout for a single workspace.
//
// Each workspace gets its own Manager instance. Implementations are
// stateful and event-driven — they track window order, issue sway
// commands, and respond to user commands.
//
// Implementations:
//   - MasterStack: classic master-stack tiling (masterstack.go)
//   - Tabbed: full-workspace tabbed mode (tabbed.go)
type Manager interface {
	// Name returns the layout type name (e.g., "MasterStack").
	Name() string

	// WindowAdded handles a new tiling window appearing on this workspace.
	WindowAdded(ws *sway.Node, window *sway.Node) error

	// WindowRemoved handles a window being closed or moved away.
	WindowRemoved(ws *sway.Node, window *sway.Node) error

	// WindowFocused handles a window gaining focus on this workspace.
	WindowFocused(ws *sway.Node, window *sway.Node) error

	// Command handles a user command (e.g., "swap-master", "rotate cw").
	Command(cmd string, ws *sway.Node) error

	// ArrangeAll rebuilds the entire layout from scratch.
	ArrangeAll(ws *sway.Node) error

	// WindowIDs returns the ordered window ID list.
	WindowIDs() []int64
}

// IsExcluded returns true if a window should be excluded from layout management.
//
// Excluded windows: nil, non-con type, floating, fullscreen, or inside
// tabbed/stacking parent containers.
func IsExcluded(window *sway.Node) bool {
	if window == nil {
		return true
	}
	if window.Type != "con" {
		return true
	}
	if window.IsFloating() || window.Type == "floating_con" {
		return true
	}
	if window.FullscreenMode == 1 {
		return true
	}
	if window.Parent != nil {
		pl := window.Parent.Layout
		if pl == "stacked" || pl == "tabbed" {
			return true
		}
	}
	return false
}

// FlattenSingletons issues `[con_id=<child>] split none` for every
// singleton structural con in the workspace's tiled subtree. Used on
// layout-transition so wrappers left behind by the outgoing manager
// (MasterStack's splitv / stacked substack) don't persist as orphan
// chains under the new manager.
func FlattenSingletons(conn sway.Client, ws *sway.Node) {
	if conn == nil || ws == nil {
		return
	}
	var targets []int64
	var walk func(n *sway.Node)
	walk = func(n *sway.Node) {
		if n.Type == "con" && len(n.Nodes) == 1 {
			targets = append(targets, n.Nodes[0].ID)
		}
		for _, child := range n.Nodes {
			walk(child)
		}
	}
	for _, child := range ws.Nodes {
		walk(child)
	}
	for _, id := range targets {
		_ = conn.RunCommand(fmt.Sprintf("[con_id=%d] split none", id))
	}
}

// TilingLeaves returns only the tiling (non-floating) leaf windows in a workspace.
func TilingLeaves(ws *sway.Node) []*sway.Node {
	if ws == nil {
		return nil
	}
	return ws.Leaves()
}

// FloatingLeaves returns only the floating leaf windows in a workspace.
func FloatingLeaves(ws *sway.Node) []*sway.Node {
	if ws == nil {
		return nil
	}
	var result []*sway.Node
	for _, n := range ws.FloatingNodes {
		// Floating containers are typed "floating_con" — they are
		// either the window itself or wrap child "con" nodes.
		if len(n.Nodes) == 0 {
			result = append(result, n)
		} else {
			result = append(result, n.Leaves()...)
		}
	}
	return result
}

// WorkspaceState represents the live state of a workspace from sway's tree.
type WorkspaceState struct {
	// Name is the workspace name/number.
	Name string

	// Windows is the ordered list of tiling windows on this workspace.
	Windows []Window

	// FloatingWindows are windows in floating mode (excluded from layout).
	FloatingWindows []Window

	// Width and Height of the workspace output in pixels.
	Width  int
	Height int
}

// Window represents a single window/container in a workspace.
type Window struct {
	// ID is the sway container ID.
	ID int64

	// AppID is the Wayland app_id (or X11 window class).
	AppID string

	// WMClass is the X11 WM_CLASS (for XWayland apps).
	WMClass string

	// Title is the current window title.
	Title string

	// Marks are sway marks set on this window.
	Marks []string

	// InstanceID is a stable identifier from the session manager (if set).
	InstanceID string

	// Focused indicates whether this window currently has focus.
	Focused bool

	// Rect is the window's current geometry.
	Rect Rect
}

// Rect is a window's position and dimensions.
type Rect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}
