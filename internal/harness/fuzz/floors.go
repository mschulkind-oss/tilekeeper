package fuzz

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
)

// floorsJSON is the checked-in per-invariant floor baseline. It is the
// single source of truth for the CI invariant gate (cmd/fuzz-gate) and is
// validated against a live reference sweep by TestFloorsMatchReferenceSweep.
//
//go:embed floors.json
var floorsJSON []byte

// Floors is the parsed floors.json: a per-invariant max-tolerated count
// for the reference sweep, plus a one-line justification per invariant.
type Floors struct {
	// Floor maps invariant name -> max tolerated count under ReferenceConfig.
	// An invariant whose name is NOT a key here is treated as a brand-new
	// class with an implicit floor of 0 (automatic gate failure).
	Floor map[string]int `json:"floors"`
	// Justification maps invariant name -> the one-line reason its floor is
	// what it is. Every Floor key should have a Justification entry.
	Justification map[string]string `json:"justifications"`
}

// LoadFloors parses the embedded floors.json.
func LoadFloors() (Floors, error) {
	var f Floors
	if err := json.Unmarshal(floorsJSON, &f); err != nil {
		return Floors{}, fmt.Errorf("parse floors.json: %w", err)
	}
	if f.Floor == nil {
		return Floors{}, fmt.Errorf("floors.json has no \"floors\" map")
	}
	return f, nil
}

// Names returns the sorted set of invariant names that have a floor entry.
func (f Floors) Names() []string {
	out := make([]string, 0, len(f.Floor))
	for k := range f.Floor {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ReferenceConfig is the FIXED, deterministic sweep the invariant gate and
// floor baseline are measured against. It is intentionally small enough to
// run in CI (< ~3s on this machine) yet large enough to be representative:
//
//   - 60 seeds (1..60). 60 rather than 50 because seed 53 is the seed in
//     this range that deterministically exercises the master-width
//     px-restore path on a HEALTHY master/stack structure — so reverting
//     the 6ae4d4a stale-rect fix surfaces in the real master-width-honored
//     class (0 -> 8), not only in the degenerate false-positive class.
//   - 2000 steps/seed (vs. the 5000 of cmd/fuzz-sweep) — enough to reach
//     deep, chaotic trees while keeping the whole sweep CI-fast.
//   - MaxWindows 12 — matches cmd/fuzz-sweep's stress level.
//
// The remaining DefaultConfig fields (Workspaces ["7","8"], MasterStack)
// are inherited so the gate exercises the same managers production runs.
const (
	ReferenceSeedLo     = 1
	ReferenceSeedHi     = 60
	ReferenceSteps      = 2000
	ReferenceMaxWindows = 12
)

// ReferenceConfig returns the fuzz.Config for a single seed of the
// reference sweep. Callers iterate seeds ReferenceSeedLo..ReferenceSeedHi.
func ReferenceConfig(seed uint64) Config {
	cfg := DefaultConfig()
	cfg.Seed = seed
	cfg.Steps = ReferenceSteps
	cfg.MaxWindows = ReferenceMaxWindows
	return cfg
}

// SweepResult is the aggregate outcome of running the reference sweep:
// per-invariant total counts plus a first reproducer per invariant.
type SweepResult struct {
	// Counts maps invariant name -> total violations across all seeds.
	Counts map[string]int
	// FirstExample maps invariant name -> a "seed=.. step=.. detail=.."
	// string for the first violation seen (the minimized reproducer used in
	// gate failure output).
	FirstExample map[string]string
}

// RunReferenceSweep executes the deterministic reference sweep and returns
// per-invariant counts and a first reproducer per invariant. Used by both
// cmd/fuzz-gate and the floor-baseline regression test.
func RunReferenceSweep() SweepResult {
	res := SweepResult{
		Counts:       map[string]int{},
		FirstExample: map[string]string{},
	}
	for seed := uint64(ReferenceSeedLo); seed <= ReferenceSeedHi; seed++ {
		r := Run(ReferenceConfig(seed))
		for _, v := range r.Violations {
			res.Counts[v.Invariant]++
			if _, ok := res.FirstExample[v.Invariant]; !ok {
				res.FirstExample[v.Invariant] = fmt.Sprintf(
					"seed=%d step=%d detail=%s", seed, v.Step, v.Detail)
			}
		}
	}
	return res
}
