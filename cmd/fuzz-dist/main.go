// fuzz-dist reports per-seed violation counts, sorted descending.
package main

import (
	"fmt"
	"sort"

	"github.com/mschulkind-oss/tilekeeper/internal/harness/fuzz"
)

func main() {
	type row struct {
		seed  uint64
		count int
	}
	var rows []row
	total := 0
	for seed := uint64(1); seed <= 200; seed++ {
		cfg := fuzz.DefaultConfig()
		cfg.Seed = seed
		cfg.Steps = 5000
		cfg.MaxWindows = 12
		r := fuzz.Run(cfg)
		n := 0
		for _, v := range r.Violations {
			if v.Invariant == "no-wrapper-chain" {
				n++
			}
		}
		total += n
		if n > 0 {
			rows = append(rows, row{seed, n})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].count > rows[j].count })
	fmt.Printf("total=%d seeds_with_violations=%d\n", total, len(rows))
	for _, r := range rows {
		fmt.Printf("  seed=%d count=%d\n", r.seed, r.count)
	}
}
