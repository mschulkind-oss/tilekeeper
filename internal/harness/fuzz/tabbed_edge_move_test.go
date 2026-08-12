package fuzz

import (
	"testing"

	"github.com/mschulkind-oss/tilekeeper/internal/harness/sim"
	"github.com/mschulkind-oss/tilekeeper/internal/sway"
	"github.com/mschulkind-oss/tilekeeper/internal/workspace"
)

// The ws8 report from 2026-08-12:
//
//	"on workspace 8, the tabbed workspace, if I `move left` a window, once
//	 it moves left from the first tab position it becomes its own window
//	 that takes half the screen and leaves the tabs. Same thing to the
//	 right. I want it to just stop moving when it gets to first/last."
//
// Tabbed forwarded `move left` / `move right` to sway verbatim. Inside the
// strip that reorders tabs, but at an EDGE sway does not stop:
// container_move_in_direction (sway/commands/move.c:301-413) walks up the
// ancestor chain and PROMOTES the container out of its parent, so the
// workspace ends up as [escapee, tab strip] with the escapee tiled beside
// the tabs at half width. Nothing repairs it either — the hub ignores
// same-workspace window::move events, so the broken shape persists until
// the next window opens.
//
// Two moves reach that promotion from a healthy strip, and both are covered:
//
//   - HORIZONTAL at an edge (TestTabbedEdgeMoveKeepsStrip): the first tab
//     moving left, or the last moving right, has no sibling that way, so the
//     window is promoted out of the strip container.
//   - VERTICAL from anywhere (TestTabbedPerpendicularMoveKeepsStrip): a tab
//     strip has no vertical axis, so sway re-orients the WHOLE workspace
//     (workspace_wrap_children + workspace layout splitv) and promotes the
//     window out of the fresh wrapper. Same visible damage, from any tab.
//
// The fuzzer could not see any of this before: the sim's moveDir was
// intra-parent only and silently no-opped at a parent edge. It now models
// sway's promotion, and tabbed-strip-flat names the property being broken.

func TestTabbedEdgeMoveKeepsStrip(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tabIdx  int // negative counts from the end
		command string
	}{
		{"first tab moves left", 0, "move left"},
		{"last tab moves right", -1, "move right"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, hub, state := newTabbedStrip(t, 3)
			strip := tabStripOf(t, s, "8")

			idx := tc.tabIdx
			if idx < 0 {
				idx += len(strip.Nodes)
			}
			clearAllFocus(state.root)
			strip.Nodes[idx].Focused = true
			want := leafIDsOf(strip)

			ev := sway.Event{Type: "binding", Binding: &sway.Binding{
				Command: "nop tilekeeper " + tc.command + " workspace 8",
			}}
			hub.HandleEvent(ev)

			after := tabStripOf(t, s, "8")
			if got := leafIDsOf(after); len(got) != len(want) {
				tree, _ := s.GetTree()
				t.Errorf("strip holds %v after %q, want all of %v\n%s",
					got, tc.command, want, dumpTreeStr(tree))
			}
			assertNoStripViolation(t, hub, s, ev)
		})
	}
}

func TestTabbedPerpendicularMoveKeepsStrip(t *testing.T) {
	for _, cmd := range []string{"move up", "move down"} {
		t.Run(cmd, func(t *testing.T) {
			s, hub, state := newTabbedStrip(t, 3)
			strip := tabStripOf(t, s, "8")

			clearAllFocus(state.root)
			strip.Nodes[1].Focused = true
			want := leafIDsOf(strip)

			ev := sway.Event{Type: "binding", Binding: &sway.Binding{
				Command: "nop tilekeeper " + cmd + " workspace 8",
			}}
			hub.HandleEvent(ev)

			tree, _ := s.GetTree()
			ws := findWorkspace(tree, "8")
			if ws.Layout != "splith" {
				t.Errorf("ws8 layout=%q after %q, want \"splith\" — sway re-oriented the workspace\n%s",
					ws.Layout, cmd, dumpTreeStr(tree))
			}
			if got := leafIDsOf(tabStripOf(t, s, "8")); len(got) != len(want) {
				t.Errorf("strip holds %v after %q, want all of %v\n%s",
					got, cmd, want, dumpTreeStr(tree))
			}
			assertNoStripViolation(t, hub, s, ev)
		})
	}
}

// TestTabbedInteriorMoveStillReorders is the other half of the clamp: a tab
// that is NOT at an edge must still swap with its neighbour, because that is
// the whole point of `move left` / `move right` on a tab strip. A "fix" that
// simply stopped forwarding directional moves would pass the tests above and
// silently break tab reordering.
func TestTabbedInteriorMoveStillReorders(t *testing.T) {
	s, hub, state := newTabbedStrip(t, 3)
	strip := tabStripOf(t, s, "8")

	first, middle := strip.Nodes[0].ID, strip.Nodes[1].ID
	clearAllFocus(state.root)
	strip.Nodes[1].Focused = true

	hub.HandleEvent(sway.Event{Type: "binding", Binding: &sway.Binding{
		Command: "nop tilekeeper move left workspace 8",
	}})

	after := tabStripOf(t, s, "8")
	if len(after.Nodes) != 3 {
		tree, _ := s.GetTree()
		t.Fatalf("strip has %d tabs, want 3\n%s", len(after.Nodes), dumpTreeStr(tree))
	}
	if after.Nodes[0].ID != middle || after.Nodes[1].ID != first {
		t.Errorf("tabs did not swap: got [%d %d ...], want [%d %d ...]",
			after.Nodes[0].ID, after.Nodes[1].ID, middle, first)
	}
}

// newTabbedStrip builds a hub with ws8 tabbed (ws9 MasterStack, unused here)
// and opens n windows on ws8, returning the sim, hub, and fuzz state.
func newTabbedStrip(t *testing.T, n int) (*sim.SimSwayClient, *workspace.Hub, *fuzzState) {
	t.Helper()
	s := sim.New()
	hub := newTabbedPlusMasterHub(s)
	state := newFuzzState([]string{"8", "9"})
	hub.HandleEvent(state.initWorkspace(s, "8"))
	hub.HandleEvent(state.initWorkspace(s, "9"))
	for range n {
		hub.HandleEvent(one(state.genNew(s, state.workspaces["8"], 100))[0])
	}
	if strip := tabStripOf(t, s, "8"); len(strip.Nodes) != n {
		tree, _ := s.GetTree()
		t.Fatalf("setup: ws8 should start as a %d-tab strip\n%s", n, dumpTreeStr(tree))
	}
	return s, hub, state
}

// tabStripOf returns the workspace's tab strip and asserts the shape Tabbed
// maintains: one tiled container holding every window, tabbed, directly under
// the workspace. (A workspace container that is itself tabbed also counts —
// see chooseStrip — but that is not what `layout tabbed` builds.)
func tabStripOf(t *testing.T, s *sim.SimSwayClient, wsName string) *sway.Node {
	t.Helper()
	tree, _ := s.GetTree()
	ws := findWorkspace(tree, wsName)
	if ws == nil {
		t.Fatalf("workspace %s not found\n%s", wsName, dumpTreeStr(tree))
	}
	strip := ws
	if ws.Layout != "tabbed" && ws.Layout != "stacked" {
		if len(ws.Nodes) != 1 {
			t.Fatalf("ws%s has %d tiled children, want 1 (the tab strip)\n%s",
				wsName, len(ws.Nodes), dumpTreeStr(tree))
		}
		strip = ws.Nodes[0]
	}
	if strip.Layout != "tabbed" {
		t.Fatalf("ws%s strip layout=%q, want \"tabbed\"\n%s", wsName, strip.Layout, dumpTreeStr(tree))
	}
	for _, leaf := range strip.Leaves() {
		if leaf.Parent != strip {
			t.Fatalf("leaf %d is not a direct tab of the strip (parent=%d/%s)\n%s",
				leaf.ID, leaf.Parent.ID, leaf.Parent.Layout, dumpTreeStr(tree))
		}
	}
	return strip
}

// assertNoStripViolation runs the shared invariant battery and fails on any
// tabbed-strip-flat violation, so the structural assertions above and the
// fuzzer's own checker cannot drift apart.
func assertNoStripViolation(t *testing.T, hub *workspace.Hub, s *sim.SimSwayClient, ev sway.Event) {
	t.Helper()
	res := &Result{}
	CheckStep(hub, s, []string{"8"}, ev, 1, res)
	for _, v := range res.Violations {
		if v.Invariant == "tabbed-strip-flat" {
			t.Errorf("tabbed-strip-flat violated: %s", v.Detail)
		}
	}
}

func leafIDsOf(n *sway.Node) []int64 {
	var out []int64
	for _, l := range n.Leaves() {
		out = append(out, l.ID)
	}
	return out
}
