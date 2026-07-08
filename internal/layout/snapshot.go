package layout

import (
	"encoding/json"
	"time"
)

// LayoutSnapshot captures the actual window arrangement on a workspace
// at a point in time. Combined with a LayoutSpec, it can restore the layout.
//
// Snapshots are keyed by slot ID (not role name) for stability.
type LayoutSnapshot struct {
	// SpecName identifies which LayoutSpec this snapshot was captured from.
	SpecName string `json:"spec_name"`

	// Workspace is the workspace name/number.
	Workspace string `json:"workspace"`

	// Slots maps slot node IDs to the windows placed in each slot.
	Slots map[string][]WindowInfo `json:"slots"`

	// CapturedAt is when this snapshot was taken.
	CapturedAt time.Time `json:"captured_at"`
}

// WindowInfo describes a window at capture time, with enough information
// to re-identify it on restore.
type WindowInfo struct {
	// AppID is the Wayland app_id (or X11 window class).
	AppID string `json:"app_id"`

	// Title is the window title at capture time.
	// Used as a weak matching hint — titles change frequently.
	Title string `json:"title,omitempty"`

	// Marks are sway marks set on this window.
	Marks []string `json:"marks,omitempty"`

	// InstanceID is a stable identifier assigned by the session manager.
	// This is the strongest matching signal — if set, it should be preferred.
	InstanceID string `json:"instance_id,omitempty"`

	// Focused indicates this window had focus at capture time.
	Focused bool `json:"focused,omitempty"`
}

// WindowMatcher defines criteria for matching a window during layout restore.
// Fields are tried in priority order: InstanceID > Marks > AppID+TitleRegex.
type WindowMatcher struct {
	// InstanceID matches the session-manager-assigned app instance.
	// Strongest signal — unique across the session.
	InstanceID string `json:"instance_id,omitempty"`

	// AppID matches the Wayland app_id or X11 window class.
	AppID string `json:"app_id,omitempty"`

	// WMClass matches the X11 WM_CLASS (for XWayland apps).
	WMClass string `json:"wm_class,omitempty"`

	// TitleRegex is a regex pattern matched against the window title.
	// Weak signal — use only as a tiebreaker.
	TitleRegex string `json:"title_regex,omitempty"`

	// Marks matches windows that have any of these sway marks.
	Marks []string `json:"marks,omitempty"`

	// Ordinal disambiguates when multiple windows match the same criteria.
	// E.g., ordinal=2 means "the second matching window".
	Ordinal int `json:"ordinal,omitempty"`
}

// SlotAssignment pairs a slot ID with a matcher, used in restore rules
// and declarative placement configs.
type SlotAssignment struct {
	// SlotID is the target slot node ID.
	SlotID string `json:"slot_id"`

	// Matcher defines which window(s) should go in this slot.
	Matcher WindowMatcher `json:"matcher"`
}

// CaptureSnapshot creates a LayoutSnapshot from a spec and the current window state.
// It assigns windows to slots based on their current positions.
func CaptureSnapshot(spec *LayoutSpec, workspace string, slotWindows map[string][]WindowInfo) *LayoutSnapshot {
	return &LayoutSnapshot{
		SpecName:   spec.Name,
		Workspace:  workspace,
		Slots:      slotWindows,
		CapturedAt: time.Now(),
	}
}

// MarshalJSON produces the JSON representation of a LayoutSnapshot.
func (s *LayoutSnapshot) MarshalJSON() ([]byte, error) {
	type Alias LayoutSnapshot
	return json.Marshal((*Alias)(s))
}

// UnmarshalJSON parses a LayoutSnapshot from JSON.
func (s *LayoutSnapshot) UnmarshalJSON(data []byte) error {
	type Alias LayoutSnapshot
	return json.Unmarshal(data, (*Alias)(s))
}
