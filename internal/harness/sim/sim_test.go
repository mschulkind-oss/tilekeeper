package sim

import (
	"errors"
	"testing"

	"github.com/mschulkind-oss/tilekeeper/internal/sway"
)

// buildWorkspace builds a workspace with n leaf windows named w1..wN,
// wrapped in an output/root. The first window starts focused.
func buildWorkspace(t *testing.T, wsName string, n int) (*SimSwayClient, []*sway.Node) {
	t.Helper()
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace(wsName, n)
	output := &sway.Node{ID: 10, Name: "eDP-1", Type: "output", Nodes: []*sway.Node{ws}}
	root := &sway.Node{ID: 1, Type: "root", Nodes: []*sway.Node{output}}
	root.SetParents()
	if len(ws.Nodes) > 0 {
		ws.Nodes[0].Focused = true
	}
	s := NewWithTree(root, []sway.Workspace{{Num: 7, Name: wsName, Focused: true}})
	return s, ws.Leaves()
}

func TestRunCommand_SplitVWrapsTargetInSplitvParent(t *testing.T) {
	s, leaves := buildWorkspace(t, "7", 3)
	target := leaves[1]
	origParent := target.Parent

	if err := s.RunCommand("[con_id=" + itoa(target.ID) + "] splitv"); err != nil {
		t.Fatalf("splitv: %v", err)
	}
	if target.Parent == origParent {
		t.Fatalf("target parent unchanged after splitv")
	}
	if target.Parent.Layout != "splitv" {
		t.Fatalf("new parent layout = %q, want splitv", target.Parent.Layout)
	}
	if target.Parent.Parent != origParent {
		t.Fatalf("new parent not inserted under original parent")
	}
}

func TestRunCommand_SplitNoneFlattensSingleChild(t *testing.T) {
	s, leaves := buildWorkspace(t, "7", 2)
	target := leaves[0]

	// Wrap it first, then flatten.
	if err := s.RunCommand("[con_id=" + itoa(target.ID) + "] splitv"); err != nil {
		t.Fatal(err)
	}
	wrapper := target.Parent
	if err := s.RunCommand("[con_id=" + itoa(target.ID) + "] split none"); err != nil {
		t.Fatal(err)
	}
	if target.Parent == wrapper {
		t.Fatalf("split none didn't lift target out of wrapper")
	}
}

// TestRunCommand_SplitNoneRejectedWhenSiblingsPresent mirrors what real
// sway does: `split none` on a container that has siblings returns
// "Can only flatten a child container with no siblings". The sim now
// surfaces that as ErrFlattenSiblings (wrapping ErrSwayRejected) and
// records the offending command in SwayRejections so the fuzzer and
// replay driver can flag it.
func TestRunCommand_SplitNoneRejectedWhenSiblingsPresent(t *testing.T) {
	sway.ResetIDCounter()
	master := &sway.Node{ID: 101, Type: "con"}
	sibling := &sway.Node{ID: 102, Type: "con"}
	inner := &sway.Node{ID: 103, Type: "con", Layout: "splitv",
		Nodes: []*sway.Node{master, sibling}}
	outer := &sway.Node{ID: 104, Type: "con", Layout: "splith",
		Nodes: []*sway.Node{inner}}
	ws := &sway.Node{ID: 105, Name: "7", Type: "workspace", Layout: "splith",
		Nodes: []*sway.Node{outer}}
	output := &sway.Node{ID: 10, Name: "eDP-1", Type: "output", Nodes: []*sway.Node{ws}}
	root := &sway.Node{ID: 1, Type: "root", Nodes: []*sway.Node{output}}
	root.SetParents()
	s := NewWithTree(root, []sway.Workspace{{Num: 7, Name: "7", Focused: true}})

	err := s.RunCommand("[con_id=101] split none")
	if err == nil {
		t.Fatalf("expected ErrFlattenSiblings, got nil")
	}
	if !errors.Is(err, ErrSwayRejected) {
		t.Fatalf("want ErrSwayRejected, got %v", err)
	}
	if !errors.Is(err, ErrFlattenSiblings) {
		t.Fatalf("want ErrFlattenSiblings, got %v", err)
	}
	if len(s.SwayRejections) != 1 {
		t.Fatalf("SwayRejections = %v, want exactly 1 entry", s.SwayRejections)
	}
	if len(s.UnsupportedCommands) != 0 {
		t.Fatalf("real-sway rejection leaked into UnsupportedCommands: %v", s.UnsupportedCommands)
	}
	if master.Parent != inner {
		t.Fatalf("rejected split none mutated the tree")
	}
}

func TestRunCommand_LayoutSetsParentLayout(t *testing.T) {
	s, leaves := buildWorkspace(t, "7", 3)
	target := leaves[0]

	if err := s.RunCommand("[con_id=" + itoa(target.ID) + "] layout tabbed"); err != nil {
		t.Fatal(err)
	}
	if target.Parent.Layout != "tabbed" {
		t.Fatalf("parent layout = %q, want tabbed", target.Parent.Layout)
	}
}

func TestRunCommand_FocusDirection(t *testing.T) {
	s, leaves := buildWorkspace(t, "7", 3)
	// First leaf is focused initially.
	if err := s.RunCommand("focus right"); err != nil {
		t.Fatal(err)
	}
	if !leaves[1].Focused {
		t.Fatalf("focus right should land on leaves[1]")
	}
	if leaves[0].Focused {
		t.Fatalf("leaves[0] should no longer be focused")
	}
}

func TestRunCommand_MoveRightSwapsSiblings(t *testing.T) {
	s, leaves := buildWorkspace(t, "7", 3)
	parent := leaves[0].Parent

	if err := s.RunCommand("[con_id=" + itoa(leaves[0].ID) + "] move right"); err != nil {
		t.Fatal(err)
	}
	if parent.Nodes[0] != leaves[1] || parent.Nodes[1] != leaves[0] {
		t.Fatalf("move right didn't swap leaves[0] and leaves[1]")
	}
}

func TestRunCommand_MarkThenMoveToMark(t *testing.T) {
	s, leaves := buildWorkspace(t, "7", 3)
	a, b := leaves[0], leaves[2]
	parent := a.Parent

	if err := s.RunCommand("[con_id=" + itoa(b.ID) + "] mark --add move_target"); err != nil {
		t.Fatal(err)
	}
	if err := s.RunCommand("[con_id=" + itoa(a.ID) + "] move window to mark move_target"); err != nil {
		t.Fatal(err)
	}
	// a should now be directly after b in parent.Nodes.
	ia, ib := -1, -1
	for i, c := range parent.Nodes {
		if c == a {
			ia = i
		}
		if c == b {
			ib = i
		}
	}
	if ia != ib+1 {
		t.Fatalf("after move to mark: a idx=%d, b idx=%d — want a = b+1", ia, ib)
	}
}

func TestRunCommand_SwapContainer(t *testing.T) {
	s, leaves := buildWorkspace(t, "7", 3)
	a, b := leaves[0], leaves[2]
	parent := a.Parent

	cmd := "[con_id=" + itoa(a.ID) + "] swap container with con_id " + itoa(b.ID)
	if err := s.RunCommand(cmd); err != nil {
		t.Fatal(err)
	}
	if parent.Nodes[0] != b || parent.Nodes[2] != a {
		t.Fatalf("swap didn't exchange positions: %+v", parent.Nodes)
	}
}

func TestRunCommand_ResizeSetWidth(t *testing.T) {
	s, leaves := buildWorkspace(t, "7", 2)
	target := leaves[0]
	other := leaves[1]
	if err := s.RunCommand("[con_id=" + itoa(target.ID) + "] resize set width 75 ppt"); err != nil {
		t.Fatal(err)
	}
	if target.Percent < 0.74 || target.Percent > 0.76 {
		t.Errorf("target.Percent = %.3f, want ~0.75", target.Percent)
	}
	if other.Percent < 0.24 || other.Percent > 0.26 {
		t.Errorf("other.Percent = %.3f, want ~0.25 (rescaled)", other.Percent)
	}
	if target.Percent+other.Percent < 0.99 || target.Percent+other.Percent > 1.01 {
		t.Errorf("siblings sum = %.3f, want ~1.0", target.Percent+other.Percent)
	}
}

func TestRunCommand_ResizeSetWidthRescalesMultipleSiblings(t *testing.T) {
	s, leaves := buildWorkspace(t, "7", 4)
	target := leaves[0]
	if err := s.RunCommand("[con_id=" + itoa(target.ID) + "] resize set width 70 ppt"); err != nil {
		t.Fatal(err)
	}
	parent := target.Parent
	var sum float64
	for _, c := range parent.Nodes {
		sum += c.Percent
	}
	if sum < 0.99 || sum > 1.01 {
		t.Errorf("sibling sum = %.3f after resize, want ~1.0", sum)
	}
	if target.Percent < 0.69 || target.Percent > 0.71 {
		t.Errorf("target.Percent = %.3f, want ~0.70", target.Percent)
	}
}

func TestRunCommand_UnsupportedCommandRecorded(t *testing.T) {
	s, _ := buildWorkspace(t, "7", 2)
	err := s.RunCommand("fullscreen enable")
	if err == nil || !errors.Is(err, ErrUnsupportedCommand) {
		t.Fatalf("expected ErrUnsupportedCommand, got %v", err)
	}
	if len(s.UnsupportedCommands) != 1 {
		t.Fatalf("UnsupportedCommands = %v, want 1 entry", s.UnsupportedCommands)
	}
}

func TestRunCommand_CompoundCommand(t *testing.T) {
	s, leaves := buildWorkspace(t, "7", 2)
	target := leaves[0]
	cmd := "[con_id=" + itoa(target.ID) + "] splitv, layout stacking"
	if err := s.RunCommand(cmd); err != nil {
		t.Fatalf("compound: %v", err)
	}
	if target.Parent.Layout != "stacked" {
		t.Fatalf("compound didn't apply second clause: layout=%q", target.Parent.Layout)
	}
}

// Sway's command parser accepts "stacking" (sway/commands/layout.c:18-19),
// but IPC serializes L_STACKED as "stacked" (sway/ipc-json.c:55-56). The
// sim must store the IPC form so tilekeeper's IsExcluded (which checks
// parent.Layout == "stacked") agrees. An earlier bug stored "stacking"
// directly and caused ~4000 false fuzzer invariant violations per sweep.
func TestCmdLayout_StackingInputMapsToStackedStorage(t *testing.T) {
	s, leaves := buildWorkspace(t, "7", 2)
	target := leaves[0]
	if err := s.RunCommand("[con_id=" + itoa(target.ID) + "] layout stacking"); err != nil {
		t.Fatalf("layout stacking: %v", err)
	}
	// Input "stacking" must be stored as IPC-canonical "stacked".
	if target.Parent.Layout != "stacked" {
		t.Fatalf("input 'stacking' should store as 'stacked', got %q", target.Parent.Layout)
	}
	// Regression guard: the *input* string "stacking" must never appear
	// in stored form anywhere in the tree.
	var walk func(*sway.Node)
	walk = func(n *sway.Node) {
		if n == nil {
			return
		}
		if n.Layout == "stacking" {
			t.Errorf("node id=%d has forbidden Layout=%q (command form leaked into storage)", n.ID, n.Layout)
		}
		for _, c := range n.Nodes {
			walk(c)
		}
	}
	tree, _ := s.GetTree()
	walk(tree)
}

func TestSubscribeAndInjectEvent(t *testing.T) {
	s, leaves := buildWorkspace(t, "7", 1)
	got := 0
	_ = s.Subscribe([]string{"window"}, func(ev sway.Event) {
		if ev.Change == "focus" {
			got++
		}
	})
	// Workspace events should be filtered out.
	s.InjectEvent(sway.Event{Type: "workspace", Change: "focus"})
	s.InjectEvent(sway.Event{Type: "window", Change: "focus", Container: leaves[0]})
	if got != 1 {
		t.Fatalf("got %d window:focus events, want 1", got)
	}
}

// itoa converts an int64 to decimal without pulling in strconv at the
// call site (keeps the tests terse).
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// --- directional move: sway's container_move_in_direction ---
//
// These pin the behaviors the sim gained when moveDir stopped being
// intra-parent-only. Real sway does not stop at a container edge: it walks
// up the ancestor chain and promotes, or re-orients the workspace. The old
// "silently no-op at the edge" model made a whole class of layout bug
// (windows escaping a tab strip or a stack column) unreachable by the
// fuzzer.

// TestMoveDir_PromotesOutOfContainerAtEdge is the ws8 tab-escape shape: a
// tab strip inside a splith workspace, first tab moved left. Sway promotes
// it out of the strip so it lands beside the tabs.
func TestMoveDir_PromotesOutOfContainerAtEdge(t *testing.T) {
	s, leaves := buildWorkspace(t, "7", 3)
	ws := leaves[0].FindWorkspace()
	strip := &sway.Node{ID: 500, Type: "con", Layout: "tabbed", Parent: ws, Nodes: ws.Nodes}
	for _, c := range strip.Nodes {
		c.Parent = strip
	}
	ws.Nodes = []*sway.Node{strip}

	if err := s.RunCommand("[con_id=" + itoa(leaves[0].ID) + "] move left"); err != nil {
		t.Fatal(err)
	}
	if len(ws.Nodes) != 2 || ws.Nodes[0] != leaves[0] || ws.Nodes[1] != strip {
		t.Fatalf("want ws=[leaf, strip] after promotion, got %d children", len(ws.Nodes))
	}
	if leaves[0].Parent != ws {
		t.Fatalf("promoted leaf parent = %v, want the workspace", leaves[0].Parent.ID)
	}
	if len(strip.Nodes) != 2 {
		t.Fatalf("strip kept %d tabs, want 2", len(strip.Nodes))
	}
	// sway zeroes the moved container's size fraction; the next arrange
	// (which the sim leaves to the layout managers) assigns a real share.
	if leaves[0].Percent != 0 {
		t.Errorf("promoted leaf percent = %v, want 0", leaves[0].Percent)
	}
}

// TestMoveDir_ReorientsWorkspaceWhenPerpendicular covers the other escape
// route: no ancestor is parallel to the direction, so sway wraps every
// workspace child in a container, flips the workspace to that axis, and
// promotes the mover out of the fresh wrapper.
func TestMoveDir_ReorientsWorkspaceWhenPerpendicular(t *testing.T) {
	s, leaves := buildWorkspace(t, "7", 3)
	ws := leaves[0].FindWorkspace()
	ws.Layout = "tabbed"

	if err := s.RunCommand("[con_id=" + itoa(leaves[1].ID) + "] move up"); err != nil {
		t.Fatal(err)
	}
	if ws.Layout != "splitv" {
		t.Fatalf("workspace layout = %q, want splitv (re-oriented by the move)", ws.Layout)
	}
	if len(ws.Nodes) != 2 || ws.Nodes[0] != leaves[1] {
		t.Fatalf("want ws=[mover, wrapper], got %d children with first=%v", len(ws.Nodes), ws.Nodes[0].ID)
	}
	wrapper := ws.Nodes[1]
	if wrapper.Layout != "tabbed" || len(wrapper.Nodes) != 2 {
		t.Fatalf("wrapper = %s with %d children, want tabbed with 2", wrapper.Layout, len(wrapper.Nodes))
	}
}

// TestMoveDir_WorkspaceEdgeIsNoOp: a workspace-direct container at the end
// of a parallel workspace has only "the next output" left to move to, and
// the sim has a single output.
func TestMoveDir_WorkspaceEdgeIsNoOp(t *testing.T) {
	s, leaves := buildWorkspace(t, "7", 3)
	ws := leaves[0].FindWorkspace()
	ws.Layout = "splith"

	if err := s.RunCommand("[con_id=" + itoa(leaves[0].ID) + "] move left"); err != nil {
		t.Fatal(err)
	}
	if len(ws.Nodes) != 3 || ws.Nodes[0] != leaves[0] {
		t.Fatalf("workspace-edge move should be a no-op, got %d children with first=%v",
			len(ws.Nodes), ws.Nodes[0].ID)
	}
}

// TestMoveDir_EntersParallelNeighborContainer: moving toward a container
// whose layout runs along the move axis descends INTO it (sway's
// "Reparenting container (parallel)"), entering from the near end.
func TestMoveDir_EntersParallelNeighborContainer(t *testing.T) {
	s, leaves := buildWorkspace(t, "7", 3)
	ws := leaves[0].FindWorkspace()
	ws.Layout = "splith"
	// [leaf0, column(leaf1, leaf2)] with the column laid out horizontally.
	column := &sway.Node{ID: 500, Type: "con", Layout: "splith", Parent: ws,
		Nodes: []*sway.Node{leaves[1], leaves[2]}}
	leaves[1].Parent, leaves[2].Parent = column, column
	ws.Nodes = []*sway.Node{leaves[0], column}

	if err := s.RunCommand("[con_id=" + itoa(leaves[0].ID) + "] move right"); err != nil {
		t.Fatal(err)
	}
	if len(ws.Nodes) != 1 || ws.Nodes[0] != column {
		t.Fatalf("mover should have left the workspace level, got %d children", len(ws.Nodes))
	}
	if len(column.Nodes) != 3 || column.Nodes[0] != leaves[0] {
		t.Fatalf("mover should have entered the column at index 0, got %v children", len(column.Nodes))
	}
}

// TestMoveDir_FloatingIsNoOp: floating containers move by pixel delta in
// sway and never re-parent, so the tree is untouched.
func TestMoveDir_FloatingIsNoOp(t *testing.T) {
	s, leaves := buildWorkspace(t, "7", 2)
	ws := leaves[0].FindWorkspace()
	float := &sway.Node{ID: 500, Type: "con", Floating: "user_on", Parent: ws}
	ws.FloatingNodes = []*sway.Node{float}

	if err := s.RunCommand("[con_id=500] move left"); err != nil {
		t.Fatal(err)
	}
	if len(ws.FloatingNodes) != 1 || len(ws.Nodes) != 2 {
		t.Fatalf("floating move should not restructure the tree: tiled=%d floating=%d",
			len(ws.Nodes), len(ws.FloatingNodes))
	}
}
