// Command fuzz-gate is the CI invariant gate. It runs the deterministic
// reference sweep (fuzz.ReferenceConfig over seeds 1..60), compares each
// invariant's violation count to the checked-in floor (fuzz.LoadFloors),
// and EXITS NON-ZERO if:
//
//   - any invariant exceeds its floor, OR
//   - a brand-new invariant class appears (no floor entry == implicit
//     floor 0 == automatic fail).
//
// Motivation: the 2026-06-14 "master resized to 50%" bug was DETECTED by
// the master-width invariant (firing 56000+ times) yet ignored as noise.
// This gate makes a fuzz regression a hard, proactive CI signal instead of
// tolerated noise — it would have failed CI the day that bug shipped.
//
// Usage:
//
//	go run ./cmd/fuzz-gate           # run the gate (exit 1 on any regression)
//	go run ./cmd/fuzz-gate -json     # machine-readable table
//	go run ./cmd/fuzz-gate -update   # print floors.json reflecting CURRENT
//	                                 # counts (does NOT write — review + paste)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/mschulkind-oss/tilekeeper/internal/harness/fuzz"
)

func main() {
	jsonOut := flag.Bool("json", false, "emit the per-invariant table as JSON")
	update := flag.Bool("update", false, "print a floors.json body reflecting current counts (does not write)")
	flag.Parse()

	floors, err := fuzz.LoadFloors()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fuzz-gate: load floors:", err)
		os.Exit(2)
	}

	start := time.Now()
	sweep := fuzz.RunReferenceSweep()
	elapsed := time.Since(start)

	if *update {
		printUpdate(sweep, floors)
		return
	}

	// Build the union of floor names and observed names so we report both
	// known-clean invariants (count 0, floor 0 -> PASS) and surprises.
	names := map[string]struct{}{}
	for n := range floors.Floor {
		names[n] = struct{}{}
	}
	for n := range sweep.Counts {
		names[n] = struct{}{}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)

	type row struct {
		Invariant string `json:"invariant"`
		Count     int    `json:"count"`
		Floor     int    `json:"floor"`
		Delta     int    `json:"delta"`
		NewClass  bool   `json:"new_class"`
		Status    string `json:"status"`
		Example   string `json:"example,omitempty"`
	}
	var rows []row
	failed := false
	for _, n := range sorted {
		count := sweep.Counts[n]
		floor, listed := floors.Floor[n]
		newClass := !listed
		status := "PASS"
		if newClass && count > 0 {
			status = "FAIL"
			failed = true
		} else if count > floor {
			status = "FAIL"
			failed = true
		}
		r := row{
			Invariant: n,
			Count:     count,
			Floor:     floor,
			Delta:     count - floor,
			NewClass:  newClass,
			Status:    status,
		}
		if status == "FAIL" {
			r.Example = sweep.FirstExample[n]
		}
		rows = append(rows, r)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(struct {
			Config  string `json:"config"`
			Elapsed string `json:"elapsed"`
			Pass    bool   `json:"pass"`
			Rows    []row  `json:"rows"`
		}{
			Config:  referenceConfigStr(),
			Elapsed: elapsed.String(),
			Pass:    !failed,
			Rows:    rows,
		})
		if failed {
			os.Exit(1)
		}
		return
	}

	fmt.Printf("fuzz-gate: reference sweep %s (%.1fs)\n", referenceConfigStr(), elapsed.Seconds())
	fmt.Printf("%-26s %10s %10s %10s   %s\n", "INVARIANT", "COUNT", "FLOOR", "DELTA", "STATUS")
	fmt.Printf("%-26s %10s %10s %10s   %s\n", "---------", "-----", "-----", "-----", "------")
	for _, r := range rows {
		marker := ""
		if r.NewClass {
			marker = " (NEW CLASS, implicit floor 0)"
		}
		fmt.Printf("%-26s %10d %10d %+10d   %s%s\n", r.Invariant, r.Count, r.Floor, r.Delta, r.Status, marker)
	}

	if failed {
		fmt.Println()
		fmt.Println("GATE FAILED. Regressed / new invariant classes (minimized first reproducer):")
		for _, r := range rows {
			if r.Status != "FAIL" {
				continue
			}
			label := "regressed"
			if r.NewClass {
				label = "NEW CLASS"
			}
			fmt.Printf("  [%s] %s (count=%d floor=%d delta=%+d)\n", label, r.Invariant, r.Count, r.Floor, r.Delta)
			fmt.Printf("      %s\n", r.Example)
			if j := floors.Justification[r.Invariant]; j != "" {
				fmt.Printf("      floor justification: %s\n", j)
			}
		}
		fmt.Println()
		fmt.Println("If this is an INTENTIONAL increase (e.g. a new aggressive generator), update")
		fmt.Println("internal/harness/fuzz/floors.json (see `go run ./cmd/fuzz-gate -update`) WITH a")
		fmt.Println("justification. If it's a regression, fix the bug — do not raise the floor.")
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("GATE PASSED: no invariant exceeds its floor; no new invariant classes.")
}

func referenceConfigStr() string {
	return fmt.Sprintf("seeds %d..%d x %d steps x MaxWindows %d (MasterStack, ws 7/8)",
		fuzz.ReferenceSeedLo, fuzz.ReferenceSeedHi, fuzz.ReferenceSteps, fuzz.ReferenceMaxWindows)
}

// printUpdate emits a floors map reflecting the CURRENT measured counts, so
// a maintainer ratcheting the baseline (up for a new generator, or down
// after a fix) can paste it into floors.json and then edit justifications.
func printUpdate(sweep fuzz.SweepResult, floors fuzz.Floors) {
	names := map[string]struct{}{}
	for n := range floors.Floor {
		names[n] = struct{}{}
	}
	for n := range sweep.Counts {
		names[n] = struct{}{}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)

	out := map[string]int{}
	for _, n := range sorted {
		out[n] = sweep.Counts[n]
	}
	b, _ := json.MarshalIndent(out, "  ", "  ")
	fmt.Println("// current measured counts for floors.json \"floors\" map:")
	fmt.Printf("  \"floors\": %s\n", string(b))
	fmt.Println("// (justifications must be written/updated by hand)")
}
