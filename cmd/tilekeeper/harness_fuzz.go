package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/mschulkind-oss/tilekeeper/internal/harness/fuzz"
)

// runHarnessFuzz executes a property-based fuzz run and prints results.
// Exit code is non-zero if any invariant violation was observed.
func runHarnessFuzz(args []string) {
	fs := flag.NewFlagSet("harness fuzz", flag.ExitOnError)
	seed := fs.Uint64("seed", 1, "RNG seed")
	steps := fs.Int("steps", 500, "event steps to execute")
	wss := fs.String("workspaces", "7", "comma-separated workspace names to fuzz")
	layoutName := fs.String("layout", "MasterStack", "default layout for fuzzed workspaces")
	maxWindows := fs.Int("max-windows", 6, "cap on concurrent windows per workspace")
	jsonOut := fs.Bool("json", false, "emit results as JSON instead of text")
	_ = fs.Parse(args)

	cfg := fuzz.Config{
		Seed:          *seed,
		Steps:         *steps,
		Workspaces:    strings.Split(*wss, ","),
		MaxWindows:    *maxWindows,
		DefaultLayout: *layoutName,
	}
	res := fuzz.Run(cfg)

	if *jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(res)
	} else {
		fmt.Printf("fuzz: seed=%d steps=%d workspaces=%v\n", cfg.Seed, res.Steps, cfg.Workspaces)
		if len(res.Violations) == 0 {
			fmt.Printf("  no violations\n")
		} else {
			fmt.Printf("  %d violation(s):\n", len(res.Violations))
			for _, v := range res.Violations {
				fmt.Printf("    [%s] step=%d %s\n", v.Invariant, v.Step, v.Detail)
			}
		}
	}

	if len(res.Violations) > 0 {
		os.Exit(1)
	}
}
