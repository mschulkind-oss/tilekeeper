// Command replay-journal turns a captured (or journal-derived) sway event
// stream into a fuzzer regression run, and mines the real operation
// distribution to weight the generator toward reality.
//
// Inputs (pick one):
//
//	<file.jsonl>          a daemon capture (TK_EVENT_CAPTURE output) — the
//	                      faithful source; preferred.
//	--journal <file>      a text journal exported via journalctl — a
//	                      best-effort, lossy fallback (no container snapshots
//	                      or tree shape; warns and skips unreconstructable
//	                      events rather than crashing).
//
// Modes:
//
//	(default)             replay the stream through a fresh sim+Hub and run
//	                      the SAME invariants the fuzzer uses; report any
//	                      violation with its originating event + seq, so a
//	                      real incident becomes a precise repro. Exit 1 on
//	                      any violation.
//	--weights             skip replay; tally the observed op_name
//	                      distribution and print generator weights, so the
//	                      fuzz generator can be tuned to match real usage.
//
// Usage examples:
//
//	replay-journal session.jsonl
//	replay-journal --journal lm.log
//	replay-journal --weights session.jsonl
//	replay-journal --json session.jsonl
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/mschulkind-oss/tilekeeper/internal/capture"
	"github.com/mschulkind-oss/tilekeeper/internal/harness/replay"
)

func main() {
	fs := flag.NewFlagSet("replay-journal", flag.ExitOnError)
	journalPath := fs.String("journal", "", "read a text journal (journalctl export) instead of a JSONL capture — lossy, best-effort")
	weightsMode := fs.Bool("weights", false, "mine the operation distribution and print generator weights instead of replaying")
	jsonOut := fs.Bool("json", false, "emit results as JSON")
	layoutName := fs.String("layout", "", "override default layout for replayed workspaces (default: from capture meta, else MasterStack)")
	masterWidth := fs.Int("master-width", 0, "override MasterStack masterWidth percent (default: from capture meta, else 75)")
	workspaces := fs.String("workspaces", "", "comma-separated workspace allow-list (default: infer from capture)")
	_ = fs.Parse(os.Args[1:])

	// Resolve the input into a capture.File. Either an explicit --journal or
	// the first positional argument (a JSONL capture).
	var f *capture.File
	var srcDesc string
	switch {
	case *journalPath != "":
		jf, skipped, err := replay.JournalFileToCapture(*journalPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "replay-journal: read journal: %v\n", err)
			os.Exit(2)
		}
		f = jf
		srcDesc = fmt.Sprintf("journal %s (lossy; %d lines skipped, %d events)", *journalPath, skipped, len(f.Events))
	default:
		args := fs.Args()
		if len(args) < 1 {
			fmt.Fprintln(os.Stderr, "replay-journal: need a capture file path (or --journal <file>)")
			fs.Usage()
			os.Exit(2)
		}
		cf, err := capture.ReadFile(args[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "replay-journal: read capture: %v\n", err)
			os.Exit(2)
		}
		f = cf
		srcDesc = fmt.Sprintf("capture %s (%d events, %d lines skipped)", args[0], len(f.Events), f.SkippedLines)
	}

	if *weightsMode {
		runWeights(f, srcDesc, *jsonOut)
		return
	}
	runReplay(f, srcDesc, *jsonOut, replay.Options{
		DefaultLayout: *layoutName,
		MasterWidth:   *masterWidth,
		Workspaces:    splitCSV(*workspaces),
	})
}

func runWeights(f *capture.File, srcDesc string, jsonOut bool) {
	w := replay.MineCapture(f)
	if jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"source":            srcDesc,
			"total":             w.Total,
			"counts":            w.Counts,
			"same_ws_moves":     w.SameWSMoves,
			"leaf_moves":        w.LeafMoves,
			"container_moves":   w.ContainerMoves,
			"dialog_bursts":     w.DialogBursts,
			"binding_commands":  w.BindingCommands,
			"generator_weights": w.GeneratorWeights(),
		})
		return
	}
	fmt.Printf("source: %s\n", srcDesc)
	fmt.Print(w.String())
}

func runReplay(f *capture.File, srcDesc string, jsonOut bool, opts replay.Options) {
	res, err := replay.Run(f, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "replay-journal: replay failed: %v\n", err)
		os.Exit(2)
	}

	if jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(res)
	} else {
		fmt.Printf("source: %s\n", srcDesc)
		fmt.Printf("replay: events=%d steps=%d workspaces=%v\n", res.Events, res.Steps, res.Workspaces)
		if res.SkippedLines > 0 {
			fmt.Printf("  %d malformed/unknown capture line(s) skipped\n", res.SkippedLines)
		}
		if len(res.Warnings) > 0 {
			fmt.Printf("  %d reconstruction warning(s) (lossy input):\n", len(res.Warnings))
			for _, wn := range res.Warnings {
				fmt.Printf("    seq=%d %s:%s %s\n", wn.Seq, wn.Type, wn.Change, wn.Reason)
			}
		}
		if len(res.Violations) == 0 {
			fmt.Printf("  no violations — replay clean\n")
		} else {
			fmt.Printf("  %d violation(s):\n", len(res.Violations))
			for _, v := range res.Violations {
				fmt.Printf("    [%s] step=%d seq=%d %s\n", v.Invariant, v.Step, v.Event.Seq, v.Detail)
			}
		}
	}

	if len(res.Violations) > 0 {
		os.Exit(1)
	}
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}
