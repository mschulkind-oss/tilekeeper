package daemon

import (
	"testing"

	"github.com/mschulkind-oss/tilekeeper/internal/sway"
)

// TestShouldDispatch documents which sway events the daemon's subscribe
// layer accepts vs. drops at source. Source-dropping high-volume no-op
// events (window::mark, window::title, window::urgent) is one of the
// preventions against the bounded eventCh overflowing during a burst —
// MasterStack's own mark+unmark pairs generate 2× window::mark events
// per repositioning, and were the bulk of the 2026-05-22 ws7 drop storm.
//
// Allow-list, not deny-list: any new sway event type that we don't yet
// handle must default to dropped. Add it here AND to handleWindowEvent
// (or the equivalent) in the same PR.
func TestShouldDispatch(t *testing.T) {
	cases := []struct {
		name string
		ev   sway.Event
		want bool
	}{
		// Window events the hub reacts to.
		{"window:new", sway.Event{Type: "window", Change: "new"}, true},
		{"window:close", sway.Event{Type: "window", Change: "close"}, true},
		{"window:focus", sway.Event{Type: "window", Change: "focus"}, true},
		{"window:floating", sway.Event{Type: "window", Change: "floating"}, true},
		{"window:move", sway.Event{Type: "window", Change: "move"}, true},

		// Window events the hub does NOT react to — source-drop.
		{"window:mark dropped", sway.Event{Type: "window", Change: "mark"}, false},
		{"window:title dropped", sway.Event{Type: "window", Change: "title"}, false},
		{"window:urgent dropped", sway.Event{Type: "window", Change: "urgent"}, false},
		{"window:fullscreen_mode dropped",
			sway.Event{Type: "window", Change: "fullscreen_mode"}, false},

		// Workspace events.
		{"workspace:init", sway.Event{Type: "workspace", Change: "init"}, true},
		{"workspace:focus", sway.Event{Type: "workspace", Change: "focus"}, true},
		{"workspace:empty dropped",
			sway.Event{Type: "workspace", Change: "empty"}, false},
		{"workspace:rename dropped",
			sway.Event{Type: "workspace", Change: "rename"}, false},

		// Binding events — all dispatched (ParseNopCommand filters
		// further downstream).
		{"binding:run", sway.Event{Type: "binding", Change: "run"}, true},

		// Unknown type.
		{"unknown type dropped",
			sway.Event{Type: "tick", Change: "anything"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldDispatch(tc.ev); got != tc.want {
				t.Errorf("shouldDispatch(%+v) = %v, want %v", tc.ev, got, tc.want)
			}
		})
	}
}
