package sim

import (
	"errors"
	"testing"

	"github.com/mschulkind-oss/tilekeeper/internal/sway"
)

// floatLeaf moves leaf from the tiled tree to its workspace's floating
// list, the way real sway's container_set_floating does.
func floatLeaf(t *testing.T, leaf *sway.Node) {
	t.Helper()
	ws := leaf.FindWorkspace()
	if ws == nil {
		t.Fatalf("leaf %d has no workspace", leaf.ID)
	}
	p := leaf.Parent
	for i, c := range p.Nodes {
		if c == leaf {
			p.Nodes = append(p.Nodes[:i], p.Nodes[i+1:]...)
			break
		}
	}
	leaf.Floating = "auto_on"
	ws.FloatingNodes = append(ws.FloatingNodes, leaf)
	leaf.Parent = ws
}

// TestSwap_TiledWithFloatingExchangesFloatingness pins sway's swap_places
// semantics (sway/tree/container.c:1718-1764): floatingness is positional,
// so swapping a tiled container with a floating one transfers floating
// status both ways — silently (real sway emits NO IPC events from swap;
// the only window::floating emitter is container_set_floating, which swap
// never calls). This is the mechanism that scrambled live ws7 on
// 2026-06-12 when MasterStack's master-insert dance swapped a tracked
// window with an already-floating portal save-dialog.
func TestSwap_TiledWithFloatingExchangesFloatingness(t *testing.T) {
	s, leaves := buildWorkspace(t, "7", 3)
	tiled, floater := leaves[0], leaves[2]
	floatLeaf(t, floater)
	ws := tiled.FindWorkspace()
	tiledSlotParent := tiled.Parent

	if err := s.RunCommand("[con_id=" + itoa(floater.ID) + "] swap container with con_id " + itoa(tiled.ID)); err != nil {
		t.Fatalf("swap: %v", err)
	}

	if !tiled.IsFloating() {
		t.Errorf("tiled window %d did not become floating after swap with floating %d",
			tiled.ID, floater.ID)
	}
	if floater.IsFloating() {
		t.Errorf("floating window %d did not become tiled after swap", floater.ID)
	}
	// List membership must match the flags.
	inFloatingList := false
	for _, fn := range ws.FloatingNodes {
		if fn == tiled {
			inFloatingList = true
		}
		if fn == floater {
			t.Errorf("ex-floating %d still in FloatingNodes", floater.ID)
		}
	}
	if !inFloatingList {
		t.Errorf("newly-floating %d not in ws.FloatingNodes", tiled.ID)
	}
	if floater.Parent != tiledSlotParent {
		t.Errorf("ex-floating %d not in the vacated tiled slot (parent=%v, want %v)",
			floater.ID, floater.Parent.ID, tiledSlotParent.ID)
	}
}

// TestSwap_TiledWithTiledKeepsBothTiled guards the boring case.
func TestSwap_TiledWithTiledKeepsBothTiled(t *testing.T) {
	s, leaves := buildWorkspace(t, "7", 3)
	a, b := leaves[0], leaves[2]
	pa, pb := a.Parent, b.Parent

	if err := s.RunCommand("[con_id=" + itoa(a.ID) + "] swap container with con_id " + itoa(b.ID)); err != nil {
		t.Fatalf("swap: %v", err)
	}
	if a.IsFloating() || b.IsFloating() {
		t.Errorf("tiled<->tiled swap produced a floating window: a=%q b=%q", a.Floating, b.Floating)
	}
	if a.Parent != pb || b.Parent != pa {
		t.Errorf("positions not exchanged")
	}
}

// TestSwap_NotFoundTargetRejected mirrors real sway's cmd_swap returning
// CMD_FAILURE for a vanished con_id — previously the sim silently
// succeeded, letting engines swap against closed windows without the
// fuzzer noticing.
func TestSwap_NotFoundTargetRejected(t *testing.T) {
	s, leaves := buildWorkspace(t, "7", 2)
	err := s.RunCommand("[con_id=" + itoa(leaves[0].ID) + "] swap container with con_id 99999")
	if err == nil {
		t.Fatal("expected rejection for missing swap target, got nil")
	}
	if !errors.Is(err, ErrSwayRejected) {
		t.Fatalf("want ErrSwayRejected, got %v", err)
	}
}

// TestMoveToMark_FloatingSourceSameWorkspaceIsNoop pins
// container_move_to_container's floating-source short-circuit
// (sway/commands/move.c:247-250 → container_move_to_workspace, which
// early-returns for same workspace at move.c:200-202). The op=114
// `move 77 to mark on 17` from the 2026-06-12 incident was this no-op.
func TestMoveToMark_FloatingSourceSameWorkspaceIsNoop(t *testing.T) {
	s, leaves := buildWorkspace(t, "7", 3)
	src, dest := leaves[0], leaves[2]
	floatLeaf(t, src)
	ws := src.FindWorkspace()

	if err := s.RunCommand("[con_id=" + itoa(dest.ID) + "] mark --add move_target"); err != nil {
		t.Fatal(err)
	}
	if err := s.RunCommand("[con_id=" + itoa(src.ID) + "] move window to mark move_target"); err != nil {
		t.Fatalf("move to mark: %v", err)
	}
	if !src.IsFloating() {
		t.Errorf("floating source got tiled by same-workspace move-to-mark")
	}
	found := false
	for _, fn := range ws.FloatingNodes {
		if fn == src {
			found = true
		}
	}
	if !found {
		t.Errorf("floating source left ws.FloatingNodes")
	}
}

// TestMoveToMark_FloatingSourceCrossWorkspaceStaysFloating pins the
// cross-workspace half of the floating-source branch: the float relocates
// to the destination workspace but STAYS floating
// (container_move_to_workspace → workspace_add_floating).
func TestMoveToMark_FloatingSourceCrossWorkspaceStaysFloating(t *testing.T) {
	sway.ResetIDCounter()
	wsA := sway.CreateWorkspace("7", 2)
	wsB := sway.CreateWorkspace("8", 1)
	output := &sway.Node{ID: 10, Name: "eDP-1", Type: "output", Nodes: []*sway.Node{wsA, wsB}}
	root := &sway.Node{ID: 1, Type: "root", Nodes: []*sway.Node{output}}
	root.SetParents()
	s := NewWithTree(root, []sway.Workspace{
		{Num: 7, Name: "7", Focused: true}, {Num: 8, Name: "8"}})

	src := wsA.Leaves()[0]
	floatLeaf(t, src)
	dest := wsB.Leaves()[0]

	if err := s.RunCommand("[con_id=" + itoa(dest.ID) + "] mark --add move_target"); err != nil {
		t.Fatal(err)
	}
	if err := s.RunCommand("[con_id=" + itoa(src.ID) + "] move window to mark move_target"); err != nil {
		t.Fatalf("move to mark: %v", err)
	}
	if !src.IsFloating() {
		t.Errorf("cross-workspace move-to-mark tiled the floating source (Floating=%q)", src.Floating)
	}
	found := false
	for _, fn := range wsB.FloatingNodes {
		if fn == src {
			found = true
		}
	}
	if !found {
		t.Errorf("floating source not relocated to destination workspace's FloatingNodes")
	}
}

// TestMoveToMark_FloatingDestinationFloatsSource pins the second silent
// float vector: container_add_sibling with a floating reference inserts
// the moved container into the workspace floating list
// (sway/tree/container.c:1410-1423 via move.c:257-260). Only a
// window::move event fires in real sway — no window::floating. This is
// how window 18 was silently floated during op=115 of the 2026-06-12
// incident (substack promote moved 18 to a mark sitting on
// already-floating 17).
func TestMoveToMark_FloatingDestinationFloatsSource(t *testing.T) {
	s, leaves := buildWorkspace(t, "7", 3)
	src, dest := leaves[0], leaves[2]
	floatLeaf(t, dest)
	ws := dest.FindWorkspace()

	if err := s.RunCommand("[con_id=" + itoa(dest.ID) + "] mark --add move_target"); err != nil {
		t.Fatal(err)
	}
	if err := s.RunCommand("[con_id=" + itoa(src.ID) + "] move window to mark move_target"); err != nil {
		t.Fatalf("move to mark: %v", err)
	}
	if !src.IsFloating() {
		t.Errorf("tiled source moved onto floating destination should float, got %q", src.Floating)
	}
	found := false
	for _, fn := range ws.FloatingNodes {
		if fn == src {
			found = true
		}
	}
	if !found {
		t.Errorf("source not in ws.FloatingNodes after move onto floating dest")
	}
}
