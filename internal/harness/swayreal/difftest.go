package swayreal

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mschulkind-oss/tilekeeper/internal/harness/sim"
	"github.com/mschulkind-oss/tilekeeper/internal/sway"
)

// DiffResult captures the outcome of running one command sequence through
// both the sim and real sway. Diverged is true if any command produced a
// structural mismatch between the two trees.
type DiffResult struct {
	Commands    []string
	Diverged    bool
	DivergedAt  int    // index into Commands of the first divergence (−1 if none)
	DivergeCmd  string // the command that diverged
	Detail      string // human-readable description of the mismatch
	SimSubtree  string // normalized sim tree at divergence
	SwaySubtree string // normalized sway tree at divergence
	// CmdErrors records, per command index, any error from applying the
	// command to the sim or sway (e.g. sim "unsupported", sway "rejected").
	// A command that BOTH reject is not a divergence; one rejecting and the
	// other accepting IS.
	CmdErrors []CmdError
}

// CmdError holds the per-side application result for one command.
type CmdError struct {
	Index   int
	Command string
	SimErr  string
	SwayErr string
}

// RunScenario seeds a fresh sim from sway's current tree, then applies
// each command in `commands` to BOTH the sim and real sway, diffing their
// trees structurally after each step. It reports the FIRST diverging
// command. The caller is responsible for having already set up the desired
// initial window count on sway (see SpawnWindows).
//
// Seeding from sway's real tree (ids and all) is the crux: a sim built
// from scratch would assign its own con ids, so node identities would not
// line up and every diff would be noise. By deep-copying sway's get_tree
// into the sim we guarantee identical ids, then any later structural
// disagreement is a genuine sim-fidelity gap.
func (s *Sway) RunScenario(commands []string) (*DiffResult, error) {
	realTree, err := s.GetTree()
	if err != nil {
		return nil, fmt.Errorf("difftest: initial get_tree: %w", err)
	}
	simClient := sim.NewWithTree(CloneTree(realTree), nil)

	res := &DiffResult{Commands: commands, DivergedAt: -1}

	// Sanity: the freshly-seeded sim must already match sway (it's a copy).
	if d := DiffTrees(mustTree(simClient), realTree); d != "" {
		res.Diverged = true
		res.DivergedAt = -1
		res.DivergeCmd = "<seed>"
		res.Detail = "seed mismatch: " + d
		res.SimSubtree = NormalizeTree(mustTree(simClient))
		res.SwaySubtree = NormalizeTree(realTree)
		return res, nil
	}

	for i, cmd := range commands {
		simErr := simClient.RunCommand(cmd)
		swayErr := s.RunCommand(cmd)

		ce := CmdError{Index: i, Command: cmd}
		if simErr != nil {
			ce.SimErr = simErr.Error()
		}
		if swayErr != nil {
			ce.SwayErr = swayErr.Error()
		}
		res.CmdErrors = append(res.CmdErrors, ce)

		// One side accepted, the other rejected → divergence in handling.
		if (simErr == nil) != (swayErr == nil) {
			res.Diverged = true
			res.DivergedAt = i
			res.DivergeCmd = cmd
			res.Detail = fmt.Sprintf(
				"command acceptance diverged: sim_err=%q sway_err=%q",
				ce.SimErr, ce.SwayErr)
			res.SimSubtree = NormalizeTree(mustTree(simClient))
			res.SwaySubtree = NormalizeTree(mustTreeOf(s))
			return res, nil
		}

		simTree := mustTree(simClient)
		swayTree, err := s.GetTree()
		if err != nil {
			return nil, fmt.Errorf("difftest: get_tree after %q: %w", cmd, err)
		}
		if d := DiffTrees(simTree, swayTree); d != "" {
			res.Diverged = true
			res.DivergedAt = i
			res.DivergeCmd = cmd
			res.Detail = d
			res.SimSubtree = NormalizeTree(simTree)
			res.SwaySubtree = NormalizeTree(swayTree)
			return res, nil
		}
	}
	return res, nil
}

func mustTree(c *sim.SimSwayClient) *sway.Node {
	t, _ := c.GetTree()
	return t
}

func mustTreeOf(s *Sway) *sway.Node {
	t, _ := s.GetTree()
	return t
}

// CloneTree returns a deep copy of n with Parent links re-wired. The sim
// adopts this copy so its mutations never touch sway's tree (and vice
// versa). Ids, layout, floating, marks, rects, and percents are all
// preserved so the two trees start identical.
func CloneTree(n *sway.Node) *sway.Node {
	if n == nil {
		return nil
	}
	c := &sway.Node{
		ID:             n.ID,
		Name:           n.Name,
		Type:           n.Type,
		Layout:         n.Layout,
		Rect:           n.Rect,
		AppID:          n.AppID,
		WindowClass:    n.WindowClass,
		Focused:        n.Focused,
		FullscreenMode: n.FullscreenMode,
		Floating:       n.Floating,
		Percent:        n.Percent,
		Marks:          append([]string(nil), n.Marks...),
	}
	for _, child := range n.Nodes {
		c.Nodes = append(c.Nodes, CloneTree(child))
	}
	for _, child := range n.FloatingNodes {
		c.FloatingNodes = append(c.FloatingNodes, CloneTree(child))
	}
	c.SetParents()
	return c
}

// --- structural diff -------------------------------------------------------

// normNode is the noise-normalized projection of a sway.Node used for
// comparison. It deliberately drops fields that legitimately differ between
// a fresh sim and real sway (pixel rects beyond a coarse bucket, app_id,
// name) and keeps the structural / semantic fields that a sim-fidelity bug
// would corrupt: id, kind (con/floating), layout, floating-ness, fullscreen,
// focus, percent (bucketed), and the recursive child shape.
type normNode struct {
	ID         int64
	Kind       string // "con" | "floating" | "workspace" | "output" | "root"
	Layout     string // normalized: leaves → "none"
	Floating   bool
	Fullscreen bool
	Focused    bool
	Percent    int  // bucketed to whole percent (0..100); −1 if unset
	PercentCmp bool // whether Percent is apples-to-apples comparable
	Children   []*normNode
}

// classifyKind collapses sway's type vocabulary into the buckets that
// matter structurally. A floating leaf is "floating_con" in sway but plain
// "con" with Floating="user_on" in the sim — both map to kind "floating".
func classifyKind(n *sway.Node) string {
	switch n.Type {
	case "root", "output", "workspace":
		return n.Type
	}
	// con or floating_con
	if n.IsFloating() || n.Type == "floating_con" {
		return "floating"
	}
	return "con"
}

// normLayout maps sway's leaf layout to a canonical "none". Real sway
// reports "none" for window leaves; the sim leaves Layout empty. Split
// containers keep their real layout (splith/splitv/tabbed/stacked).
func normLayout(n *sway.Node) string {
	l := n.Layout
	if l == "" {
		return "none"
	}
	return l
}

// bucketPercent rounds a percent share to the nearest whole percent. Sim
// and sway compute pixel/percent slightly differently (rounding, gaps,
// borders), so an exact float compare is too brittle; whole-percent
// granularity still catches the master-width-50% class of bug. 0 percent
// (root/output/unset) maps to −1 ("not meaningful").
func bucketPercent(p float64) int {
	if p <= 0 {
		return -1
	}
	return int(p*100 + 0.5)
}

// normalize projects a sway.Node subtree into a normNode tree. Output and
// root wrappers are kept structurally so workspace placement is comparable.
// parentLayout is the (normalized) layout of n's parent split container; it
// decides whether n's stored Percent is a comparable on-screen share.
func normalize(n *sway.Node) *normNode { return normalizeWithParent(n, "") }

func normalizeWithParent(n *sway.Node, parentLayout string) *normNode {
	kind := classifyKind(n)
	// Fullscreen is only meaningful on window leaves. Real sway reports
	// fullscreen_mode=1 on the WORKSPACE node itself (it is the implicit
	// fullscreen container), which is not a window state and would show a
	// spurious FS flag on every workspace.
	fullscreen := false
	if kind == "con" || kind == "floating" {
		fullscreen = n.FullscreenMode != 0
	}
	// Percent is apples-to-apples only for a TILED child of a splith/splitv
	// parent. In tabbed/stacked parents every child's on-screen share is the
	// full container (sway reports 1.0), while the sim stores the underlying
	// split fraction — two different quantities. Floating containers store an
	// arbitrary output fraction. The diff compares Percent only when both
	// sides agree it is a real split share (PercentCmp true).
	percentCmp := !n.IsFloating() && n.Type != "floating_con" &&
		(parentLayout == "splith" || parentLayout == "splitv")
	nn := &normNode{
		ID:         n.ID,
		Kind:       kind,
		Layout:     normLayout(n),
		Floating:   n.IsFloating() || n.Type == "floating_con",
		Fullscreen: fullscreen,
		Focused:    n.Focused,
		Percent:    bucketPercent(n.Percent),
		PercentCmp: percentCmp,
	}
	childParentLayout := normLayout(n)
	for _, c := range n.Nodes {
		nn.Children = append(nn.Children, normalizeWithParent(c, childParentLayout))
	}
	// Floating children are compared as an order-independent set (sway and
	// sim may list them in different orders); sort by id for a stable view.
	var floats []*normNode
	for _, c := range n.FloatingNodes {
		floats = append(floats, normalizeWithParent(c, ""))
	}
	sort.Slice(floats, func(i, j int) bool { return floats[i].ID < floats[j].ID })
	// Mark floating children distinctly by appending after tiled ones; the
	// Kind=="floating" tag already distinguishes them, and DiffTrees walks
	// Children positionally for tiled but the floating set is sorted, so
	// both trees present floats in the same canonical order.
	nn.Children = append(nn.Children, floats...)
	return nn
}

// DiffTrees compares two sway trees structurally after normalization,
// scoped to the workspaces that hold windows (ignores empty scaffolding
// differences like a transient empty __i3_scratch workspace). Returns ""
// if they match, else a human-readable description of the first mismatch.
func DiffTrees(a, b *sway.Node) string {
	na := workspaceForest(a)
	nb := workspaceForest(b)
	return diffForest(na, nb)
}

// workspaceForest extracts the set of non-empty workspaces keyed by name,
// each normalized. Comparing per-workspace (rather than whole-root) avoids
// false positives from output/scratch scaffolding that the sim does not
// model identically, while still covering every place a window can live.
func workspaceForest(root *sway.Node) map[string]*normNode {
	out := map[string]*normNode{}
	for _, ws := range root.Workspaces() {
		// Skip sway's internal scratch workspace and empty workspaces with
		// no windows at all.
		if strings.HasPrefix(ws.Name, "__i3_") {
			continue
		}
		if len(ws.Leaves()) == 0 && len(ws.FloatingNodes) == 0 {
			continue
		}
		out[ws.Name] = normalize(ws)
	}
	return out
}

func diffForest(a, b map[string]*normNode) string {
	names := map[string]bool{}
	for k := range a {
		names[k] = true
	}
	for k := range b {
		names[k] = true
	}
	var sorted []string
	for k := range names {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, name := range sorted {
		wa, oka := a[name]
		wb, okb := b[name]
		if oka != okb {
			return fmt.Sprintf("workspace %q present in %s only",
				name, presentSide(oka))
		}
		if d := diffNode(wa, wb, "ws["+name+"]"); d != "" {
			return d
		}
	}
	return ""
}

func presentSide(simHas bool) string {
	if simHas {
		return "sim"
	}
	return "sway"
}

// diffNode compares two normalized nodes recursively, ignoring the node id
// for non-leaf structural containers the sim may number differently (it
// reuses sway's ids for seeded nodes but mints its own for wrappers it
// creates). Leaf identity (windows) IS compared by id because both sides
// share sway's original window ids.
func diffNode(a, b *normNode, path string) string {
	if a == nil || b == nil {
		if a == b {
			return ""
		}
		return fmt.Sprintf("%s: one side nil (sim=%v sway=%v)", path, a != nil, b != nil)
	}
	if a.Kind != b.Kind {
		return fmt.Sprintf("%s: kind differs sim=%q sway=%q", path, a.Kind, b.Kind)
	}
	// Layout only meaningful for non-leaf containers and workspaces.
	if a.Layout != b.Layout {
		return fmt.Sprintf("%s: layout differs sim=%q sway=%q", path, a.Layout, b.Layout)
	}
	if a.Floating != b.Floating {
		return fmt.Sprintf("%s: floating differs sim=%v sway=%v", path, a.Floating, b.Floating)
	}
	if a.Fullscreen != b.Fullscreen {
		return fmt.Sprintf("%s: fullscreen differs sim=%v sway=%v", path, a.Fullscreen, b.Fullscreen)
	}
	if a.Focused != b.Focused {
		return fmt.Sprintf("%s: focus differs sim=%v sway=%v", path, a.Focused, b.Focused)
	}
	// Percent compared only when BOTH sides agree it is a real split share
	// (PercentCmp): a tiled child of a splith/splitv parent, with a value
	// set (>0). This excludes floating containers (arbitrary output
	// fraction) and tabbed/stacked children (on-screen share is the full
	// container in sway, but the sim stores the underlying fraction). A
	// fresh sim leaf may also lack percent until a split/resize touches it.
	if a.PercentCmp && b.PercentCmp &&
		a.Percent > 0 && b.Percent > 0 && abs(a.Percent-b.Percent) > 1 {
		return fmt.Sprintf("%s: percent differs sim=%d sway=%d", path, a.Percent, b.Percent)
	}
	if len(a.Children) != len(b.Children) {
		return fmt.Sprintf("%s: child count differs sim=%d sway=%d (%s)",
			path, len(a.Children), len(b.Children), childIDSummary(a, b))
	}
	for i := range a.Children {
		// For leaf windows, also assert id equality (shared window ids).
		ca, cb := a.Children[i], b.Children[i]
		if isLeaf(ca) && isLeaf(cb) && ca.ID != cb.ID {
			return fmt.Sprintf("%s/child%d: leaf id differs sim=%d sway=%d",
				path, i, ca.ID, cb.ID)
		}
		if d := diffNode(ca, cb, fmt.Sprintf("%s/%d", path, i)); d != "" {
			return d
		}
	}
	return ""
}

func isLeaf(n *normNode) bool {
	return len(n.Children) == 0 && (n.Kind == "con" || n.Kind == "floating")
}

func childIDSummary(a, b *normNode) string {
	return fmt.Sprintf("sim_ids=%s sway_ids=%s", idList(a.Children), idList(b.Children))
}

func idList(ns []*normNode) string {
	var sb strings.Builder
	sb.WriteByte('[')
	for i, n := range ns {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "%d:%s", n.ID, n.Kind)
	}
	sb.WriteByte(']')
	return sb.String()
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// NormalizeTree renders a sway tree as an indented, noise-normalized text
// view scoped to non-empty workspaces, for diagnostics.
func NormalizeTree(root *sway.Node) string {
	var sb strings.Builder
	forest := workspaceForest(root)
	var names []string
	for k := range forest {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		renderNode(&sb, forest[name], 0)
	}
	return sb.String()
}

func renderNode(sb *strings.Builder, n *normNode, depth int) {
	indent := strings.Repeat("  ", depth)
	pct := ""
	if n.Percent > 0 {
		pct = fmt.Sprintf(" pct=%d", n.Percent)
	}
	flags := ""
	if n.Floating {
		flags += " FLOAT"
	}
	if n.Fullscreen {
		flags += " FS"
	}
	if n.Focused {
		flags += " *focus"
	}
	fmt.Fprintf(sb, "%sid=%d kind=%s layout=%s%s%s\n",
		indent, n.ID, n.Kind, n.Layout, pct, flags)
	for _, c := range n.Children {
		renderNode(sb, c, depth+1)
	}
}
