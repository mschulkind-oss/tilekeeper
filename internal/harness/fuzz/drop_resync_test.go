package fuzz

import (
	"testing"
)

// TestDrop_ProducesCorruption documents the cost of letting daemon-side
// event drops happen. The daemon has NO recovery mechanism: drops are
// silent corruption sources (manager tracking diverges from the live
// sway tree the moment a state-change event is missed), and the fix is
// strictly upstream — bounded-channel sizing + subscribe-side filtering
// of high-volume no-op events (window::mark / window::title / urgent).
//
// This test exists to:
//
//  1. Keep the fuzz mechanism for `Config.DropRate` alive so anyone
//     weakening the production preventions can rerun it and see what
//     breaks.
//  2. Pin the consequence in a regression test — a passing test with
//     DropRate > 0 and zero tracked-matches-leaves violations would
//     mean either the invariant regressed OR someone re-introduced a
//     hidden recovery path. Both are bugs.
//
// Originally surfaced by the 2026-05-22 ws7 journal: a cross-workspace
// move burst (17×"event queue full, dropping event") corrupted
// MasterStack's windowIDs and produced "No matching node" / "Mark
// 'move_target' not found" warnings 10 s later.
func TestDrop_ProducesCorruption(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Steps = 400
	cfg.DropRate = 0.15

	seeds := []uint64{1, 2, 3, 5, 7, 11, 13}
	hits := 0
	for _, seed := range seeds {
		cfg.Seed = seed
		res := Run(cfg)
		for _, v := range res.Violations {
			if v.Invariant == "tracked-matches-leaves" {
				hits++
				break
			}
		}
	}
	if hits == 0 {
		t.Fatalf("expected drop-induced tracking divergence across seeds %v, got none — "+
			"either DropRate is no longer biting OR a hidden recovery path was added "+
			"(both are bugs; the daemon must NOT have drop-recovery, only drop-prevention)",
			seeds)
	}
	t.Logf("seeds with drop-induced violations: %d/%d", hits, len(seeds))
}
