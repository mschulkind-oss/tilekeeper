package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/mschulkind-oss/tilekeeper/internal/harness/fuzz"
)

func main() {
	cfg := fuzz.DefaultConfig()
	cfg.Seed = 2
	cfg.Steps = 500
	if len(os.Args) > 1 {
		if n, err := strconv.ParseUint(os.Args[1], 10, 64); err == nil {
			cfg.Seed = n
		}
	}
	res := fuzz.Run(cfg)
	seen := map[string]bool{}
	for _, v := range res.Violations {
		key := fmt.Sprintf("%s step=%d", v.Invariant, v.Step)
		if seen[key] {
			continue
		}
		seen[key] = true
		fmt.Printf("%s step=%d detail=%s\n", v.Invariant, v.Step, v.Detail)
		if len(seen) >= 15 {
			return
		}
	}
}
