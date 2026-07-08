package main

import (
	"fmt"
	"github.com/mschulkind-oss/tilekeeper/internal/harness/fuzz"
)

func main() {
	counts := map[string]int{}
	samples := map[string]string{}
	for seed := uint64(1); seed <= 200; seed++ {
		cfg := fuzz.DefaultConfig()
		cfg.Seed = seed
		cfg.Steps = 5000
		cfg.MaxWindows = 12
		r := fuzz.Run(cfg)
		for _, v := range r.Violations {
			counts[v.Invariant]++
			if _, ok := samples[v.Invariant]; !ok {
				samples[v.Invariant] = fmt.Sprintf("seed=%d step=%d detail=%s", seed, v.Step, v.Detail)
			}
		}
	}
	fmt.Println("counts:")
	for k, v := range counts {
		fmt.Printf("  %s: %d\n", k, v)
	}
	fmt.Println("samples:")
	for k, v := range samples {
		fmt.Printf("  %s: %s\n", k, v)
	}
}
