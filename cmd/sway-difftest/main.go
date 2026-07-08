// Command sway-difftest runs the differential test of the in-memory sim
// (internal/harness/sim) against a real headless sway. It boots sway, spawns
// windows, then drives a battery of command scenarios — the same command
// vocabulary the sim's apply.go handles — through BOTH the sim (seeded from
// sway's real tree) and live sway, diffing their trees after each command.
//
// Every escaped production bug (ctrl-s float bleed, master-width-50%) was the
// sim silently diverging from real sway; this finds such divergences
// automatically. The scenarios in scenarios.go deliberately target the
// command classes those bugs came from: swap with a floating endpoint,
// move-to-mark with floating source/dest, resize set width ppt/px, split
// none flatten, and layout tabbed on a nested tree.
//
// Usage:
//
//	go run ./cmd/sway-difftest             # run all scenarios, text report
//	go run ./cmd/sway-difftest --json      # machine-readable report
//	go run ./cmd/sway-difftest --scenario swap-floating-endpoint
//
// Exits non-zero if any scenario diverges (so CI / a justfile target can
// gate on it), and exits 0 with a skip notice if no sway binary is found.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/mschulkind-oss/tilekeeper/internal/harness/swayreal"
)

func main() {
	var (
		jsonOut = flag.Bool("json", false, "emit JSON report")
		only    = flag.String("scenario", "", "run only the named scenario")
		verbose = flag.Bool("v", false, "print every command and the trees on divergence")
	)
	flag.Parse()

	sw, err := swayreal.Start(swayreal.Options{})
	if err != nil {
		if err == swayreal.ErrNoSway {
			fmt.Fprintln(os.Stderr, "sway-difftest: no sway binary found — skipping (this is not a failure)")
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "sway-difftest: failed to start sway: %v\n", err)
		os.Exit(2)
	}
	defer sw.Close()

	scenarios := AllScenarios()
	if *only != "" {
		filtered := scenarios[:0]
		for _, s := range scenarios {
			if s.Name == *only {
				filtered = append(filtered, s)
			}
		}
		if len(filtered) == 0 {
			fmt.Fprintf(os.Stderr, "sway-difftest: no scenario named %q\n", *only)
			os.Exit(2)
		}
		scenarios = filtered
	}

	type report struct {
		Scenario   string   `json:"scenario"`
		Windows    int      `json:"windows"`
		Diverged   bool     `json:"diverged"`
		DivergedAt int      `json:"diverged_at"`
		Command    string   `json:"command,omitempty"`
		Detail     string   `json:"detail,omitempty"`
		KnownGap   string   `json:"known_gap,omitempty"`
		Commands   []string `json:"commands,omitempty"`
	}
	var reports []report
	anyUnexpected := false

	for _, sc := range scenarios {
		res, err := RunScenario(sw, sc)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sway-difftest: scenario %q error: %v\n", sc.Name, err)
			os.Exit(2)
		}
		r := report{
			Scenario:   sc.Name,
			Windows:    sc.Windows,
			Diverged:   res.Diverged,
			DivergedAt: res.DivergedAt,
			Command:    res.DivergeCmd,
			Detail:     res.Detail,
			KnownGap:   sc.KnownGap,
			Commands:   res.Commands,
		}
		reports = append(reports, r)

		// A divergence fails the run UNLESS the scenario is annotated as a
		// known, deliberately-unmodeled gap. A known-gap scenario that
		// unexpectedly STOPS diverging also fails — so the annotation gets
		// removed once the sim is made faithful.
		switch {
		case res.Diverged && sc.KnownGap == "":
			anyUnexpected = true
		case !res.Diverged && sc.KnownGap != "":
			anyUnexpected = true
		}

		if !*jsonOut {
			status := "OK   "
			switch {
			case res.Diverged && sc.KnownGap != "":
				status = "GAP  "
			case res.Diverged:
				status = "DIFF "
			case sc.KnownGap != "":
				status = "FIXED" // known gap no longer diverges — annotation stale
			}
			fmt.Printf("[%s] %-28s windows=%d cmds=%d\n", status, sc.Name, sc.Windows, len(res.Commands))
			if res.Diverged {
				fmt.Printf("        diverged at cmd #%d: %q\n", res.DivergedAt, res.DivergeCmd)
				fmt.Printf("        detail: %s\n", res.Detail)
				if sc.KnownGap != "" {
					fmt.Printf("        KNOWN GAP (tolerated): %s\n", sc.KnownGap)
				}
				fmt.Printf("        --- sim tree ---\n%s", indent(res.SimSubtree, "        "))
				fmt.Printf("        --- sway tree ---\n%s", indent(res.SwaySubtree, "        "))
			}
			if status == "FIXED" {
				fmt.Printf("        scenario no longer diverges — remove KnownGap annotation\n")
			}
			if *verbose {
				for i, c := range res.Commands {
					mark := "  "
					if i == res.DivergedAt {
						mark = ">>"
					}
					fmt.Printf("        %s [%d] %s\n", mark, i, c)
				}
			}
		}
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(reports)
	}

	if anyUnexpected {
		os.Exit(1)
	}
}

// indent prefixes every line of s with pad and ensures a trailing newline.
func indent(s, pad string) string {
	if s == "" {
		return ""
	}
	out := ""
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out += pad + s[start:i+1]
			start = i + 1
		}
	}
	if start < len(s) {
		out += pad + s[start:] + "\n"
	}
	return out
}
