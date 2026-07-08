package replay

import (
	"testing"

	"github.com/mschulkind-oss/tilekeeper/internal/capture"
	"github.com/mschulkind-oss/tilekeeper/internal/sway"
)

// ev is a small helper to build a window EventRecord.
func winEv(seq int64, change string, id int64, ws string) *capture.EventRecord {
	return &capture.EventRecord{
		Seq:       seq,
		Type:      "window",
		Change:    change,
		WS:        ws,
		Container: &sway.Node{ID: id, Type: "con", Name: "w", Rect: sway.Rect{Width: 1280, Height: 720}},
	}
}

func wsInit(seq int64, name string) *capture.EventRecord {
	return &capture.EventRecord{
		Seq:       seq,
		Type:      "workspace",
		Change:    "init",
		Workspace: &sway.Node{Type: "workspace", Name: name},
	}
}

// TestReplayHealthyClean drives a realistic session — windows created and
// focused on ws7, one closed — and asserts replay reports NO violations.
// This is the round-trip baseline: a faithful capture of healthy behavior
// must run clean through the same invariants the fuzzer uses.
func TestReplayHealthyClean(t *testing.T) {
	f := &capture.File{
		Meta: &capture.MetaRecord{
			Workspaces:    []string{"7"},
			DefaultLayout: "MasterStack",
			MasterWidth:   75,
		},
		Events: []*capture.EventRecord{
			winEv(1, "new", 100, "7"),
			winEv(2, "new", 101, "7"),
			winEv(3, "new", 102, "7"),
			winEv(4, "focus", 101, "7"),
			winEv(5, "close", 102, "7"),
			winEv(6, "focus", 100, "7"),
		},
	}
	res, err := Run(f, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Violations) != 0 {
		for _, v := range res.Violations {
			t.Logf("unexpected violation: [%s] step=%d %s", v.Invariant, v.Step, v.Detail)
		}
		t.Fatalf("healthy capture replayed with %d violation(s); want 0", len(res.Violations))
	}
}

// TestReplayFellowTravelerClean reproduces the 2026-06-13 ws7 incident
// SHAPE on the FIXED code path: three windows created on ws9, then a single
// window::move relocates the whole subtree to ws7 (the fellow-traveler
// move — sway emits one event for a multi-window column). The fixed
// handleWindowMove adopts the fellow-travelers via ArrangeAll, so replay
// must run clean. This proves the round-trip handles the real incident
// shape without false positives.
func TestReplayFellowTravelerClean(t *testing.T) {
	f := &capture.File{
		Meta: &capture.MetaRecord{
			Workspaces:    []string{"7", "9"},
			DefaultLayout: "MasterStack",
			MasterWidth:   75,
		},
		Events: []*capture.EventRecord{
			wsInit(1, "9"),
			winEv(2, "new", 200, "9"),
			winEv(3, "new", 201, "9"),
			winEv(4, "new", 202, "9"),
			// One move event for the focused column → ws7. The siblings ride
			// along in the reconstructed tree (topLevelAncestor moves the
			// whole subtree).
			winEv(5, "move", 200, "7"),
		},
	}
	res, err := Run(f, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, v := range res.Violations {
		t.Logf("violation: [%s] step=%d seq=%d %s", v.Invariant, v.Step, v.Event.Seq, v.Detail)
	}
	if len(res.Violations) != 0 {
		t.Fatalf("fellow-traveler capture replayed with %d violation(s) on FIXED code; want 0", len(res.Violations))
	}
}

// TestReplayFlagsDivergence hand-crafts a capture containing a divergence and
// asserts replay FLAGS it. The divergence: a "dropped close" — the daemon's
// event channel overflowed and the window::close for con 301 never reached
// the hub, but the window really did leave the tree. We model that by
// emitting the close to the reconstruction (so the sim tree loses the leaf)
// but as a DROP marker the replay tree applies WITHOUT dispatching to the
// hub. Since the public capture format has no drop bit, we instead inject
// the post-drop STATE directly: con 301 is created and tracked, then a
// SECOND window 302 arrives whose snapshot the hub adopts, while 301 has
// silently vanished from the tree via a move whose WS we leave empty so the
// hub never updates tracking — leaving 301 stale in the manager but absent
// from the leaves. tracked-matches-leaves must fire.
//
// Concretely: create 300, 301 on ws7 (both tracked). Then close 301 but mark
// the capture so replay drops the dispatch (DropConID). The reconstructed
// tree loses 301; the manager still tracks it → stale → tracked-matches-
// leaves fires with the exact con id.
func TestReplayFlagsDivergence(t *testing.T) {
	f := &capture.File{
		Meta: &capture.MetaRecord{
			Workspaces:    []string{"7"},
			DefaultLayout: "MasterStack",
			MasterWidth:   75,
		},
		Events: []*capture.EventRecord{
			winEv(1, "new", 300, "7"),
			winEv(2, "new", 301, "7"),
			// Dropped close: the tree loses 301 but the hub never sees it.
			{Seq: 3, Type: "window", Change: "close", WS: "7", Drop: true,
				Container: &sway.Node{ID: 301, Type: "con", Name: "w"}},
		},
	}
	res, err := Run(f, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !hasViolation(res, "tracked-matches-leaves") {
		for _, v := range res.Violations {
			t.Logf("got: [%s] %s", v.Invariant, v.Detail)
		}
		t.Fatalf("expected tracked-matches-leaves violation on dropped-close capture; got %d violations", len(res.Violations))
	}
	// The flagged violation must name con 301 (the dropped/stale window).
	var detail string
	for _, v := range res.Violations {
		if v.Invariant == "tracked-matches-leaves" {
			detail = v.Detail
			break
		}
	}
	if detail == "" || !contains(detail, "301") {
		t.Errorf("violation detail should name stale con 301; got %q", detail)
	}
}

func hasViolation(res *Result, inv string) bool {
	for _, v := range res.Violations {
		if v.Invariant == inv {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
