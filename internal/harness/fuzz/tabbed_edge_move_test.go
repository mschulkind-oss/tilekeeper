package fuzz

import (
	"fmt"
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
// Two tree shapes produce the report, and the tests below cover both:
//
//   - STRIP-IN-A-CONTAINER (TestTabbedEdgeMoveKeepsStripContainer). The tabs
//     live in a tabbed container under a splith workspace — what sway builds
//     when `layout tabbed` is run with a WINDOW in scope rather than the
//     workspace (cmd_layout → workspace_wrap_children). `move left` on the
//     first tab promotes it out beside the strip. This is the reported
//     symptom exactly.
//   - WORKSPACE-LEVEL STRIP (TestTabbedPerpendicularMoveKeepsStrip). The
//     workspace container itself is tabbed — what tilekeeper's ensure()
//     builds. Here left/right at the edge is a no-op on a single output
//     (sway falls through to "move to the next output"), but move UP/DOWN is
//     never parallel to a tab strip, so sway re-orients the WHOLE workspace
//     (workspace_wrap_children + workspace layout = splitv) and promotes the
//     window out of the freshly-made wrapper. Same visible damage.
//
// The fuzzer could not see any of this before: the sim's moveDir was
// intra-parent only and silently no-opped at a parent edge. It now models
// sway's promotion, and tabbed-strip-flat names the property being broken.

func TestTabbedEdgeMoveKeepsStripContainer(t *testing.T) {
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

			// Re-shape ws8 into strip-in-a-container, the way a plain sway
			// `bindsym $mod+w layout tabbed` binding does: with a WINDOW in
			// scope and a non-tabbed workspace, cmd_layout has no container
			// parent to re-layout, so it wraps every workspace child in a new
			// container and tabs THAT (workspace_wrap_children), leaving the
			// workspace itself splith.
			ws8 := state.workspaces["8"]
			for _, cmd := range []string{
				"[workspace=8] layout splith",
				fmt.Sprintf("[con_id=%d] layout tabbed", ws8.Nodes[0].ID),
			} {
				if err := s.RunCommand(cmd); err != nil {
					t.Fatalf("build strip-in-a-container (%s): %v", cmd, err)
				}
			}
			strip := ws8.Nodes[0]
			if strip.Layout != "tabbed" || len(strip.Nodes) != 3 {
				t.Fatalf("setup: want a 3-tab container, got %s with %d children\n%s",
					strip.Layout, len(strip.Nodes), dumpTreeStr(state.root))
			}

			idx := tc.tabIdx
			if idx < 0 {
				idx += len(strip.Nodes)
			}
			clearAllFocus(state.root)
			strip.Nodes[idx].Focused = true
			want := leafIDsOf(strip)

			hub.HandleEvent(sway.Event{Type: "binding", Binding: &sway.Binding{
				Command: "nop tilekeeper " + tc.command + " workspace 8",
			}})

			tree, _ := s.GetTree()
			ws := findWorkspace(tree, "8")
			if len(ws.Nodes) != 1 {
				t.Fatalf("a tab escaped the strip: ws8 now has %d children after %q\n%s",
					len(ws.Nodes), tc.command, dumpTreeStr(tree))
			}
			if got := leafIDsOf(ws.Nodes[0]); len(got) != len(want) {
				t.Errorf("strip holds %v after %q, want all of %v\n%s",
					got, tc.command, want, dumpTreeStr(tree))
			}
		})
	}
}

func TestTabbedPerpendicularMoveKeepsStrip(t *testing.T) {
	for _, cmd := range []string{"move up", "move down"} {
		t.Run(cmd, func(t *testing.T) {
			s, hub, state := newTabbedStrip(t, 3)

			ws8 := state.workspaces["8"]
			clearAllFocus(state.root)
			ws8.Nodes[1].Focused = true

			ev := sway.Event{Type: "binding", Binding: &sway.Binding{
				Command: "nop tilekeeper " + cmd + " workspace 8",
			}}
			hub.HandleEvent(ev)

			tree, _ := s.GetTree()
			ws := findWorkspace(tree, "8")
			if ws.Layout != "tabbed" {
				t.Errorf("ws8 layout=%q after %q, want \"tabbed\" — sway re-oriented the workspace\n%s",
					ws.Layout, cmd, dumpTreeStr(tree))
			}
			for _, leaf := range ws.Leaves() {
				if leaf.Parent != ws {
					t.Errorf("leaf %d escaped the tab strip (parent=%d/%s) after %q\n%s",
						leaf.ID, leaf.Parent.ID, leaf.Parent.Layout, cmd, dumpTreeStr(tree))
				}
			}

			// The invariant must agree with the structural assertions above.
			res := &Result{}
			CheckStep(hub, s, []string{"8"}, ev, 1, res)
			for _, v := range res.Violations {
				if v.Invariant == "tabbed-strip-flat" {
					t.Errorf("tabbed-strip-flat violated: %s", v.Detail)
				}
			}
		})
	}
}

// TestTabbedInteriorMoveStillReorders is the other half of the clamp: a tab
// that is NOT at an edge must still swap with its neighbor, because that is
// the whole point of `move left` / `move right` on a tab strip. A "fix" that
// simply stopped forwarding directional moves would pass the tests above and
// silently break tab reordering.
func TestTabbedInteriorMoveStillReorders(t *testing.T) {
	s, hub, state := newTabbedStrip(t, 3)

	ws8 := state.workspaces["8"]
	first, middle := ws8.Nodes[0].ID, ws8.Nodes[1].ID
	clearAllFocus(state.root)
	ws8.Nodes[1].Focused = true

	hub.HandleEvent(sway.Event{Type: "binding", Binding: &sway.Binding{
		Command: "nop tilekeeper move left workspace 8",
	}})

	tree, _ := s.GetTree()
	ws := findWorkspace(tree, "8")
	if len(ws.Nodes) != 3 {
		t.Fatalf("ws8 has %d children, want 3\n%s", len(ws.Nodes), dumpTreeStr(tree))
	}
	if ws.Nodes[0].ID != middle || ws.Nodes[1].ID != first {
		t.Errorf("tabs did not swap: got [%d %d ...], want [%d %d ...]",
			ws.Nodes[0].ID, ws.Nodes[1].ID, middle, first)
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
	tree, _ := s.GetTree()
	if ws := findWorkspace(tree, "8"); ws == nil || len(ws.Nodes) != n || ws.Layout != "tabbed" {
		t.Fatalf("setup: ws8 should start as %d flat tabs\n%s", n, dumpTreeStr(tree))
	}
	return s, hub, state
}

func leafIDsOf(n *sway.Node) []int64 {
	var out []int64
	for _, l := range n.Leaves() {
		out = append(out, l.ID)
	}
	return out
}
