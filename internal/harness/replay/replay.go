// Package replay turns a captured (or journal-derived) sway event stream
// into a fuzzer regression run. It reads the JSONL capture written by the
// daemon's TK_EVENT_CAPTURE sink (internal/capture), reconstructs the live
// sway tree by applying each event's sway-faithful tree mutation, drives the
// reconstructed stream through a fresh (sim, workspace.Hub) pair — the same
// wiring the property fuzzer uses — and runs the SAME invariant checkers
// (via fuzz.CheckStep). Any violation is reported with the originating event
// and its daemon-assigned seq, so a real incident becomes a precise repro.
//
// Why reconstruct the tree at all? The capture (and certainly the text
// journal) records event *snapshots*: a container's id/name/app_id/layout/
// floating/fullscreen/rect/focused at emission time, with no children and no
// parent (matching real IPC payloads). The invariant checkers read the LIVE
// tree (focus convergence, wrapper chains, master/stack split, tracked-vs-
// leaves), so we must rebuild that tree as the stream advances. We model the
// minimal subset of sway tree semantics the events imply:
//
//   - window::new     attach a fresh leaf as sibling of the focused leaf
//   - window::close   detach the leaf and cascade-flatten empties
//   - window::focus   move the single focus marker
//   - window::floating toggle the leaf between tiled tree and floating list
//   - window::move    relocate the leaf (and, for a multi-window subtree
//     fellow-traveler move, its whole subtree) to the
//     destination workspace, mirroring container_move_to_
//     workspace (the ws7 fellow-traveler incident shape)
//   - workspace::init create the workspace node
//
// This is intentionally lossy for a raw journal (the daemon's own emitted
// commands are NOT replayed — only the events sway delivered), so the
// reconstructed tree can diverge from production for command-heavy windows.
// A daemon-written CAPTURE is the faithful source; a text journal is a
// best-effort fallback. Unreconstructable events are skipped with a warning
// rather than crashing the run.
package replay

import (
	"fmt"

	"github.com/mschulkind-oss/tilekeeper/internal/capture"
	"github.com/mschulkind-oss/tilekeeper/internal/config"
	"github.com/mschulkind-oss/tilekeeper/internal/harness/fuzz"
	"github.com/mschulkind-oss/tilekeeper/internal/harness/sim"
	"github.com/mschulkind-oss/tilekeeper/internal/sway"
	"github.com/mschulkind-oss/tilekeeper/internal/workspace"

	"log/slog"
)

// Options configures a replay run.
type Options struct {
	// DefaultLayout is the layout installed for every replayed workspace
	// (matches the fuzzer's MasterStack default). When a capture meta record
	// carries a default_layout, that wins unless overridden here.
	DefaultLayout string
	// MasterWidth seeds the MasterStack config so the master-width-honored
	// invariant has a target. Meta record value wins when present and this
	// is zero.
	MasterWidth int
	// Workspaces, when non-empty, restricts the managed workspace set.
	// Empty => infer from the capture (meta workspaces + any workspace that
	// appears in the event stream).
	Workspaces []string
}

// Warning records a non-fatal reconstruction problem (an event that could
// not be applied to the live tree). The seq + reason let the operator see
// exactly how lossy the replay was.
type Warning struct {
	Seq    int64
	Type   string
	Change string
	Reason string
}

// Result is the outcome of a replay run.
type Result struct {
	Events     int
	Steps      int
	Violations []fuzz.Violation
	Warnings   []Warning
	// SkippedLines is carried through from the capture parse (malformed or
	// unknown-kind JSONL lines).
	SkippedLines int
	Workspaces   []string
}

// RunFile parses the capture at path and replays it.
func RunFile(path string, opts Options) (*Result, error) {
	f, err := capture.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Run(f, opts)
}

// Run replays a parsed capture File through a fresh sim+Hub and runs the
// fuzzer invariants on each step.
func Run(f *capture.File, opts Options) (*Result, error) {
	res := &Result{SkippedLines: f.SkippedLines}

	defaultLayout := opts.DefaultLayout
	masterWidth := opts.MasterWidth
	var metaWorkspaces []string
	var seedTree *sway.Node
	if f.Meta != nil {
		if defaultLayout == "" && f.Meta.DefaultLayout != "" {
			defaultLayout = f.Meta.DefaultLayout
		}
		if masterWidth == 0 {
			masterWidth = f.Meta.MasterWidth
		}
		metaWorkspaces = f.Meta.Workspaces
		seedTree = f.Meta.Tree
	}
	if defaultLayout == "" {
		defaultLayout = "MasterStack"
	}
	if masterWidth == 0 {
		masterWidth = 75
	}

	// Determine the managed workspace set: explicit override, else the
	// union of meta workspaces and every workspace named by an event.
	wsNames := opts.Workspaces
	if len(wsNames) == 0 {
		wsNames = inferWorkspaces(metaWorkspaces, f.Events, seedTree)
	}
	res.Workspaces = wsNames

	// Build the sim + hub exactly like the fuzz harness: a "none" general
	// default with per-workspace DefaultLayout, so only the replayed
	// workspaces get a real manager.
	var s *sim.SimSwayClient
	st := newReplayState()
	if seedTree != nil {
		seedTree.SetParents()
		s = sim.NewWithTree(seedTree, seedWorkspaceList(seedTree))
		st.adoptTree(seedTree)
	} else {
		s = sim.New()
	}

	errSink := fuzz.NewErrorSink()
	logger := slog.New(errSink)

	wsCfg := map[string]config.WorkspaceConfig{}
	for _, name := range wsNames {
		wsCfg[name] = config.WorkspaceConfig{DefaultLayout: defaultLayout}
	}
	hub := workspace.NewHub(s, config.Config{
		General:    config.GeneralConfig{DefaultLayout: "none", MasterWidth: masterWidth},
		Workspaces: wsCfg,
	}, logger)
	hub.Initialize()

	// Seed each managed workspace (and any from the seed tree) so the sim
	// tree has the workspace nodes the events reference. If a seed tree was
	// adopted, its workspaces already exist; ensureWorkspace is idempotent.
	for _, name := range wsNames {
		ev := st.ensureWorkspace(s, name)
		if ev.Type != "" {
			hub.HandleEvent(ev)
		}
	}
	if seedTree != nil {
		hub.SeedTracking(seedTree)
	}

	step := 0
	for _, er := range f.Events {
		res.Events++
		ev := er.ToEvent()
		// Thread the capture's resolved workspace into the state machine so
		// window mutators (new/move) know where the event landed. For
		// window::move this is the DESTINATION workspace.
		st.pendingWS = er.WS

		// Ensure the workspace referenced by the event exists in the sim so
		// the tree mutation has somewhere to land. Events for unmanaged
		// workspaces still mutate the tree (so cross-workspace moves into a
		// managed ws are faithful) but produce no manager ops.
		st.ensureEventWorkspace(s, hub, ev, er.WS)

		// Apply the sway-faithful tree mutation for this event BEFORE
		// dispatch, mirroring the fuzz driver (sway mutates the tree
		// regardless of whether the daemon reacts). close is special: detach
		// after dispatch so managers can still read siblings — matching the
		// fuzzer's close handling.
		preErr := st.applyPreDispatch(s, ev)
		if preErr != nil {
			res.Warnings = append(res.Warnings, Warning{
				Seq: ev.Seq, Type: ev.Type, Change: ev.Change, Reason: preErr.Error(),
			})
			// Skip unreconstructable events rather than crash. Still bump the
			// step so seq/step coordinates stay aligned with the capture.
			step++
			continue
		}
		step++
		res.Steps++

		// fuzz.CheckStep / runStep append to a *fuzz.Result. Use a per-step
		// scratch Result and fold its violations into ours, so we reuse the
		// fuzzer's exact checkers without duplicating their bodies.
		//
		// A dropped event (er.Drop) mutates the tree but is NOT dispatched to
		// the hub — modeling the daemon's bounded channel overflowing. The
		// resulting tracking-vs-leaves divergence is the corruption a drop
		// causes; the checkers below catch it. This is the replay analogue of
		// the fuzzer's DropRate path.
		fr := &fuzz.Result{}
		if !er.Drop {
			runStep(hub, s, ev, step, fr)
		}
		st.applyPostDispatch(s, ev)

		fuzz.CheckStep(hub, s, wsNames, ev, step, fr)
		for _, msg := range errSink.DrainMessages() {
			fr.Violations = append(fr.Violations, fuzz.Violation{
				Invariant: "no-handler-error",
				Step:      step,
				Event:     ev,
				Detail:    msg,
			})
		}
		res.Violations = append(res.Violations, fr.Violations...)
	}

	return res, nil
}

// runStep dispatches ev to the hub, recovering panics as the no-crash
// invariant — the one checker only the caller can wrap.
func runStep(hub *workspace.Hub, s *sim.SimSwayClient, ev sway.Event, step int, res *fuzz.Result) {
	defer func() {
		if r := recover(); r != nil {
			res.Violations = append(res.Violations, fuzz.Violation{
				Invariant: "no-crash",
				Step:      step,
				Event:     ev,
				Detail:    fmt.Sprintf("panic: %v (event=%s/%s seq=%d)", r, ev.Type, ev.Change, ev.Seq),
			})
		}
	}()
	hub.HandleEvent(ev)
	_ = s
}

// inferWorkspaces returns the managed workspace set from meta + the event
// stream + the seed tree, de-duplicated and order-stable (meta first).
func inferWorkspaces(meta []string, events []*capture.EventRecord, seed *sway.Node) []string {
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if n == "" || seen[n] {
			return
		}
		seen[n] = true
		out = append(out, n)
	}
	for _, n := range meta {
		add(n)
	}
	if seed != nil {
		for _, ws := range seed.Workspaces() {
			add(ws.Name)
		}
	}
	for _, er := range events {
		if er.Type == "workspace" && er.Workspace != nil {
			add(er.Workspace.Name)
		}
		// Window events carry their resolved workspace in WS (the move
		// destination, the new/focus home). Harvest these too so a journal-
		// only input — which has no meta and no workspace::init lines —
		// still installs managers for the workspaces its windows live on.
		if er.WS != "" {
			add(er.WS)
		}
	}
	return out
}

func seedWorkspaceList(tree *sway.Node) []sway.Workspace {
	var out []sway.Workspace
	first := true
	for _, ws := range tree.Workspaces() {
		out = append(out, sway.Workspace{Name: ws.Name, Focused: first})
		first = false
	}
	return out
}
