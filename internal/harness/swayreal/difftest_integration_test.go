//go:build integration

// This test runs ONLY under `-tags integration` (e.g. via
// `just test-integration`), so normal `go test ./...` neither builds nor
// runs it. It boots a real headless sway and differentially tests the
// in-memory sim against it. If no sway binary is available it Skips, exactly
// like the `command -v sway` gate in the justfile.
package swayreal

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// startSwayOrSkip boots headless sway, skipping the test (not failing) when
// no sway binary exists — matching the test-integration contract.
func startSwayOrSkip(t *testing.T) *Sway {
	t.Helper()
	sw, err := Start(Options{StartTimeout: 15 * time.Second})
	if err == ErrNoSway {
		t.Skip("no sway binary found ($SWAY_BIN, PATH, known nix path) — skipping integration test")
	}
	if err != nil {
		t.Fatalf("start headless sway: %v", err)
	}
	t.Cleanup(func() { sw.Close() })
	return sw
}

// TestIntegrationSwayBoots is the smallest smoke test: sway serves get_tree
// and run_command over real IPC.
func TestIntegrationSwayBoots(t *testing.T) {
	sw := startSwayOrSkip(t)
	tree, err := sw.GetTree()
	if err != nil {
		t.Fatalf("get_tree: %v", err)
	}
	if tree == nil || tree.Type != "root" {
		t.Fatalf("expected root node, got %#v", tree)
	}
	if err := sw.RunCommand("workspace 7; splith"); err != nil {
		t.Fatalf("run_command: %v", err)
	}
}

// TestIntegrationSpawnWindows confirms the weston-terminal spawn helper
// produces the expected number of tiled leaves.
func TestIntegrationSpawnWindows(t *testing.T) {
	sw := startSwayOrSkip(t)
	if err := sw.FocusWorkspace("7"); err != nil {
		t.Fatalf("focus ws7: %v", err)
	}
	ids, err := sw.SpawnWindows(2)
	if err != nil {
		t.Skipf("could not spawn windows (no Wayland client?): %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 spawned ids, got %d (%v)", len(ids), ids)
	}
}

// TestIntegrationDiffScenarios drives the differential engine: seed a sim
// from sway's real tree, apply a command sequence to both, and assert the
// trees stay structurally identical. These target the command classes that
// caused real escaped bugs (swap with a floating endpoint, move-to-mark with
// floating source/dest, resize set width, split none, layout tabbed). Each
// scenario carries a KnownGap note for behaviors the sim deliberately does
// not model; those report but do not fail.
func TestIntegrationDiffScenarios(t *testing.T) {
	sw := startSwayOrSkip(t)
	if sw == nil {
		return
	}

	for _, sc := range integrationScenarios() {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			if err := sw.FocusWorkspace("7"); err != nil {
				t.Fatalf("focus ws7: %v", err)
			}
			ids, err := sw.SpawnWindows(sc.windows)
			if err != nil {
				t.Skipf("could not spawn %d windows: %v", sc.windows, err)
			}
			t.Cleanup(func() { drain(t, sw) })

			cmds, err := substitutePlaceholders(sc.commands, ids)
			if err != nil {
				t.Fatalf("substitute: %v", err)
			}
			res, err := sw.RunScenario(cmds)
			if err != nil {
				t.Fatalf("run scenario: %v", err)
			}
			switch {
			case res.Diverged && sc.knownGap == "":
				t.Errorf("UNEXPECTED divergence at cmd #%d %q: %s\n--- sim ---\n%s--- sway ---\n%s",
					res.DivergedAt, res.DivergeCmd, res.Detail, res.SimSubtree, res.SwaySubtree)
			case res.Diverged && sc.knownGap != "":
				t.Logf("known gap (tolerated) at cmd #%d %q: %s\ngap: %s",
					res.DivergedAt, res.DivergeCmd, res.Detail, sc.knownGap)
			case !res.Diverged && sc.knownGap != "":
				t.Errorf("scenario %q no longer diverges — remove the KnownGap annotation", sc.name)
			}
		})
	}
}

// --- test-local scenario set (kept independent of cmd/sway-difftest so the
// integration package has no cross-main dependency) ---

type intScenario struct {
	name     string
	windows  int
	commands []string
	knownGap string
}

func integrationScenarios() []intScenario {
	return []intScenario{
		{name: "split-none-flatten", windows: 2, commands: []string{
			"[con_id=$0] splitv", "[con_id=$0] split none"}},
		{name: "layout-tabbed-nested", windows: 3, commands: []string{
			"[con_id=$0] splitv", "[con_id=$1] layout tabbed"}},
		{name: "resize-set-width-ppt", windows: 2, commands: []string{
			"[con_id=$0] resize set width 75 ppt"}},
		{name: "resize-set-width-px", windows: 2, commands: []string{
			"[con_id=$0] resize set width 960 px"}},
		{name: "swap-floating-endpoint", windows: 2, commands: []string{
			"[con_id=$0] floating enable",
			"[con_id=$1] swap container with con_id $0"}},
		{name: "move-to-mark-floating-dest", windows: 3, commands: []string{
			"[con_id=$0] floating enable",
			"[con_id=$0] mark --add dest",
			"[con_id=$1] move to mark dest"}},
		{name: "move-to-mark-floating-src", windows: 3, commands: []string{
			"[con_id=$2] mark --add anchor",
			"[con_id=$0] floating enable",
			"[con_id=$0] move to mark anchor"}},
		{name: "swap-two-tiled", windows: 3, commands: []string{
			"[con_id=$0] swap container with con_id $2"}},
		{name: "layout-stacking-then-split", windows: 2, commands: []string{
			"[con_id=$0] layout stacking", "[con_id=$0] splith"}},
		{
			name: "floating-toggle-reinsert", windows: 2,
			commands: []string{
				"[con_id=$0] floating enable", "[con_id=$0] floating disable"},
			knownGap: "insert-arrange percent: sway eagerly redistributes a split row on " +
				"re-tile; the sim leaves insert-time percents to the layout manager " +
				"(modeling it regressed the master-width fuzzer invariant). Float-out " +
				"rescale is modeled; float-in arrange is deferred by design.",
		},
	}
}

func drain(t *testing.T, sw *Sway) {
	t.Helper()
	for i := 0; i < 10; i++ {
		n, err := sw.LeafCount()
		if err != nil || n == 0 {
			return
		}
		if err := sw.KillAllLeaves(); err != nil {
			return
		}
		if sw.WaitForLeafCount(0, 8*time.Second) == nil {
			return
		}
	}
}

// substitutePlaceholders replaces $i with the i-th spawned con id (highest
// index first so $10 is not partially matched by $1).
func substitutePlaceholders(cmds []string, ids []int64) ([]string, error) {
	out := make([]string, 0, len(cmds))
	for _, c := range cmds {
		s := c
		for i := len(ids) - 1; i >= 0; i-- {
			s = strings.ReplaceAll(s, fmt.Sprintf("$%d", i), fmt.Sprintf("%d", ids[i]))
		}
		if strings.Contains(s, "$") {
			return nil, fmt.Errorf("unsubstituted placeholder in %q (have %d windows)", c, len(ids))
		}
		out = append(out, s)
	}
	return out, nil
}
