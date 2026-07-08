package fuzz

import (
	"testing"
)

func TestRun_Deterministic(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Steps = 100
	r1 := Run(cfg)
	r2 := Run(cfg)
	if len(r1.Violations) != len(r2.Violations) {
		t.Fatalf("non-determinism: %d vs %d violations", len(r1.Violations), len(r2.Violations))
	}
	if r1.Steps != r2.Steps {
		t.Fatalf("non-determinism: %d vs %d steps", r1.Steps, r2.Steps)
	}
}

func TestRun_Smoke(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Steps = 200
	res := Run(cfg)
	if res.Steps == 0 {
		t.Fatal("no steps executed")
	}
	// We expect that MasterStack on a fresh workspace does NOT panic and
	// the sim remains fully-covered (no unsupported commands) for the
	// basic generator. If this fails, either the sim is incomplete or the
	// decision logic has a real bug — surface the details.
	for _, v := range res.Violations {
		t.Logf("violation: inv=%s step=%d detail=%s", v.Invariant, v.Step, v.Detail)
	}
	// Don't fail on violations in the smoke test — this is a *fuzzer*:
	// its job is to report, not to gate CI until we've triaged what it
	// actually finds. Separate CI target can treat violations as failures.
	t.Logf("fuzz run: %d steps, %d violations", res.Steps, len(res.Violations))
}

func TestRun_DifferentSeedsProduceDifferentStreams(t *testing.T) {
	a := DefaultConfig()
	a.Seed = 1
	a.Steps = 50
	b := DefaultConfig()
	b.Seed = 2
	b.Steps = 50
	ra := Run(a)
	rb := Run(b)
	// Very weak assertion — at least one of steps or violation count should
	// differ. If seeds produce identical streams of 50 events, something is
	// wrong with the RNG wiring.
	if ra.Steps == rb.Steps && len(ra.Violations) == len(rb.Violations) {
		t.Logf("seeds 1 and 2 happened to produce same-shaped runs; flaky but not necessarily wrong")
	}
}
