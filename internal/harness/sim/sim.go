// Package sim implements a synthetic in-memory Sway tree that satisfies
// sway.Client. It backs the property-based fuzzer (internal/harness/fuzz).
//
// The sim implements the subset of Sway's tree-update semantics the
// layout engines actually exercise:
//
//   - add/remove/move leaf containers
//   - splith, splitv, split none
//   - focus updates
//   - floating toggle
//   - workspace init / destroy
//   - layout <tabbed|splith|splitv|stacking>
//   - resize set width <N> ppt|px
//   - mark --add NAME / unmark NAME
//   - move window to mark NAME / move to mark NAME
//   - swap container with con_id N
//
// Everything else returns ErrUnsupportedCommand and is recorded on
// SimSwayClient.UnsupportedCommands. Commands real sway would refuse
// (e.g. `split none` on a node with siblings) return ErrSwayRejected
// and are recorded on SwayRejections — the fuzzer asserts both lists
// stay empty.
package sim

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/mschulkind-oss/tilekeeper/internal/sway"
)

// ErrUnsupportedCommand is returned by RunCommand when the sim sees a
// verb (or verb-argument combo) outside the supported subset. It signals
// a gap in the sim, not a bug in the layout engine.
var ErrUnsupportedCommand = errors.New("sim: unsupported command")

// ErrSwayRejected is returned by RunCommand when the command is
// well-formed but real sway would also reject it (e.g. "split none" on a
// container that has siblings). Unlike ErrUnsupportedCommand, a violation
// here indicates the layout engine emitted a command sway itself would
// refuse to run — a real bug, not a sim gap.
var ErrSwayRejected = errors.New("sim: sway would reject")

// ErrFlattenSiblings is sway's response to `split none` when the target
// has siblings: "Can only flatten a child container with no siblings".
// Wrapped with ErrSwayRejected.
var ErrFlattenSiblings = fmt.Errorf("%w: can only flatten a child container with no siblings", ErrSwayRejected)

// ErrResizeNonPositive is raised when a `resize set <dim> N <unit>` command
// uses N ≤ 0. Sway's cmd_resize_set (sway/commands/resize.c:452) silently
// replaces a non-positive amount with the container's current pending
// dimension, so the resize becomes a no-op — meaning tilekeeper intended to change
// the geometry and instead did nothing. We surface this as a sway-reject so
// the fuzzer flags any layout manager emitting a useless resize.
var ErrResizeNonPositive = fmt.Errorf("%w: resize set with non-positive amount is a silent no-op in sway", ErrSwayRejected)

// SimSwayClient is an in-memory Sway client used by the harness.
//
// It is safe for concurrent use; all mutations take the internal lock.
// Events injected via InjectEvent are delivered to every subscriber that
// registered for the event type.
type SimSwayClient struct {
	mu sync.Mutex

	root       *sway.Node
	workspaces []sway.Workspace

	nextID int64
	marks  map[string]int64 // mark name → con id

	subs []subscription

	// UnsupportedCommands records every command the sim could not apply
	// because of a gap in the sim model (unknown verb, missing feature).
	// Fuzzer invariants assert this list is empty at end of a run.
	UnsupportedCommands []string

	// SwayRejections records commands that real sway would refuse to run.
	// These are real layout-engine bugs, not sim gaps. Each entry is the
	// command string followed by a short reason.
	SwayRejections []string

	// TraceSink, if non-nil, receives every command string passed to
	// RunCommand. Used by fuzz-find-min to dump the command tape up to
	// the first violation.
	TraceSink func(cmd string)
}

type subscription struct {
	types   map[string]struct{}
	handler sway.EventHandler
}

// New constructs an empty SimSwayClient with a root node and no outputs.
// Use AddOutput / AddWorkspace or SetTree to populate.
func New() *SimSwayClient {
	return &SimSwayClient{
		root:   &sway.Node{ID: 1, Type: "root"},
		nextID: 1000,
		marks:  map[string]int64{},
	}
}

// NewWithTree constructs a SimSwayClient with a pre-built tree.
// The tree is adopted as-is; SetParents is invoked to wire up .Parent.
// IDs above the tree's max are reserved for future allocations.
func NewWithTree(root *sway.Node, wss []sway.Workspace) *SimSwayClient {
	root.SetParents()
	max := maxID(root)
	s := &SimSwayClient{
		root:       root,
		workspaces: append([]sway.Workspace(nil), wss...),
		nextID:     max + 1,
		marks:      map[string]int64{},
	}
	// Index pre-existing marks so `unmark` works on fixtures.
	s.indexMarks(root)
	return s
}

func maxID(n *sway.Node) int64 {
	m := n.ID
	for _, c := range n.Nodes {
		if cm := maxID(c); cm > m {
			m = cm
		}
	}
	for _, c := range n.FloatingNodes {
		if cm := maxID(c); cm > m {
			m = cm
		}
	}
	return m
}

func (s *SimSwayClient) indexMarks(n *sway.Node) {
	for _, mk := range n.Marks {
		s.marks[mk] = n.ID
	}
	for _, c := range n.Nodes {
		s.indexMarks(c)
	}
	for _, c := range n.FloatingNodes {
		s.indexMarks(c)
	}
}

// GetTree returns the live tree root. Callers must not mutate.
// (A deep copy would be nicer, but the production sway.Conn.GetTree also
// returns a freshly unmarshalled tree; for the sim we rely on callers
// using the tree read-only as the managers already do.)
func (s *SimSwayClient) GetTree() (*sway.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.root, nil
}

// GetWorkspaces returns the configured workspace list.
func (s *SimSwayClient) GetWorkspaces() ([]sway.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]sway.Workspace, len(s.workspaces))
	copy(out, s.workspaces)
	return out, nil
}

// Subscribe registers a handler for a set of event types. Events are
// delivered on InjectEvent; this method itself does not block.
func (s *SimSwayClient) Subscribe(types []string, h sway.EventHandler) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := map[string]struct{}{}
	for _, t := range types {
		set[t] = struct{}{}
	}
	s.subs = append(s.subs, subscription{types: set, handler: h})
	return nil
}

// InjectEvent delivers ev to every subscriber registered for ev.Type.
// It is used by the replay driver and fuzzer to drive state changes.
func (s *SimSwayClient) InjectEvent(ev sway.Event) {
	s.mu.Lock()
	subs := append([]subscription(nil), s.subs...)
	s.mu.Unlock()
	for _, sub := range subs {
		if _, ok := sub.types[ev.Type]; !ok {
			continue
		}
		sub.handler(ev)
	}
}

// SetWorkspaces replaces the workspace list (used by fixtures).
func (s *SimSwayClient) SetWorkspaces(wss []sway.Workspace) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workspaces = append([]sway.Workspace(nil), wss...)
}

// AllocID reserves and returns a new container id, useful when the fuzzer
// or replay driver needs to fabricate new windows outside of RunCommand.
func (s *SimSwayClient) AllocID() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextID
	s.nextID++
	return id
}

// CloseLeaf removes leaf from the tree and cascade-flattens its ancestry,
// matching what real sway does when a window is destroyed. Empty
// intermediate containers are removed; singleton ones auto-flatten
// (their child takes their slot). Workspaces, outputs, and root are
// never flattened.
//
// If the closed leaf was focused, focus transfers to any remaining leaf
// in the same workspace — real sway maintains the "something is always
// focused" invariant by promoting an adjacent window when the focused
// one is destroyed.
//
// Caller is responsible for firing the matching window::close event
// before calling this — the event container still has its original
// .Parent wired so hub/managers can identify the workspace.
func (s *SimSwayClient) CloseLeaf(leaf *sway.Node) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if leaf == nil {
		return
	}
	wasFocused := leaf.Focused
	ws := leaf.FindWorkspace()
	s.detach(leaf)
	if !wasFocused {
		return
	}
	if ws != nil {
		for _, l := range ws.Leaves() {
			l.Focused = true
			return
		}
	}
	// The closed leaf was focused but its workspace is now empty (or
	// gone). Real sway keeps the "exactly one focused window" invariant
	// when other windows exist by promoting focus to another workspace's
	// most-recently-used leaf. We don't track MRU, so pick the first
	// leaf in any other workspace deterministically.
	for _, w := range s.root.Workspaces() {
		if w == ws {
			continue
		}
		for _, l := range w.Leaves() {
			l.Focused = true
			return
		}
	}
}

// RunCommand parses cmd, applies it to the tree, and records unsupported
// commands for later assertion. Compound commands separated by commas
// are applied in order; a single unsupported sub-command fails the whole
// call (like sway itself).
func (s *SimSwayClient) RunCommand(cmd string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.TraceSink != nil {
		s.TraceSink(cmd)
	}

	// Handle compound commands: "[con_id=X] splitv, layout stacking"
	for _, part := range splitCompound(cmd) {
		scope, verb, args := parseSwayCommand(part)
		if verb == "" {
			continue
		}
		if err := s.apply(scope, verb, args); err != nil {
			if errors.Is(err, ErrSwayRejected) {
				s.SwayRejections = append(s.SwayRejections, fmt.Sprintf("%s: %s", part, err.Error()))
				return err
			}
			s.UnsupportedCommands = append(s.UnsupportedCommands, part)
			return fmt.Errorf("%w: %s", ErrUnsupportedCommand, part)
		}
	}
	return nil
}

// splitCompound splits "a, b, c" into ["a", "b", "c"] preserving any
// leading scope on the first segment only.
func splitCompound(cmd string) []string {
	parts := strings.Split(cmd, ",")
	// The scope prefix (if any) applies to all subparts in real sway,
	// so propagate it when missing.
	scope := ""
	first := strings.TrimSpace(parts[0])
	if strings.HasPrefix(first, "[") {
		if i := strings.Index(first, "]"); i > 0 {
			scope = first[:i+1]
		}
	}
	out := make([]string, 0, len(parts))
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if i > 0 && scope != "" && !strings.HasPrefix(p, "[") {
			p = scope + " " + p
		}
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Interface compliance.
var _ sway.Client = (*SimSwayClient)(nil)

// resolveScope returns the node targeted by scope (e.g. "[con_id=20]").
// Empty scope returns the currently focused leaf.
func (s *SimSwayClient) resolveScope(scope string) *sway.Node {
	if scope == "" {
		return s.root.FindFocused()
	}
	// Parse "[con_id=N]" or "[workspace=NAME]".
	inner := strings.TrimSuffix(strings.TrimPrefix(scope, "["), "]")
	for clause := range strings.SplitSeq(inner, " ") {
		kv := strings.SplitN(clause, "=", 2)
		if len(kv) != 2 {
			continue
		}
		// sway accepts both [workspace=7] and [workspace="7"] — strip
		// surrounding double quotes so the sim matches either form.
		val := strings.Trim(kv[1], `"`)
		switch kv[0] {
		case "con_id":
			id, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				return nil
			}
			return s.root.FindByID(id)
		case "workspace":
			for _, ws := range s.root.Workspaces() {
				if ws.Name == val {
					return ws
				}
			}
		}
	}
	return nil
}
