//go:build integration

// Real-sway regression tests for the MasterStack alt-tab cycle: focus into
// the stack must land on the TOP, and swap-master must promote in MRU order
// so the ex-master is sitting there.
//
// These live here, against real sway, because NOTHING ELSE CAN CATCH THIS:
//
//   - sway.Mock records commands without restructuring the tree, so it can
//     only assert what tilekeeper emitted, never where a window ended up.
//   - The sim descends into a container via Nodes[0] (see the KNOWN
//     DIVERGENCE note on sim.directionalSibling), i.e. it lands on the top —
//     the behavior we want — so a sim test passes against the BROKEN code.
//     The sim's bug masks production's bug.
//   - The fuzzer inherits the sim's model, and no invariant expresses "focus
//     is on the right window" anyway; that is layout semantics, not a
//     structural property.
//
// Real sway descends by focus history (seat_get_focus_inactive), which is the
// whole bug: focus landed on whichever stack window was touched last. Only a
// real sway can tell the two apart.
package swayreal

import (
	"fmt"
	"testing"
	"time"

	"github.com/mschulkind-oss/tilekeeper/internal/layout"
	"github.com/mschulkind-oss/tilekeeper/internal/sway"
)

// msOnRealSway boots sway, spawns n windows on workspace 7, and returns a
// MasterStack driven by the production IPC client against that sway.
func msOnRealSway(t *testing.T, n int) (*layout.MasterStack, *sway.Conn, *Sway) {
	t.Helper()
	sw := startSwayOrSkip(t)
	if err := sw.FocusWorkspace("7"); err != nil {
		t.Fatalf("focus ws7: %v", err)
	}
	if _, err := sw.SpawnWindows(n); err != nil {
		t.Fatalf("spawn %d windows: %v", n, err)
	}
	conn, err := sway.ConnectTo(sw.SocketPath())
	if err != nil {
		t.Fatalf("connect production client: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	cfg := layout.DefaultMasterStackConfig()
	cfg.StackSide = layout.SideRight
	cfg.StackLayout = layout.StackSplitV
	cfg.VisibleStackLimit = n + 1 // no substack; keep the shape simple
	ms := layout.NewMasterStackManager(conn, cfg)

	ws := wsNode(t, conn, "7")
	if err := ms.ArrangeAll(ws); err != nil {
		t.Fatalf("ArrangeAll: %v", err)
	}
	settle()
	return ms, conn, sw
}

// settle gives sway a moment to apply commands before the tree is re-read.
func settle() { time.Sleep(150 * time.Millisecond) }

func wsNode(t *testing.T, conn *sway.Conn, name string) *sway.Node {
	t.Helper()
	tree, err := conn.GetTree()
	if err != nil {
		t.Fatalf("get_tree: %v", err)
	}
	var find func(n *sway.Node) *sway.Node
	find = func(n *sway.Node) *sway.Node {
		if n.Type == "workspace" && n.Name == name {
			return n
		}
		for _, c := range n.Nodes {
			if r := find(c); r != nil {
				return r
			}
		}
		return nil
	}
	ws := find(tree)
	if ws == nil {
		t.Fatalf("workspace %s not found", name)
	}
	return ws
}

// tiledOrder is the workspace's tiled leaves in tree order.
func tiledOrder(t *testing.T, conn *sway.Conn) []int64 {
	t.Helper()
	var ids []int64
	for _, l := range wsNode(t, conn, "7").Leaves() {
		if layout.IsExcluded(l) {
			continue
		}
		ids = append(ids, l.ID)
	}
	return ids
}

func focusedID(t *testing.T, conn *sway.Conn) int64 {
	t.Helper()
	f := wsNode(t, conn, "7").FindFocused()
	if f == nil {
		return 0
	}
	return f.ID
}

// cmd runs a layout command with a FRESHLY fetched workspace, the way the Hub
// does — the tree is the truth, never a stale snapshot.
func cmd(t *testing.T, ms *layout.MasterStack, conn *sway.Conn, c string) {
	t.Helper()
	if err := ms.Command(c, wsNode(t, conn, "7")); err != nil {
		t.Fatalf("%s: %v", c, err)
	}
	settle()
}

// TestIntegrationFocusIntoStackLandsOnTop is the direct regression test for
// "$mod+o lands mid-stack". It touches a MIDDLE stack window first, which is
// what poisons sway's focus history and made native `focus right` land there.
func TestIntegrationFocusIntoStackLandsOnTop(t *testing.T) {
	ms, conn, _ := msOnRealSway(t, 4)

	ids := ms.WindowIDs()
	if len(ids) != 4 {
		t.Fatalf("tracked %d windows, want 4 (%v)", len(ids), ids)
	}
	master, top, middle := ids[0], ids[1], ids[2]

	// Poison the stack column's focus history, then return to master.
	if err := conn.RunCommand(fmt.Sprintf("[con_id=%d] focus", middle)); err != nil {
		t.Fatalf("focus middle: %v", err)
	}
	settle()
	if err := conn.RunCommand(fmt.Sprintf("[con_id=%d] focus", master)); err != nil {
		t.Fatalf("focus master: %v", err)
	}
	settle()

	cmd(t, ms, conn, "focus right")

	if got := focusedID(t, conn); got != top {
		t.Errorf("focus right from master landed on %d, want top of stack %d\n"+
			"(middle=%d was focused last — landing there is the focus-history bug)",
			got, top, middle)
	}
}

// TestIntegrationAltTabCycle is the user-reported workflow end to end:
// $mod+o then $mod+Return, repeatedly, must alternate between the same two
// windows with the partner always on TOP of the stack, leaving the rest of
// the stack alone.
//
// Note the discriminating assertion is the TOP/order one, not "two distinct
// masters": the old swap-based code ALSO alternated between two windows — it
// just parked the partner at the BOTTOM of the stack. Asserting only on the
// cycle would pass against the bug.
func TestIntegrationAltTabCycle(t *testing.T) {
	ms, conn, _ := msOnRealSway(t, 4)

	ids := ms.WindowIDs()
	a, b, c, d := ids[0], ids[1], ids[2], ids[3]

	// Seed from a BOTTOM-of-stack promote: the case old swap-master got
	// wrong (it exiled the ex-master to the bottom instead of the top).
	if err := conn.RunCommand(fmt.Sprintf("[con_id=%d] focus", d)); err != nil {
		t.Fatalf("focus bottom: %v", err)
	}
	settle()
	cmd(t, ms, conn, "swap-master")

	want := []int64{d, a, b, c}
	if got := tiledOrder(t, conn); !equal(got, want) {
		t.Fatalf("after promoting the bottom window: tree = %v, want %v\n"+
			"(ex-master must land on TOP of the stack, not in the promoted window's old slot)",
			got, want)
	}

	for round := range 4 {
		order := tiledOrder(t, conn)
		top := order[1]

		cmd(t, ms, conn, "focus right")
		if got := focusedID(t, conn); got != top {
			t.Fatalf("round %d: focus right landed on %d, want top of stack %d",
				round, got, top)
		}

		cmd(t, ms, conn, "swap-master")
		if got := focusedID(t, conn); got != top {
			t.Fatalf("round %d: promoted window %d is not focused (got %d)", round, top, got)
		}
		if got := tiledOrder(t, conn); got[0] != top {
			t.Fatalf("round %d: master = %d, want the promoted %d (tree=%v)",
				round, got[0], top, got)
		}
		// The tail past the two cycling windows must never move.
		if got := tiledOrder(t, conn)[2:]; !equal(got, []int64{b, c}) {
			t.Fatalf("round %d: tail = %v, want %v — cycling disturbed the stack",
				round, got, []int64{b, c})
		}
	}
}

func equal(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
