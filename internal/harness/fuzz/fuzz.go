// Package fuzz implements a property-based fuzzer for the layout
// decision logic. It generates random but valid event sequences, drives
// them through a (sim, hub) pair, and checks invariants after each step.
//
// Invariants checked:
//
//   - no-crash: HandleEvent never panics.
//   - no-invalid-cmd: sim never records an ErrUnsupportedCommand.
//   - no-sway-reject: sim never records a command real sway would refuse
//     (e.g. `split none` on a node with siblings).
//   - focus-convergence: after any sequence, there is ≤1 focused leaf.
//   - no-wrapper-chain: no path from a workspace to any descendant passes
//     through more than maxWrapperChain consecutive singleton structural
//     containers. Real sway flattens singleton chains on detach
//     (container_flatten in sway/tree/container.c), so any persistent
//     chain indicates a layout manager creating wrappers faster than they
//     collapse — the live "12-deep nesting on ws7" bug.
//   - idempotent-arrange: calling ArrangeAll twice issues no new commands
//     beyond the first invocation on each workspace.
//
// Order-independence is deferred (it requires generating N equivalent
// permutations and comparing canonicalized end states, which is more
// plumbing than we need for v1).
package fuzz

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"sync"

	"github.com/mschulkind-oss/tilekeeper/internal/config"
	"github.com/mschulkind-oss/tilekeeper/internal/harness/sim"
	"github.com/mschulkind-oss/tilekeeper/internal/layout"
	"github.com/mschulkind-oss/tilekeeper/internal/sway"
	"github.com/mschulkind-oss/tilekeeper/internal/workspace"
)

// Config tunes a fuzz run.
type Config struct {
	Seed          uint64
	Steps         int
	Workspaces    []string
	MaxWindows    int    // cap per workspace
	DefaultLayout string // "MasterStack", "tabbed", etc.

	// DropRate is the per-step probability (0..1) that a generated event
	// has its side effects applied to the sim but is NOT dispatched to the
	// hub — modeling the daemon's bounded event channel overflowing on a
	// burst. Drops cause `tracked-matches-leaves` to fire because manager
	// tracking diverges from the sim tree. The production code has NO
	// recovery from drops; the only correct response is to prevent them
	// (larger buffer, subscribe-side filter for high-volume no-op events).
	// This knob exists so anyone weakening those preventions can see what
	// breaks.
	DropRate float64
}

// DefaultConfig returns a reasonable starting config.
func DefaultConfig() Config {
	return Config{
		Seed:          1,
		Steps:         200,
		Workspaces:    []string{"7", "8"},
		MaxWindows:    6,
		DefaultLayout: "MasterStack",
	}
}

// Violation records a single invariant failure.
type Violation struct {
	Invariant string
	Step      int
	Event     sway.Event
	Detail    string
}

// Result is the outcome of a fuzz run.
type Result struct {
	Config     Config
	Steps      int
	Violations []Violation
}

// StepTrace holds the event and sway commands for a single fuzz step.
type StepTrace struct {
	Event string
	Cmds  []string
}

// Run executes a single fuzz iteration with the given config.
func Run(cfg Config) *Result {
	return RunWithTrace(cfg, nil)
}

// RunWithTrace is Run with an optional per-step command tracer. When
// trace != nil, each emitted sway command is appended to trace[stepIdx].
// Step 0 holds workspace-init commands; steps 1..cfg.Steps hold one
// event's worth of commands each.
func RunWithTrace(cfg Config, trace *[]StepTrace) *Result {
	rng := rand.New(rand.NewPCG(cfg.Seed, cfg.Seed^0x9E3779B97F4A7C15))
	// dropRng is a SEPARATE PRNG so adding DropRate doesn't shift the
	// event-generation RNG stream. With shared RNG, every Float64() call
	// for the drop decision advanced rng and produced a completely
	// different event sequence vs. baseline (DropRate=0), making
	// "baseline vs. drop+resync" violation counts apples-to-oranges. With
	// a separate stream, generated events are identical and we can
	// attribute new violations purely to the drop/recovery path.
	dropRng := rand.New(rand.NewPCG(cfg.Seed^0xD2A5_5E5F_6B3F_8D11, cfg.Seed))
	s := sim.New()
	curStep := 0
	if trace != nil {
		*trace = make([]StepTrace, cfg.Steps+1)
		s.TraceSink = func(cmd string) {
			if curStep >= 0 && curStep < len(*trace) {
				(*trace)[curStep].Cmds = append((*trace)[curStep].Cmds, cmd)
			}
		}
	}
	errSink := &errorCaptureHandler{}
	logger := slog.New(errSink)

	wsCfg := map[string]config.WorkspaceConfig{}
	for _, name := range cfg.Workspaces {
		wsCfg[name] = config.WorkspaceConfig{DefaultLayout: cfg.DefaultLayout}
	}
	hub := workspace.NewHub(s, config.Config{
		General:    config.GeneralConfig{DefaultLayout: "none", MasterWidth: 75},
		Workspaces: wsCfg,
	}, logger)
	hub.Initialize()

	state := newFuzzState(cfg.Workspaces)
	res := &Result{Config: cfg}

	// Seed each workspace as "init".
	for _, ws := range cfg.Workspaces {
		ev := state.initWorkspace(s, ws)
		runStep(hub, s, ev, 0, "workspace:init", true, res)
	}

	for i := 1; i <= cfg.Steps; i++ {
		curStep = i

		events := state.generateEvents(rng, s, cfg.MaxWindows)
		if len(events) == 0 {
			continue
		}
		res.Steps++
		// A step may carry a BURST of events (dialognew: window::new with a
		// stale snapshot + window::floating). Burst members dispatch in
		// order with no tree mutation in between — modeling sway emitting
		// faster than the daemon's single-threaded loop consumes.
		for _, ev := range events {
			desc := describe(ev)
			drop := cfg.DropRate > 0 && dropRng.Float64() < cfg.DropRate
			if drop {
				desc = "[dropped] " + desc
			}
			if trace != nil {
				if (*trace)[i].Event != "" {
					(*trace)[i].Event += " + "
				}
				(*trace)[i].Event += desc
			}
			// Match real sway: window::close fires AFTER the container is
			// destroyed. Subscribers that query the tree see the post-close
			// shape, and the event Container has Parent=nil (IPC JSON has no
			// parent). Detach first, then dispatch — the hub's
			// findWorkspaceForContainer wsForCon fallback handles the
			// now-unresolvable Container.
			//
			// Sway destroys the container regardless of whether tilekeeper
			// dispatches the event, so the detach happens even on drops.
			// Event payloads are snapshots (Node.Snapshot), so resolve the
			// LIVE node for the tree mutation.
			if ev.Type == "window" && ev.Change == "close" && ev.Container != nil {
				if live := state.windows[ev.Container.ID]; live != nil {
					s.CloseLeaf(live)
				}
				delete(state.windows, ev.Container.ID)
			}
			runStep(hub, s, ev, i, desc, !drop, res)
			// No recovery from drops by design — drops are bugs we prevent
			// upstream (channel buffer size, subscribe-side filter). The
			// invariant below will fire when DropRate > 0, documenting the
			// cost of letting events slip.
			checkTrackedMatchesLeaves(hub, s, cfg.Workspaces, ev, i, res)
			checkMasterWidthHonored(hub, s, cfg.Workspaces, ev, i, res)
			checkMasterStackSplit(hub, s, cfg.Workspaces, ev, i, res)
			// Surface any Hub-logged Error after the step. These cover
			// "command failed" / "unknown command" rejections from layout
			// managers — i.e. a binding the user has bound that lm cannot
			// service.
			for _, msg := range errSink.drain() {
				res.Violations = append(res.Violations, Violation{
					Invariant: "no-handler-error",
					Step:      i,
					Event:     ev,
					Detail:    msg,
				})
			}
		}
	}

	return res
}

// errorCaptureHandler is a slog.Handler that records Error-level log
// messages so the fuzzer can assert no handler returned an error mid-run.
type errorCaptureHandler struct {
	mu   sync.Mutex
	msgs []string
}

func (h *errorCaptureHandler) Enabled(_ context.Context, lvl slog.Level) bool {
	return lvl >= slog.LevelError
}

func (h *errorCaptureHandler) Handle(_ context.Context, r slog.Record) error {
	var b []byte
	b = append(b, r.Message...)
	r.Attrs(func(a slog.Attr) bool {
		b = append(b, ' ')
		b = append(b, a.Key...)
		b = append(b, '=')
		b = fmt.Appendf(b, "%v", a.Value.Any())
		return true
	})
	h.mu.Lock()
	h.msgs = append(h.msgs, string(b))
	h.mu.Unlock()
	return nil
}

func (h *errorCaptureHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *errorCaptureHandler) WithGroup(_ string) slog.Handler      { return h }

func (h *errorCaptureHandler) drain() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.msgs
	h.msgs = nil
	return out
}

// runStep optionally delivers ev to the hub (dispatch=false skips the
// dispatch to model a daemon-side event drop), recovers panics
// (no-crash invariant), then runs the other invariants.
func runStep(hub *workspace.Hub, s *sim.SimSwayClient, ev sway.Event, step int, desc string, dispatch bool, res *Result) {
	defer func() {
		if r := recover(); r != nil {
			res.Violations = append(res.Violations, Violation{
				Invariant: "no-crash",
				Step:      step,
				Event:     ev,
				Detail:    fmt.Sprintf("panic: %v (event=%s)", r, desc),
			})
		}
	}()
	if dispatch {
		hub.HandleEvent(ev)
	}
	checkInvariants(s, ev, step, res)
}

// checkInvariants runs the always-on invariants after each step.
func checkInvariants(s *sim.SimSwayClient, ev sway.Event, step int, res *Result) {
	if n := len(s.UnsupportedCommands); n > 0 {
		res.Violations = append(res.Violations, Violation{
			Invariant: "no-invalid-cmd",
			Step:      step,
			Event:     ev,
			Detail:    fmt.Sprintf("sim recorded unsupported: %q", s.UnsupportedCommands[n-1]),
		})
		// Clear so we only report each instance once.
		s.UnsupportedCommands = s.UnsupportedCommands[:0]
	}
	if n := len(s.SwayRejections); n > 0 {
		res.Violations = append(res.Violations, Violation{
			Invariant: "no-sway-reject",
			Step:      step,
			Event:     ev,
			Detail:    fmt.Sprintf("sway would reject: %s", s.SwayRejections[n-1]),
		})
		s.SwayRejections = s.SwayRejections[:0]
	}
	tree, _ := s.GetTree()
	if tree == nil {
		return
	}
	if focused := countFocused(tree); focused > 1 {
		res.Violations = append(res.Violations, Violation{
			Invariant: "focus-convergence",
			Step:      step,
			Event:     ev,
			Detail:    fmt.Sprintf("%d focused leaves, want ≤ 1", focused),
		})
	}
	if depth, path := longestSingletonChain(tree); depth > maxWrapperChain {
		detail := fmt.Sprintf("singleton chain depth=%d (limit=%d) path=%s", depth, maxWrapperChain, path)
		if dumpTreeOnViolation {
			detail += "\ntree:\n" + dumpTreeStr(tree)
		}
		res.Violations = append(res.Violations, Violation{
			Invariant: "no-wrapper-chain",
			Step:      step,
			Event:     ev,
			Detail:    detail,
		})
	}
}

// dumpTreeOnViolation enables tree snapshots in Violation.Detail for the
// no-wrapper-chain invariant. Toggled on by debug tooling (fuzz-find-min)
// so the first reproducer shows the shape without re-running the fuzz.
var dumpTreeOnViolation = false

func dumpTreeStr(n *sway.Node) string {
	var b []byte
	var walk func(n *sway.Node, depth int)
	walk = func(n *sway.Node, depth int) {
		for range depth {
			b = append(b, ' ', ' ')
		}
		b = fmt.Appendf(b, "[%d] %s %s n=%d\n", n.ID, n.Type, n.Layout, len(n.Nodes))
		for _, c := range n.Nodes {
			walk(c, depth+1)
		}
	}
	walk(n, 0)
	return string(b)
}

// SetDumpTreeOnViolation toggles tree snapshots on the no-wrapper-chain
// invariant's Detail field, for diagnostic tooling.
func SetDumpTreeOnViolation(on bool) { dumpTreeOnViolation = on }

// maxWrapperChain bounds the length of a persistent chain of singleton
// structural containers. MasterStack legitimately creates a single splitv
// wrapper around the stack when the first stack window appears, which
// collapses once a second stack window is added. A chain longer than 1
// implies a layout manager is wrapping a container that is already a
// singleton wrapper — real sway's container_split (container.c:1512-1530)
// avoids this by updating the existing parent's layout instead of adding
// a new wrapper.
const maxWrapperChain = 1

// longestSingletonChain walks every workspace and returns the longest
// chain of consecutive singleton structural ("con" type, len(Nodes)==1)
// containers on any root-to-leaf path, plus a "/"-joined description of
// that chain for diagnostics.
func longestSingletonChain(root *sway.Node) (int, string) {
	worst := 0
	var worstPath []string
	for _, ws := range root.Workspaces() {
		walkChain(ws, 0, nil, &worst, &worstPath)
	}
	return worst, fmt.Sprintf("%v", worstPath)
}

func walkChain(n *sway.Node, chain int, path []string, worst *int, worstPath *[]string) {
	if n == nil {
		return
	}
	newChain := chain
	newPath := path
	if n.Type == "con" && len(n.Nodes) == 1 {
		newChain = chain + 1
		newPath = append(append([]string(nil), path...),
			fmt.Sprintf("%d/%s", n.ID, n.Layout))
	} else if n.Type == "con" {
		newChain = 0
		newPath = nil
	}
	if newChain > *worst {
		*worst = newChain
		*worstPath = newPath
	}
	for _, c := range n.Nodes {
		walkChain(c, newChain, newPath, worst, worstPath)
	}
}

// checkTrackedMatchesLeaves enforces that, for each configured workspace,
// the manager's tracked window id set matches the sim tree's non-excluded
// leaf set exactly. Divergence means either the manager dropped/kept a
// stale id (tracking bug) or the tree and tracking diverged silently
// (e.g. master-promotion lost the new master id).
func checkTrackedMatchesLeaves(hub *workspace.Hub, s *sim.SimSwayClient, wsNames []string, ev sway.Event, step int, res *Result) {
	tree, _ := s.GetTree()
	if tree == nil {
		return
	}
	for _, name := range wsNames {
		mgr := hub.Manager(name)
		if mgr == nil {
			continue
		}
		// Some managers (Tabbed) deliberately don't track window ids —
		// WindowIDs() returning nil is a sentinel for "this layout defers
		// to sway". Skip the invariant for those; they can't drift.
		if mgr.WindowIDs() == nil {
			continue
		}
		var wsNode *sway.Node
		for _, ws := range tree.Workspaces() {
			if ws.Name == name {
				wsNode = ws
				break
			}
		}
		if wsNode == nil {
			continue
		}
		// Two independent checks, to avoid conflating them:
		//
		//   missed: a non-excluded leaf (IsExcluded=false) that the
		//     manager isn't tracking. This means the manager forgot a
		//     window it should own.
		//
		//   stale: a tracked id with NO corresponding leaf anywhere in
		//     the workspace tree. This means the manager kept a closed-
		//     or-relocated window id. We use *all* leaves (no IsExcluded
		//     filter) because a manager may legitimately track windows
		//     under its own stacked/tabbed wrappers (e.g. MasterStack's
		//     stack region when StackLayout=stacking).
		wantTracked := map[int64]struct{}{}
		for _, leaf := range wsNode.Leaves() {
			if layout.IsExcluded(leaf) {
				continue
			}
			wantTracked[leaf.ID] = struct{}{}
		}
		allLeaves := map[int64]struct{}{}
		for _, leaf := range wsNode.Leaves() {
			if leaf == nil || leaf.Type != "con" {
				continue
			}
			allLeaves[leaf.ID] = struct{}{}
		}
		got := map[int64]struct{}{}
		for _, id := range mgr.WindowIDs() {
			got[id] = struct{}{}
		}
		var missed, stale []int64
		for id := range wantTracked {
			if _, ok := got[id]; !ok {
				missed = append(missed, id)
			}
		}
		for id := range got {
			if _, ok := allLeaves[id]; !ok {
				stale = append(stale, id)
			}
		}
		if len(missed) == 0 && len(stale) == 0 {
			continue
		}
		slices.Sort(missed)
		slices.Sort(stale)
		res.Violations = append(res.Violations, Violation{
			Invariant: "tracked-matches-leaves",
			Step:      step,
			Event:     ev,
			Detail: fmt.Sprintf("workspace=%s tracked=%v leaves=%v missed=%v stale=%v",
				name, mgr.WindowIDs(), leafIDs(wsNode), missed, stale),
		})
	}
}

// checkMasterWidthHonored asserts that on every MasterStack-managed
// workspace with ≥ 2 tracked windows, the master container's percent-of-
// parent matches the configured MasterWidth within a small tolerance.
//
// This catches bugs where setMasterWidth runs against the wrong target
// or runs while master is inside an intermediate wrapper that later
// flattens — both produce a master.Percent that drifts from
// MasterWidth/100. The live ws7 "master at 0.375 instead of 0.75" bug
// from 2026-04-25 and the 2026-06-14 "resized to 50% master" bug (stale
// leaf rect snapshot, fixed in 6ae4d4a) are exactly this shape: a
// HEALTHY master/stack split whose master width regressed.
//
// THE SPLIT (deliverable 1): this invariant emits two DISTINCT names so a
// real width regression is never masked by structural false positives:
//
//   - master-width-honored — the REAL-bug class. Emitted ONLY when the
//     layout structure is healthy: the master does NOT share its direct
//     parent with any tracked stack window (i.e. a real stack column
//     exists, the master-stack-split invariant is satisfied for this
//     master). In this shape `setMasterWidth`'s `resize 75 ppt` lands on
//     a clean 2-child splith, so master.Percent SHOULD be exactly 0.75;
//     any drift is a genuine width regression. Measured floor on the
//     reference sweep: 0. A regression (e.g. reverting 6ae4d4a) makes
//     this NON-ZERO.
//
//   - master-width-degenerate — the false-positive class. Emitted when
//     the master shares its direct parent with a tracked stack window.
//     That is the master-stack-split bug shape (the stack column is
//     missing / the tree is a chaotic foreign structure), so the
//     `resize 75 ppt` divides 75% across N degenerate siblings and the
//     master's Percent naturally is not 0.75. The width here is
//     unreliable BY CONSTRUCTION — the structure is already wrong and
//     master-stack-split owns that signal. Splitting it off keeps the
//     real class at a near-zero floor instead of burying it under
//     thousands of structural transients.
//
// The targeted tests in master_width_test.go are the authoritative pin
// for cold-start and pre-existing-tree regressions; the split sweep
// counts gate against regression in CI (cmd/fuzz-gate).
func checkMasterWidthHonored(hub *workspace.Hub, s *sim.SimSwayClient, wsNames []string, ev sway.Event, step int, res *Result) {
	tree, _ := s.GetTree()
	if tree == nil {
		return
	}
	// Parents are needed to decide whether the master shares its container
	// with stack windows (the degenerate-structure discriminator below).
	tree.SetParents()
	const tolerance = 0.02
	for _, name := range wsNames {
		mgr := hub.Manager(name)
		if mgr == nil {
			continue
		}
		ms, ok := mgr.(*layout.MasterStack)
		if !ok {
			continue
		}
		// Maximize folds the master into the tabbed stack column — width
		// is intentionally full there, not MasterWidth.
		if ms.Maximized() {
			continue
		}
		ids := ms.WindowIDs()
		if len(ids) < 2 {
			continue
		}
		want := float64(ms.Config().MasterWidth) / 100
		if want <= 0 || want >= 1 {
			continue
		}
		var wsNode *sway.Node
		for _, ws := range tree.Workspaces() {
			if ws.Name == name {
				wsNode = ws
				break
			}
		}
		if wsNode == nil {
			continue
		}
		master := wsNode.FindByID(ids[0])
		if master == nil {
			continue
		}
		got := master.Percent
		if got == 0 {
			// Pre-resize: the engine hasn't issued setMasterWidth yet for
			// this snapshot. Skip — the next step will catch it.
			continue
		}
		if abs64(got-want) <= tolerance {
			continue
		}
		// Classify: is the layout structure healthy enough for the width to
		// mean anything? If the master shares its direct parent with any
		// tracked stack window, the stack column is missing (the
		// master-stack-split bug shape) and `resize 75 ppt` was split
		// across degenerate siblings — the Percent is unreliable BY
		// CONSTRUCTION. That goes to the false-positive class so it cannot
		// mask a real regression. A real width regression keeps the healthy
		// 2-child splith and reports under master-width-honored.
		invName := "master-width-honored"
		if masterSharesParentWithStack(wsNode, master, ids) {
			invName = "master-width-degenerate"
		}
		res.Violations = append(res.Violations, Violation{
			Invariant: invName,
			Step:      step,
			Event:     ev,
			Detail: fmt.Sprintf("workspace=%s master_id=%d got=%.3f want=%.3f tolerance=%.3f",
				name, master.ID, got, want, tolerance),
		})
	}
}

// masterSharesParentWithStack reports whether master's direct parent also
// directly contains any of the tracked stack windows (ids[1:]). When true,
// the canonical master/stack-column split is missing (master-stack-split's
// bug shape), so the master's width is not a meaningful signal — see the
// split rationale on checkMasterWidthHonored.
func masterSharesParentWithStack(wsNode, master *sway.Node, ids []int64) bool {
	if master.Parent == nil {
		return false
	}
	for _, sid := range ids[1:] {
		sn := wsNode.FindByID(sid)
		if sn != nil && sn.Parent == master.Parent {
			return true
		}
	}
	return false
}

func abs64(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// checkMasterStackSplit asserts that on every MasterStack-managed
// workspace with ≥ 2 tracked windows, the master container does NOT
// share its direct parent with any stack window — i.e. the stack-
// column container actually exists.
//
// Healthy shape:
//
//	outer (splith)
//	├── master leaf
//	└── stack-column (splitv/stacking/tabbed)
//	    ├── stack[0]
//	    └── stack[1] ...
//
// master.Parent == outer, stack[0].Parent == stack-column → different.
//
// Bug shape (the 2026-05-25 ws7 "3 stripes instead of 1 master" report):
// the singleton-flatten in flattenWorkspace destroyed the stack column
// during the 2-window state (stack had 1 child, looks like a singleton),
// and subsequent insertAtIndex(0) calls landed siblings of master in
// the outer splith.
//
//	outer (splith)
//	├── master leaf
//	├── stack[0] leaf   ← all direct siblings; stack column missing
//	└── stack[1] leaf
//
// master.Parent == outer == stack[0].Parent → invariant fails.
//
// Substack-collapse-to-1 is not a special case: when the substack
// wrapper shrinks to one child, the SUBSTACK wrapper is what's at risk,
// not the stack column. The stack column still has multiple children
// (visible-stack + substack-wrapper) so this invariant remains clean.
func checkMasterStackSplit(hub *workspace.Hub, s *sim.SimSwayClient, wsNames []string, ev sway.Event, step int, res *Result) {
	tree, _ := s.GetTree()
	if tree == nil {
		return
	}
	tree.SetParents()
	for _, name := range wsNames {
		mgr := hub.Manager(name)
		if mgr == nil {
			continue
		}
		ms, ok := mgr.(*layout.MasterStack)
		if !ok {
			continue
		}
		// toggleMaximize deliberately folds the master INTO the stack
		// column (shared parent, layout tabbed) — that's the intended
		// maximized shape, not a missing stack column.
		if ms.Maximized() {
			continue
		}
		ids := mgr.WindowIDs()
		if len(ids) < 2 {
			continue
		}
		var wsNode *sway.Node
		for _, ws := range tree.Workspaces() {
			if ws.Name == name {
				wsNode = ws
				break
			}
		}
		if wsNode == nil {
			continue
		}
		master := wsNode.FindByID(ids[0])
		if master == nil || master.Parent == nil {
			continue
		}
		masterParent := master.Parent
		for _, sid := range ids[1:] {
			stackNode := wsNode.FindByID(sid)
			if stackNode == nil || stackNode.Parent == nil {
				continue
			}
			if stackNode.Parent == masterParent {
				res.Violations = append(res.Violations, Violation{
					Invariant: "master-stack-split",
					Step:      step,
					Event:     ev,
					Detail: fmt.Sprintf(
						"workspace=%s master=%d stack=%d share parent=%d (layout=%s); tracked=%v — stack column missing",
						name, ids[0], sid, masterParent.ID, masterParent.Layout, ids),
				})
				break // one violation per workspace per step
			}
		}
	}
}

func leafIDs(ws *sway.Node) []int64 {
	var ids []int64
	for _, l := range ws.Leaves() {
		if layout.IsExcluded(l) {
			continue
		}
		ids = append(ids, l.ID)
	}
	return ids
}

func countFocused(n *sway.Node) int {
	c := 0
	if n.Focused && len(n.Nodes) == 0 {
		c++
	}
	for _, child := range n.Nodes {
		c += countFocused(child)
	}
	for _, child := range n.FloatingNodes {
		c += countFocused(child)
	}
	return c
}

func describe(ev sway.Event) string {
	switch ev.Type {
	case "window":
		if ev.Container != nil {
			return fmt.Sprintf("window:%s con=%d", ev.Change, ev.Container.ID)
		}
	case "workspace":
		if ev.Workspace != nil {
			return fmt.Sprintf("workspace:%s name=%s", ev.Change, ev.Workspace.Name)
		}
	case "binding":
		if ev.Binding != nil {
			return fmt.Sprintf("binding:%s", ev.Binding.Command)
		}
	}
	return ev.Type + ":" + ev.Change
}

// checkMaximizedFoldIntact asserts that a manager claiming to be maximized
// is actually SITTING in the maximized shape: master folded into the stack
// column (shared parent) with that parent tabbed, exactly what
// toggleMaximize builds.
//
// This exists because `maximized` is an assertion-SUPPRESSION switch that
// the manager itself owns: both checkMasterWidthHonored and
// checkMasterStackSplit skip a maximized workspace, since the folded shape
// legitimately looks like "master shares a parent with the stack" and "the
// master is not MasterWidth wide". A flag that is stale-true therefore
// silently disables the gate's two main structural invariants — the bug and
// its own detector in one. That is how `maximized` state drift stayed
// invisible for hundreds of steps until an unmaximize replayed the
// shape-specific restore against a tree that was never folded.
//
// So the flag has to justify itself. If the manager says maximized, the
// fold must be real; otherwise the suppression above is unearned and this
// fires instead.
//
// Skipped below 2 tracked windows: a fold needs two containers to share a
// parent, and toggleMaximize early-returns there anyway.
func checkMaximizedFoldIntact(hub *workspace.Hub, s *sim.SimSwayClient, wsNames []string, ev sway.Event, step int, res *Result) {
	tree, _ := s.GetTree()
	if tree == nil {
		return
	}
	tree.SetParents()
	for _, name := range wsNames {
		mgr := hub.Manager(name)
		if mgr == nil {
			continue
		}
		ms, ok := mgr.(*layout.MasterStack)
		if !ok || !ms.Maximized() {
			continue
		}
		ids := ms.WindowIDs()
		if len(ids) < 2 {
			continue
		}
		var wsNode *sway.Node
		for _, ws := range tree.Workspaces() {
			if ws.Name == name {
				wsNode = ws
				break
			}
		}
		if wsNode == nil {
			continue
		}
		master := wsNode.FindByID(ids[0])
		if master == nil || master.Parent == nil {
			continue
		}
		second := wsNode.FindByID(ids[1])
		if second == nil || second.Parent == nil {
			continue
		}
		if master.Parent != second.Parent {
			res.Violations = append(res.Violations, Violation{
				Invariant: "maximized-fold-intact",
				Step:      step,
				Event:     ev,
				Detail: fmt.Sprintf(
					"workspace=%s claims maximized but master=%d (parent=%d/%s) and stack[0]=%d "+
						"(parent=%d/%s) are NOT folded together; tracked=%v — stale maximized flag, "+
						"and it is suppressing master-width/master-stack-split checks",
					name, ids[0], master.Parent.ID, master.Parent.Layout,
					ids[1], second.Parent.ID, second.Parent.Layout, ids),
			})
			continue
		}
		if master.Parent.Layout != "tabbed" {
			res.Violations = append(res.Violations, Violation{
				Invariant: "maximized-fold-intact",
				Step:      step,
				Event:     ev,
				Detail: fmt.Sprintf(
					"workspace=%s claims maximized: master=%d is folded into parent=%d but its layout "+
						"is %q, want \"tabbed\"; tracked=%v",
					name, ids[0], master.Parent.ID, master.Parent.Layout, ids),
			})
		}
	}
}
