package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/mschulkind-oss/tilekeeper/internal/harness/swayreal"
)

// Scenario is a differential-test case: spawn Windows tiled windows on a
// workspace, then apply Commands to both the sim and sway, diffing after
// each. Commands may reference the i-th spawned window's con id with the
// placeholder $i (0-based, in spawn order). The placeholder is substituted
// with the real con id assigned by sway before the command runs through
// both sides — this is what lets a static scenario address dynamically
// allocated windows.
type Scenario struct {
	Name     string
	Windows  int
	Commands []string
	// Why documents which production bug class this scenario targets.
	Why string
	// KnownGap, if non-empty, marks this scenario as exercising a behavior
	// the sim deliberately does not model (e.g. sway's eager insert-arrange
	// percent redistribution, which the layout managers own instead). The
	// difftest still RUNS and PRINTS any divergence for visibility, but a
	// known-gap divergence does not fail the run. A scenario that
	// unexpectedly STOPS diverging is reported so the gap annotation can be
	// removed.
	KnownGap string
}

// AllScenarios returns the full battery. Each targets a command class that
// produced a real escaped bug, per DELIVERABLE 3 coverage requirements.
func AllScenarios() []Scenario {
	return []Scenario{
		{
			Name:    "split-none-flatten",
			Windows: 2,
			Why:     "split none flatten — wrapper-chain collapse fidelity",
			Commands: []string{
				"[con_id=$0] splitv",     // wrap $0 in a splitv
				"[con_id=$0] split none", // flatten it back
			},
		},
		{
			Name:    "layout-tabbed-nested",
			Windows: 3,
			Why:     "layout tabbed on a nested tree — wrap-children path",
			Commands: []string{
				"[con_id=$0] splitv", // build nesting
				"[con_id=$1] layout tabbed",
			},
		},
		{
			Name:    "resize-set-width-ppt",
			Windows: 2,
			Why:     "resize set width ppt — does sim Percent match sway?",
			Commands: []string{
				"[con_id=$0] resize set width 75 ppt",
			},
		},
		{
			Name:    "resize-set-width-px",
			Windows: 2,
			Why:     "resize set width px — px→percent conversion fidelity (master-width bug)",
			Commands: []string{
				"[con_id=$0] resize set width 960 px",
			},
		},
		{
			Name:    "floating-toggle",
			Windows: 2,
			Why:     "floating enable/disable — structural detach/re-tile",
			KnownGap: "insert-arrange percent: when a window RE-TILES, sway eagerly " +
				"redistributes the split row (50/50), but the sim leaves insert-time " +
				"percents to the layout manager (modeling it in apply.go regressed the " +
				"master-width invariant 1492→64138, since MasterStack owns the 75% master). " +
				"Float-OUT rescale IS modeled; float-IN arrange is deliberately deferred.",
			Commands: []string{
				"[con_id=$0] floating enable",
				"[con_id=$0] floating disable",
			},
		},
		{
			Name:    "swap-floating-endpoint",
			Windows: 2,
			Why:     "swap with a floating endpoint — the ctrl-s float-bleed bug",
			Commands: []string{
				"[con_id=$0] floating enable",               // $0 floats
				"[con_id=$1] swap container with con_id $0", // tiled $1 swaps with floating $0
			},
		},
		{
			Name:    "move-to-mark-floating-dest",
			Windows: 3,
			Why:     "move to mark with floating DEST — silent-float bug (op=115)",
			Commands: []string{
				"[con_id=$0] floating enable", // $0 floats, gets a mark
				"[con_id=$0] mark --add dest",
				"[con_id=$1] move to mark dest", // tiled $1 → floating dest
			},
		},
		{
			Name:    "move-to-mark-floating-src",
			Windows: 3,
			Why:     "move to mark with floating SOURCE — same-ws no-op (op=114)",
			Commands: []string{
				"[con_id=$2] mark --add anchor",
				"[con_id=$0] floating enable",     // floating source
				"[con_id=$0] move to mark anchor", // same-ws floating move = no-op
			},
		},
		{
			Name:    "swap-two-tiled",
			Windows: 3,
			Why:     "swap two tiled containers — baseline swap geometry",
			Commands: []string{
				"[con_id=$0] swap container with con_id $2",
			},
		},
		{
			Name:    "layout-stacking-then-split",
			Windows: 2,
			Why:     "stacking↔split — the stacked/stacking IPC string trap",
			Commands: []string{
				"[con_id=$0] layout stacking",
				"[con_id=$0] splith",
			},
		},
	}
}

// RunScenario spawns the scenario's windows on workspace 7, substitutes
// con-id placeholders, and runs the resulting command sequence through the
// differential engine.
func RunScenario(sw *swayreal.Sway, sc Scenario) (*swayreal.DiffResult, error) {
	if err := sw.FocusWorkspace("7"); err != nil {
		return nil, fmt.Errorf("focus ws7: %w", err)
	}
	ids, err := sw.SpawnWindows(sc.Windows)
	if err != nil {
		return nil, fmt.Errorf("spawn %d windows: %w", sc.Windows, err)
	}
	cmds, err := substitute(sc.Commands, ids)
	if err != nil {
		return nil, err
	}
	res, err := sw.RunScenario(cmds)
	if err != nil {
		return nil, err
	}
	// Tear down this scenario's windows so the next scenario starts clean.
	// Some commands (swap, move-to-mark) may have relocated windows or
	// changed their floating state, so re-discover every live leaf rather
	// than trusting the original spawn ids, then wait for the tree to drain
	// to zero before the next scenario spawns (kill is asynchronous — the
	// client process must exit and sway must emit window::close).
	if err := killAll(sw); err != nil {
		return nil, fmt.Errorf("scenario teardown: %w", err)
	}
	return res, nil
}

// killAll closes every window leaf and waits for the tree to empty.
func killAll(sw *swayreal.Sway) error {
	// Kill in a loop: a single pass can miss windows that were mid-spawn,
	// and killing a tiled master can promote a previously-hidden child.
	for attempt := 0; attempt < 10; attempt++ {
		n, err := sw.LeafCount()
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		// Kill every leaf by walking the tree fresh each pass.
		if err := sw.KillAllLeaves(); err != nil {
			return err
		}
		if err := sw.WaitForLeafCount(0, 8*time.Second); err == nil {
			return nil
		}
	}
	return sw.WaitForLeafCount(0, 8*time.Second)
}

// substitute replaces $i placeholders with the i-th spawned con id.
func substitute(cmds []string, ids []int64) ([]string, error) {
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
