package fuzz

import "testing"

// TestFloorsParse sanity-checks the embedded floors.json: it parses, has a
// floor map, and every floor has a justification.
func TestFloorsParse(t *testing.T) {
	f, err := LoadFloors()
	if err != nil {
		t.Fatalf("LoadFloors: %v", err)
	}
	if len(f.Floor) == 0 {
		t.Fatal("no floors loaded")
	}
	for name := range f.Floor {
		if f.Justification[name] == "" {
			t.Errorf("floor %q has no justification", name)
		}
	}
}

// TestFloorsMatchReferenceSweep is the regression pin. It runs the live
// reference sweep and asserts NO invariant exceeds its checked-in floor and
// NO invariant class appears that the floors file doesn't list. This is the
// same condition cmd/fuzz-gate enforces in CI; keeping it as a unit test
// means `go test ./...` catches a floor regression even without invoking the
// gate binary.
//
// It is deliberately EXACT-or-below (not equality): a fix that drives a
// residual DOWN keeps the test green, and the floors should then be lowered
// in a follow-up. A regression that drives a count UP fails immediately.
func TestFloorsMatchReferenceSweep(t *testing.T) {
	f, err := LoadFloors()
	if err != nil {
		t.Fatalf("LoadFloors: %v", err)
	}
	sweep := RunReferenceSweep()

	for inv, count := range sweep.Counts {
		floor, listed := f.Floor[inv]
		if !listed {
			t.Errorf("NEW invariant class %q appeared (count=%d) with no floor entry — add it to floors.json with a justification (and likely fix the underlying bug). first: %s",
				inv, count, sweep.FirstExample[inv])
			continue
		}
		if count > floor {
			t.Errorf("invariant %q REGRESSED: count=%d > floor=%d (delta +%d). first: %s",
				inv, count, floor, count-floor, sweep.FirstExample[inv])
		}
	}

	// A zero-count invariant that's in the floors file is fine (and ideal).
	// We do NOT require every floor to be exercised — some are pure guards.
}
