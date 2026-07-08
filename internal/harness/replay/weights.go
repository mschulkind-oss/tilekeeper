package replay

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mschulkind-oss/tilekeeper/internal/capture"
)

// Weights is the mined operation-distribution profile: how often each kind
// of sway event appeared in a real session, plus the two structural splits
// the fuzz generator distinguishes (container-vs-leaf moves, dialog-style
// new→floating bursts). It is the bridge from "what the user actually does"
// to "how the generator should weight its random events", so the fuzzer can
// spend its budget where real usage spends it instead of uniformly.
type Weights struct {
	// Total events tallied.
	Total int
	// Counts per op_name: new, close, focus, floating, move, binding,
	// plus workspace (init/focus) and fullscreen. Keyed by the generator's
	// own kind names where they exist.
	Counts map[string]int
	// Moves split by whether they relocated a single leaf or a multi-window
	// subtree across workspaces (the fellow-traveler shape).
	LeafMoves      int
	ContainerMoves int
	SameWSMoves    int
	// DialogBursts counts window::new events immediately followed (next
	// event, same con_id) by a window::floating — the portal/1Password
	// dialog shape genDialogNew models.
	DialogBursts int
	// BindingCommands tallies each distinct binding command seen, so the
	// generator's bindingCorpus can be reweighted toward the verbs the user
	// actually presses.
	BindingCommands map[string]int
}

// MineCapture computes Weights from a parsed capture File. It walks the
// event stream in order so it can detect new→floating dialog bursts and
// classify moves by reconstructing just enough adjacency (a move's WS field
// vs. the container's previously-seen WS distinguishes same-workspace echoes
// from real cross-workspace relocations; a subtree move is inferred when the
// representative leaf's workspace held >1 window before the move).
func MineCapture(f *capture.File) *Weights {
	w := &Weights{
		Counts:          map[string]int{},
		BindingCommands: map[string]int{},
	}
	// Track each container's last-known workspace and the per-workspace
	// window count, so moves can be classified leaf-vs-container without a
	// full tree reconstruction.
	conWS := map[int64]string{}
	wsCount := map[string]int{}
	if f.Meta != nil && f.Meta.Tree != nil {
		for _, ws := range f.Meta.Tree.Workspaces() {
			for _, l := range ws.Leaves() {
				if l != nil && l.Type == "con" {
					conWS[l.ID] = ws.Name
					wsCount[ws.Name]++
				}
			}
		}
	}

	for i, er := range f.Events {
		w.Total++
		switch er.Type {
		case "binding":
			w.Counts["binding"]++
			if er.Binding != "" {
				w.BindingCommands[er.Binding]++
			}
			continue
		case "workspace":
			w.Counts["workspace"]++
			continue
		case "window":
			// fall through
		default:
			w.Counts[er.Type]++
			continue
		}

		id := int64(0)
		if er.Container != nil {
			id = er.Container.ID
		}
		switch er.Change {
		case "new":
			w.Counts["new"]++
			if er.WS != "" {
				conWS[id] = er.WS
				wsCount[er.WS]++
			}
			// Dialog burst: next event is a floating on the same container.
			if i+1 < len(f.Events) {
				nx := f.Events[i+1]
				if nx.Type == "window" && nx.Change == "floating" &&
					nx.Container != nil && nx.Container.ID == id {
					w.DialogBursts++
				}
			}
		case "close":
			w.Counts["close"]++
			if ws, ok := conWS[id]; ok {
				if wsCount[ws] > 0 {
					wsCount[ws]--
				}
				delete(conWS, id)
			}
		case "focus":
			w.Counts["focus"]++
		case "floating":
			w.Counts["floating"]++
		case "fullscreen_mode":
			w.Counts["fullscreen"]++
		case "move":
			w.Counts["move"]++
			old := conWS[id]
			dest := er.WS
			switch {
			case dest == "" || dest == old:
				w.SameWSMoves++
			case wsCount[old] > 1:
				// Source workspace held more than just this window — a
				// move-to-workspace of a focused column would carry the
				// fellow-travelers. Best-effort classification.
				w.ContainerMoves++
			default:
				w.LeafMoves++
			}
			if dest != "" && dest != old {
				// Reflect the relocation in the per-ws counts.
				if wsCount[old] > 0 {
					wsCount[old]--
				}
				wsCount[dest]++
				conWS[id] = dest
			}
		default:
			w.Counts[er.Change]++
		}
	}
	return w
}

// GeneratorWeights maps the mined Counts onto the fuzz generator's kind
// names and returns integer weights normalized so the smallest non-zero kind
// is at least 1. These are the numbers to paste into generateEvents' switch.
//
// The generator's kinds: new, close, focus, binding, floating, move
// (same-workspace echo), wsmove (single-leaf cross-ws move), fullscreen,
// containermove (fellow-traveler subtree move), dialognew (new→floating
// burst). We derive each from the mined tallies:
//
//	new        = new events that were NOT the head of a dialog burst
//	dialognew  = DialogBursts
//	move       = SameWSMoves
//	wsmove     = LeafMoves
//	containermove = ContainerMoves
func (w *Weights) GeneratorWeights() map[string]int {
	raw := map[string]int{
		"new":           max0(w.Counts["new"] - w.DialogBursts),
		"dialognew":     w.DialogBursts,
		"close":         w.Counts["close"],
		"focus":         w.Counts["focus"],
		"binding":       w.Counts["binding"],
		"floating":      w.Counts["floating"],
		"move":          w.SameWSMoves,
		"wsmove":        w.LeafMoves,
		"containermove": w.ContainerMoves,
		"fullscreen":    w.Counts["fullscreen"],
	}
	// Normalize: scale so the smallest non-zero weight becomes 1, rounding
	// the rest. Keeps the printed weights small and switch-friendly.
	minNon := 0
	for _, v := range raw {
		if v > 0 && (minNon == 0 || v < minNon) {
			minNon = v
		}
	}
	if minNon == 0 {
		return raw
	}
	out := map[string]int{}
	for k, v := range raw {
		if v == 0 {
			out[k] = 0
			continue
		}
		n := (v + minNon/2) / minNon
		if n < 1 {
			n = 1
		}
		out[k] = n
	}
	return out
}

func max0(x int) int {
	if x < 0 {
		return 0
	}
	return x
}

// String renders a human-readable report of the mined distribution and the
// derived generator weights, including a copy-paste note on how to apply
// them to internal/harness/fuzz/generator.go.
func (w *Weights) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "mined operation distribution (%d events)\n", w.Total)
	fmt.Fprintf(&b, "  raw op_name counts:\n")
	for _, k := range sortedKeys(w.Counts) {
		fmt.Fprintf(&b, "    %-12s %6d  (%5.1f%%)\n", k, w.Counts[k], pct(w.Counts[k], w.Total))
	}
	fmt.Fprintf(&b, "  move classification:\n")
	fmt.Fprintf(&b, "    %-12s %6d\n", "same-ws", w.SameWSMoves)
	fmt.Fprintf(&b, "    %-12s %6d\n", "leaf-cross", w.LeafMoves)
	fmt.Fprintf(&b, "    %-12s %6d\n", "container", w.ContainerMoves)
	fmt.Fprintf(&b, "    %-12s %6d  (new→floating bursts)\n", "dialog", w.DialogBursts)
	if len(w.BindingCommands) > 0 {
		fmt.Fprintf(&b, "  binding commands:\n")
		for _, k := range sortedKeys(w.BindingCommands) {
			fmt.Fprintf(&b, "    %-28s %6d\n", k, w.BindingCommands[k])
		}
	}
	gw := w.GeneratorWeights()
	fmt.Fprintf(&b, "  derived generator weights (kind=weight):\n")
	for _, k := range sortedKeys(gw) {
		fmt.Fprintf(&b, "    %-14s %d\n", k, gw[k])
	}
	fmt.Fprintf(&b, "\napply: set these as the case bounds in\n")
	fmt.Fprintf(&b, "  internal/harness/fuzz/generator.go generateEvents()\n")
	fmt.Fprintf(&b, "  (replace the hardcoded `new=4,close=2,...` weights and the\n")
	fmt.Fprintf(&b, "  matching cumulative `n < K` thresholds). A weight of 0 keeps\n")
	fmt.Fprintf(&b, "  the kind for coverage but should stay >=1 unless the kind is\n")
	fmt.Fprintf(&b, "  truly absent from real usage.\n")
	return b.String()
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func pct(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return 100 * float64(n) / float64(total)
}
