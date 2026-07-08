// fuzz-find-min dumps violations for a single seed.
package main

import (
	"fmt"
	"os"

	"github.com/mschulkind-oss/tilekeeper/internal/harness/fuzz"
)

func main() {
	seed := uint64(1)
	steps := 200
	maxWindows := 6
	if len(os.Args) > 1 {
		fmt.Sscanf(os.Args[1], "%d", &seed)
	}
	if len(os.Args) > 2 {
		fmt.Sscanf(os.Args[2], "%d", &steps)
	}
	if len(os.Args) > 3 {
		fmt.Sscanf(os.Args[3], "%d", &maxWindows)
	}
	cfg := fuzz.DefaultConfig()
	cfg.Seed = seed
	cfg.Steps = steps
	cfg.MaxWindows = maxWindows
	fuzz.SetDumpTreeOnViolation(true)
	var trace []fuzz.StepTrace
	r := fuzz.RunWithTrace(cfg, &trace)
	fmt.Printf("seed=%d steps=%d violations=%d\n", seed, steps, len(r.Violations))
	targetInv := os.Getenv("INVARIANT")
	// Print first violation + command trace of last 5 steps leading to it.
	for _, v := range r.Violations {
		if targetInv != "" && v.Invariant != targetInv {
			continue
		}
		fmt.Printf("step=%d inv=%s detail=%s\n", v.Step, v.Invariant, v.Detail)
		start := 1
		_ = v
		for i := start; i <= v.Step && i < len(trace); i++ {
			st := trace[i]
			if len(st.Cmds) == 0 && st.Event == "" {
				continue
			}
			fmt.Printf("  step %d ev=%s\n", i, st.Event)
			for _, c := range st.Cmds {
				fmt.Printf("    %s\n", c)
			}
		}
		return
	}
}
