package sim

import (
	"fmt"
	"strconv"

	"github.com/mschulkind-oss/tilekeeper/internal/sway"
)

// apply runs one parsed command against the tree. The caller holds s.mu.
// Returning a non-nil error causes the command to be recorded as
// unsupported.
func (s *SimSwayClient) apply(scope, verb string, args []string) error {
	target := s.resolveScope(scope)
	switch verb {
	case "split":
		return s.cmdSplit(target, args)
	case "splith":
		return s.cmdSplitAxis(target, "splith")
	case "splitv":
		return s.cmdSplitAxis(target, "splitv")
	case "layout":
		return s.cmdLayout(target, args)
	case "focus":
		return s.cmdFocus(target, args)
	case "move":
		return s.cmdMove(target, args)
	case "resize":
		return s.cmdResize(target, args)
	case "mark":
		return s.cmdMark(target, args)
	case "unmark":
		return s.cmdUnmark(target, args)
	case "swap":
		return s.cmdSwap(target, args)
	case "floating":
		return s.cmdFloating(target, args)
	}
	return fmt.Errorf("unknown verb %q", verb)
}

// cmdSplit handles "split <none|horizontal|vertical|h|v|toggle>".
func (s *SimSwayClient) cmdSplit(target *sway.Node, args []string) error {
	if target == nil || len(args) == 0 {
		return fmt.Errorf("split needs target and direction")
	}
	switch args[0] {
	case "none":
		return s.flattenSplit(target)
	case "horizontal", "h":
		return s.wrapSplit(target, "splith")
	case "vertical", "v":
		return s.wrapSplit(target, "splitv")
	case "toggle":
		// Toggle current parent axis — pragmatic approximation.
		if target.Parent != nil {
			if target.Parent.Layout == "splith" {
				target.Parent.Layout = "splitv"
			} else {
				target.Parent.Layout = "splith"
			}
		}
		return nil
	}
	return fmt.Errorf("split %s", args[0])
}

func (s *SimSwayClient) cmdSplitAxis(target *sway.Node, axis string) error {
	if target == nil {
		return fmt.Errorf("%s: no target", axis)
	}
	return s.wrapSplit(target, axis)
}

// wrapSplit wraps target in a new parent container of the given layout.
//
// i3/sway's container_split (sway/tree/container.c:1508-1530) has a
// singleton-no-op: when the target has no siblings AND the parent's
// layout is H or V, the parent's layout is updated in-place and no
// wrapper is created. Any other case — including a singleton parent
// whose layout is tabbed/stacked — creates a new wrapper.
func (s *SimSwayClient) wrapSplit(target *sway.Node, layout string) error {
	parent := target.Parent
	if parent == nil {
		return fmt.Errorf("cannot split root")
	}
	if len(parent.Nodes) == 1 &&
		(parent.Layout == "splith" || parent.Layout == "splitv") {
		parent.Layout = layout
		return nil
	}
	// Create a new intermediate "con" node. It steals target's slot in
	// the grandparent (same Rect, same Percent), and target becomes its
	// sole child at 100%. This keeps pixel/percent consistent so a later
	// `resize set width N px` on target observes a real parent width.
	newParent := &sway.Node{
		ID:      s.nextID,
		Type:    "con",
		Layout:  layout,
		Parent:  parent,
		Rect:    target.Rect,
		Percent: target.Percent,
	}
	s.nextID++
	for i, sib := range parent.Nodes {
		if sib == target {
			parent.Nodes[i] = newParent
			break
		}
	}
	newParent.Nodes = []*sway.Node{target}
	target.Parent = newParent
	target.Percent = 1.0
	return nil
}

// flattenSplit implements "split none" via sway's do_unsplit +
// container_flatten sequence.
//
// do_unsplit (sway/commands/split.c:35-51) refuses if the target's parent
// has siblings — we surface that as ErrFlattenSiblings.
//
// container_flatten (sway/tree/container.c:589-601) is a LOOP: while the
// parent container has exactly one child, destroy it (container_replace +
// container_begin_destroy) and ascend. Stops at a parent with >1 children
// or at a workspace/output/root boundary. A single `split none` on a
// deep wrapper chain collapses the entire chain in one call — earlier
// single-level versions of this function hid wrapper-accumulation bugs
// from the fuzzer.
//
// No-op cases (match sway): target is already a direct child of the
// workspace, or the parent has no grandparent.
func (s *SimSwayClient) flattenSplit(target *sway.Node) error {
	parent := target.Parent
	if parent == nil || parent.Type == "workspace" {
		return nil
	}
	if parent.Parent == nil {
		return nil
	}
	if len(parent.Nodes) != 1 {
		return ErrFlattenSiblings
	}
	for {
		p := target.Parent
		if p == nil || p.Type == "workspace" || p.Type == "output" || p.Type == "root" {
			break
		}
		if len(p.Nodes) != 1 {
			break
		}
		g := p.Parent
		if g == nil {
			break
		}
		for i, sib := range g.Nodes {
			if sib == p {
				g.Nodes[i] = target
				break
			}
		}
		target.Parent = g
		// The survivor takes the destroyed wrapper's SLOT, so it inherits
		// the wrapper's geometry share. sway's container_flatten →
		// container_replace copies the parent's width_fraction/percent (and
		// the laid-out rect) onto the child that fills its place
		// (sway/tree/container.c container_replace). Without this the
		// flattened leaf keeps the 1.0 percent wrapSplit gave it as a
		// singleton, so a splitv-then-`split none` round-trip wrongly
		// reports percent=100 where real sway restores the original 50.
		target.Percent = p.Percent
		target.Rect = p.Rect
		p.Nodes = nil
		p.Parent = nil
	}
	return nil
}

// cmdLayout changes the target parent container's layout, mirroring
// sway's cmd_layout (sway/commands/layout.c:117-199). It's more than
// "set parent.layout": sway walks up to the parent container (like i3),
// flattens a singleton grandparent once if both parent and grandparent
// are singletons, and — crucially — when the scope is a container but
// the walked-up parent is NULL (target was workspace-direct), wraps
// ALL workspace children in a new container and sets *its* layout
// (workspace_wrap_children). That wrap path is the main way MasterStack
// accumulates wrapper chains: every `layout X` on a workspace-direct
// leaf introduces a fresh wrapper.
func (s *SimSwayClient) cmdLayout(target *sway.Node, args []string) error {
	if target == nil || len(args) == 0 {
		return fmt.Errorf("layout needs target and mode")
	}
	mode := args[0]
	// Normalize "stacking" → "stacked" for IPC consistency
	// (sway/ipc-json.c:55-56 emits "stacked", sway/commands/layout.c:18-19
	// accepts "stacking" as input).
	var newLayout string
	switch mode {
	case "splith", "splitv", "tabbed":
		newLayout = mode
	case "stacking", "stacked":
		newLayout = "stacked"
	case "toggle":
		return s.cmdLayoutToggle(target)
	default:
		return fmt.Errorf("layout %s", mode)
	}

	wsScope := target.Type == "workspace"
	ws := target
	if !wsScope {
		ws = target.FindWorkspace()
	}

	// Walk up to the parent container (sway treats cmd_layout's target
	// as the parent of the original scope, like i3).
	var container *sway.Node
	if !wsScope {
		container = target.Parent
		// Flatten-once: if container is singleton AND grandparent is a
		// singleton (container OR workspace), replace grandparent's child
		// (=container) with container's child (=target), destroy container,
		// re-point to grandparent. sway/commands/layout.c:139-148 checks
		// parent->pending.children->length==1 with no type gate — so a
		// workspace grandparent with 1 child also qualifies.
		if container != nil && container.Type == "con" && len(container.Nodes) == 1 {
			grand := container.Parent
			if grand != nil && (grand.Type == "con" || grand.Type == "workspace") && len(grand.Nodes) == 1 {
				child := container.Nodes[0]
				for i, sib := range grand.Nodes {
					if sib == container {
						grand.Nodes[i] = child
						break
					}
				}
				child.Parent = grand
				container.Nodes = nil
				container.Parent = nil
				container = grand
			}
		}
		// Workspace is not a container-parent in sway; the walked-up
		// pointer is NULL when the immediate parent is the workspace.
		if container != nil && container.Type == "workspace" {
			container = nil
		}
	}

	var oldLayout string
	if container != nil {
		oldLayout = container.Layout
	} else if ws != nil {
		oldLayout = ws.Layout
	}
	if newLayout == oldLayout {
		return nil
	}
	if container != nil {
		container.Layout = newLayout
		return nil
	}
	// container == nil. If scope was a container (not the workspace),
	// sway wraps all workspace children in a new container and sets the
	// new wrapper's layout. This is workspace_wrap_children
	// (sway/tree/workspace.c:898-910).
	if !wsScope && ws != nil {
		wrapper := &sway.Node{
			ID:     s.nextID,
			Type:   "con",
			Layout: ws.Layout,
			Parent: ws,
		}
		s.nextID++
		wrapper.Nodes = ws.Nodes
		for _, c := range wrapper.Nodes {
			c.Parent = wrapper
		}
		ws.Nodes = []*sway.Node{wrapper}
		wrapper.Layout = newLayout
		return nil
	}
	// Workspace-scope: change ws layout directly.
	if ws != nil {
		ws.Layout = newLayout
	}
	return nil
}

// cmdLayoutToggle handles "layout toggle [split]", a pragmatic splith↔splitv
// swap on the target's parent container. Sway's full toggle grammar is richer
// (sway/commands/layout.c:47-94) but no layout manager issues those variants.
func (s *SimSwayClient) cmdLayoutToggle(target *sway.Node) error {
	n := target
	if len(n.Nodes) == 0 && n.Parent != nil {
		n = n.Parent
	}
	if n.Layout == "splith" {
		n.Layout = "splitv"
	} else {
		n.Layout = "splith"
	}
	return nil
}

// cmdFocus handles "focus" (focus target) and "focus left|right|up|down"
// (move focus to a sibling in that direction).
func (s *SimSwayClient) cmdFocus(target *sway.Node, args []string) error {
	if len(args) == 0 {
		if target == nil {
			return nil
		}
		s.setFocus(target)
		return nil
	}
	dir := args[0]
	if target == nil {
		target = s.root.FindFocused()
	}
	if target == nil {
		return nil
	}
	sibling := s.directionalSibling(target, dir)
	if sibling == nil {
		return nil
	}
	s.setFocus(sibling)
	return nil
}

func (s *SimSwayClient) setFocus(n *sway.Node) {
	// Clear Focused everywhere, then set on n.
	var walk func(node *sway.Node)
	walk = func(node *sway.Node) {
		node.Focused = false
		for _, c := range node.Nodes {
			walk(c)
		}
		for _, c := range node.FloatingNodes {
			walk(c)
		}
	}
	walk(s.root)
	n.Focused = true
}

// directionalSibling finds the sibling of n in direction dir. "left" /
// "right" works along splith parents; "up" / "down" along splitv.
func (s *SimSwayClient) directionalSibling(n *sway.Node, dir string) *sway.Node {
	for cur := n; cur.Parent != nil; cur = cur.Parent {
		p := cur.Parent
		// is_parallel(layout, dir): tabbed matches only horizontal,
		// stacked matches only vertical. sway/commands/move.c:79-91.
		parallel := false
		switch dir {
		case "left", "right":
			parallel = p.Layout == "splith" || p.Layout == "tabbed"
		case "up", "down":
			parallel = p.Layout == "splitv" || p.Layout == "stacked"
		default:
			return nil
		}
		if !parallel {
			continue
		}
		idx := -1
		for i, c := range p.Nodes {
			if c == cur {
				idx = i
				break
			}
		}
		if idx < 0 {
			continue
		}
		nextIdx := idx
		if dir == "right" || dir == "down" {
			nextIdx++
		} else {
			nextIdx--
		}
		if nextIdx < 0 || nextIdx >= len(p.Nodes) {
			continue
		}
		leaf := p.Nodes[nextIdx]
		for len(leaf.Nodes) > 0 {
			leaf = leaf.Nodes[0]
		}
		return leaf
	}
	return nil
}

// cmdMove handles:
//
//	move left|right|up|down
//	move window to mark NAME
//	move to mark NAME
//	move container to workspace NAME
//
// Real sway silently no-ops directional moves when there's no focused
// container — we mirror that for unscoped commands so the native fallback
// in handleBindingEvent doesn't register as an invalid-command violation.
func (s *SimSwayClient) cmdMove(target *sway.Node, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("move: no args")
	}
	if target == nil {
		return nil
	}
	switch args[0] {
	case "left", "right", "up", "down":
		return s.moveDir(target, args[0])
	case "window", "container", "to":
		// "move window to mark NAME", "move container to workspace NAME",
		// "move to mark NAME"
		rest := args
		if rest[0] == "window" || rest[0] == "container" {
			rest = rest[1:]
		}
		if len(rest) >= 3 && rest[0] == "to" && rest[1] == "mark" {
			return s.moveToMark(target, rest[2])
		}
		if len(rest) >= 3 && rest[0] == "to" && rest[1] == "workspace" {
			return s.moveToWorkspace(target, rest[2])
		}
	}
	return fmt.Errorf("move %v", args)
}

func (s *SimSwayClient) moveDir(target *sway.Node, dir string) error {
	p := target.Parent
	if p == nil {
		return nil
	}
	idx := -1
	for i, c := range p.Nodes {
		if c == target {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	swap := idx
	switch dir {
	case "right", "down":
		swap = idx + 1
	case "left", "up":
		swap = idx - 1
	}
	if swap < 0 || swap >= len(p.Nodes) {
		return nil
	}
	p.Nodes[idx], p.Nodes[swap] = p.Nodes[swap], p.Nodes[idx]
	return nil
}

func (s *SimSwayClient) moveToMark(target *sway.Node, mark string) error {
	targetID, ok := s.marks[mark]
	if !ok {
		return nil
	}
	dest := s.root.FindByID(targetID)
	if dest == nil || dest.Parent == nil {
		return nil
	}
	// Self-move is a no-op — detaching target would also unwire dest since
	// they're the same node, leaving dest.Parent nil and corrupting the
	// subsequent sibling-insert. Real sway silently succeeds in this case
	// (the container ends up adjacent to itself, i.e. unchanged).
	if dest == target {
		return nil
	}
	// FLOATING SOURCE: container_move_to_container (sway/commands/move.c:
	// 247-250) short-circuits a floating source into
	// container_move_to_workspace(dest's workspace) — which early-returns
	// for same-workspace (move.c:200-202). So a same-workspace
	// move-to-mark of a floating container is a SILENT NO-OP, and a
	// cross-workspace one relocates the float without tiling it. This is
	// the op=114 `move 77 to mark on 17` no-op from the 2026-06-12
	// incident.
	if target.IsFloating() {
		srcWS := target.FindWorkspace()
		destWS := dest.FindWorkspace()
		if srcWS == destWS || destWS == nil {
			return nil
		}
		detachFromAnyList(target)
		destWS.FloatingNodes = append(destWS.FloatingNodes, target)
		target.Parent = destWS
		return nil
	}
	// FLOATING DESTINATION: container_add_sibling on a floating dest
	// inserts the moved container into container_get_siblings(dest) — the
	// workspace floating list (sway/tree/container.c:1410-1423 via
	// move.c:257-260). The tiled source FLOATS, with only a window::move
	// event emitted (no window::floating — that emitter lives solely in
	// container_set_floating). This is the op=115 vector that silently
	// floated window 18 in the 2026-06-12 incident.
	if dest.IsFloating() {
		ws := dest.FindWorkspace()
		if ws == nil {
			return nil
		}
		// Detach without cascade so the now-shorter source row can be
		// rescaled to refill, matching sway's arrange after the moved tiled
		// window floats out (the survivors retile the full width). The
		// insert side (target joining the floating list) needs no rescale —
		// floating containers don't share a split row.
		oldParent := detachFromAnyList(target)
		rescaleTiledRowToFull(oldParent)
		if oldParent != nil {
			s.cascadeFlatten(oldParent)
		}
		target.Floating = dest.Floating
		ws.FloatingNodes = append(ws.FloatingNodes, target)
		target.Parent = ws
		return nil
	}
	s.detach(target)
	// Insert target as next sibling of dest.
	destParent := dest.Parent
	if destParent == nil {
		// dest may have been cascade-flattened during detach(target) if
		// they shared an ancestor with no other siblings.
		return nil
	}
	idx := 0
	for i, c := range destParent.Nodes {
		if c == dest {
			idx = i + 1
			break
		}
	}
	destParent.Nodes = append(destParent.Nodes, nil)
	copy(destParent.Nodes[idx+1:], destParent.Nodes[idx:])
	destParent.Nodes[idx] = target
	target.Parent = destParent
	return nil
}

func (s *SimSwayClient) moveToWorkspace(target *sway.Node, name string) error {
	// Real sway early-returns when destination workspace equals source:
	// container_move_to_workspace (sway/commands/move.c:200-202) bails
	// before any detach or arrange. Previously the sim fell through to
	// detach+re-attach, which incidentally flattened intermediate wrappers
	// — hiding MasterStack's wrapper-accumulation bug (live ws7 nested
	// 12 singleton containers deep) from the fuzzer.
	if tw := target.FindWorkspace(); tw != nil && tw.Name == name {
		return nil
	}
	for _, ws := range s.root.Workspaces() {
		if ws.Name != name {
			continue
		}
		// A floating source STAYS floating on the destination workspace:
		// container_move_to_workspace re-adds it via workspace_add_floating
		// (sway/commands/move.c). Tiling it here would mint a node with
		// Floating="user_on" sitting in ws.Nodes — a state real sway
		// cannot produce.
		if target.IsFloating() {
			detachFromAnyList(target)
			ws.FloatingNodes = append(ws.FloatingNodes, target)
			target.Parent = ws
			return nil
		}
		s.detach(target)
		ws.Nodes = append(ws.Nodes, target)
		target.Parent = ws
		return nil
	}
	return nil
}

func (s *SimSwayClient) detach(n *sway.Node) {
	p := detachFromAnyList(n)
	if p == nil {
		return
	}
	s.cascadeFlatten(p)
}

// detachFromAnyList unlinks n from whichever list on its parent holds it
// (Nodes or FloatingNodes) and clears n.Parent. Returns the former parent
// for caller-chosen followups (e.g. cascadeFlatten). Does NOT reap.
func detachFromAnyList(n *sway.Node) *sway.Node {
	p := n.Parent
	if p == nil {
		return nil
	}
	for i, c := range p.Nodes {
		if c == n {
			p.Nodes = append(p.Nodes[:i], p.Nodes[i+1:]...)
			n.Parent = nil
			return p
		}
	}
	for i, c := range p.FloatingNodes {
		if c == n {
			p.FloatingNodes = append(p.FloatingNodes[:i], p.FloatingNodes[i+1:]...)
			n.Parent = nil
			return p
		}
	}
	return p
}

// cascadeFlatten walks upward from parent, pruning only EMPTY non-structural
// containers. This mirrors sway's container_reap_empty
// (sway/tree/container.c:571-590), which destroys a container only when
// pending.children->length is zero — it does NOT auto-collapse single-child
// containers. Singletons persist in sway until an explicit
// `split none` / container_container_ungroup runs.
//
// Earlier versions of this function also collapsed single-child containers
// (case 1). That was too aggressive and hid real wrapper-accumulation bugs
// from the fuzzer (see the moveToWorkspace comment re: live ws7 nested 12
// wrappers deep). Workspaces, outputs, and the root are never reaped.
func (s *SimSwayClient) cascadeFlatten(start *sway.Node) {
	for cur := start; cur != nil; {
		if cur.Type == "workspace" || cur.Type == "output" || cur.Type == "root" {
			return
		}
		grand := cur.Parent
		if grand == nil {
			return
		}
		if len(cur.Nodes) != 0 {
			return
		}
		for i, c := range grand.Nodes {
			if c == cur {
				grand.Nodes = append(grand.Nodes[:i], grand.Nodes[i+1:]...)
				break
			}
		}
		cur.Parent = nil
		cur = grand
	}
}

// cmdResize handles "resize set width N ppt|px" and "resize set height …".
//
// ppt is modeled by mutating Percent on the target and rescaling its
// siblings so the row sums to 1. We don't redistribute pixel widths — the
// fuzzer/invariants read Percent, which is what real sway exposes via
// get_tree's "percent" field. Pixel rects in the sim are advisory only;
// nothing the engine reads depends on them.
//
// px on tiled targets is treated equivalently for our purposes: we
// convert it to a percent against the parent's current pixel width when
// possible, otherwise fall back to a literal Rect mutation. This keeps
// `resize set width 75 ppt` and `resize set width <px> px` (issued by
// popWindow when restoring lastKnownMasterPx) both observable as
// percent changes the invariant can read.
func (s *SimSwayClient) cmdResize(target *sway.Node, args []string) error {
	if target == nil || len(args) < 4 {
		return fmt.Errorf("resize: not enough args")
	}
	if args[0] != "set" {
		return fmt.Errorf("resize %s unsupported", args[0])
	}
	dim := args[1]
	val, err := strconv.Atoi(args[2])
	if err != nil {
		return err
	}
	if val <= 0 {
		return ErrResizeNonPositive
	}
	unit := args[3]
	if unit != "ppt" && unit != "px" {
		return fmt.Errorf("resize unit %q unsupported (want ppt or px)", unit)
	}
	switch dim {
	case "width":
		if unit == "ppt" {
			setSiblingPercent(target, float64(val)/100)
		} else { // px
			if target.Parent != nil && target.Parent.Rect.Width > 0 {
				setSiblingPercent(target, float64(val)/float64(target.Parent.Rect.Width))
			}
			target.Rect.Width = val
		}
	case "height":
		if unit == "ppt" {
			setSiblingPercent(target, float64(val)/100)
		} else {
			if target.Parent != nil && target.Parent.Rect.Height > 0 {
				setSiblingPercent(target, float64(val)/float64(target.Parent.Rect.Height))
			}
			target.Rect.Height = val
		}
	default:
		return fmt.Errorf("resize set %s", dim)
	}
	return nil
}

// setSiblingPercent assigns target.Percent and rescales its siblings so
// the parent's children (target included) sum to 1. If target has no
// parent or no siblings, only target.Percent is set. p is clamped to
// (0, 1); a request for 100% with siblings present is reduced to leave
// each sibling a minimum 1% (matches sway/tree/container.c:resize_tiled
// floor behavior closely enough for invariants).
//
// Pixel widths are kept in sync: target.Rect.Width is recomputed as
// parent.Rect.Width × percent, and siblings rescaled likewise. Without
// this, popWindow's lastKnownMasterPx snapshot reads a stale leaf
// Rect.Width and emits a `resize set width N px` whose N has no
// relation to the master's true width — the dominant source of false
// positives on the master-width invariant.
func setSiblingPercent(target *sway.Node, p float64) {
	if target == nil {
		return
	}
	if p <= 0 {
		p = 0.01
	}
	parent := target.Parent
	if parent == nil || len(parent.Nodes) <= 1 {
		target.Percent = p
		if parent != nil && parent.Rect.Width > 0 {
			target.Rect.Width = int(float64(parent.Rect.Width) * p)
		}
		return
	}
	siblings := parent.Nodes
	minOther := 0.01
	maxP := 1 - float64(len(siblings)-1)*minOther
	if p > maxP {
		p = maxP
	}
	var others []*sway.Node
	var oldOtherSum float64
	for _, sib := range siblings {
		if sib == target {
			continue
		}
		others = append(others, sib)
		oldOtherSum += sib.Percent
	}
	target.Percent = p
	newOtherSum := 1 - p
	if oldOtherSum > 0 {
		scale := newOtherSum / oldOtherSum
		for _, sib := range others {
			sib.Percent *= scale
		}
	} else {
		each := newOtherSum / float64(len(others))
		for _, sib := range others {
			sib.Percent = each
		}
	}
	if parent.Rect.Width > 0 {
		applyPixelWidths(parent)
	}
	if parent.Rect.Height > 0 {
		applyPixelHeights(parent)
	}
}

// rescaleTiledRowToFull proportionally rescales parent's tiled children so
// their Percent shares sum to 1, matching sway's arrange after a container
// leaves the row (float, move-out, close). Only split rows are affected —
// tabbed/stacked containers display every child full-size, so their stored
// fractions are left untouched. A no-op when parent is nil, has no tiled
// children, or those children carry no percent yet (a brand-new row sway
// would itself lay out lazily). This keeps the master-width invariant's
// view of the row faithful: dropping one of N equal windows leaves N−1
// windows that still tile the full width.
func rescaleTiledRowToFull(parent *sway.Node) {
	if parent == nil || len(parent.Nodes) == 0 {
		return
	}
	if parent.Layout != "splith" && parent.Layout != "splitv" {
		return
	}
	var sum float64
	for _, c := range parent.Nodes {
		sum += c.Percent
	}
	if sum <= 0 {
		return
	}
	scale := 1.0 / sum
	for _, c := range parent.Nodes {
		c.Percent *= scale
	}
	if parent.Rect.Width > 0 {
		applyPixelWidths(parent)
	}
	if parent.Rect.Height > 0 {
		applyPixelHeights(parent)
	}
}

// applyPixelWidths recomputes Rect.Width for every child of parent
// based on its Percent share, treating splitv/tabbed/stacked as full-
// width (siblings don't subdivide horizontally).
func applyPixelWidths(parent *sway.Node) {
	if parent == nil || parent.Rect.Width == 0 {
		return
	}
	splitsHorizontally := parent.Layout == "splith"
	for _, c := range parent.Nodes {
		if splitsHorizontally && c.Percent > 0 {
			c.Rect.Width = int(float64(parent.Rect.Width) * c.Percent)
		} else {
			c.Rect.Width = parent.Rect.Width
		}
	}
}

func applyPixelHeights(parent *sway.Node) {
	if parent == nil || parent.Rect.Height == 0 {
		return
	}
	splitsVertically := parent.Layout == "splitv"
	for _, c := range parent.Nodes {
		if splitsVertically && c.Percent > 0 {
			c.Rect.Height = int(float64(parent.Rect.Height) * c.Percent)
		} else {
			c.Rect.Height = parent.Rect.Height
		}
	}
}

// cmdMark handles "mark --add NAME" and "mark NAME".
//
// Sway's cmd_mark (sway/commands/mark.c:15-67) enforces global mark
// uniqueness: after any --add/replace, container_find_and_unmark strips
// the mark from any OTHER container that holds it. Earlier sim versions
// only updated s.marks[name] and left the stale mark on the previous
// owner's Marks slice.
func (s *SimSwayClient) cmdMark(target *sway.Node, args []string) error {
	if target == nil || len(args) == 0 {
		return fmt.Errorf("mark: no target or args")
	}
	name := args[len(args)-1]
	add := false
	for _, a := range args {
		if a == "--add" {
			add = true
		}
	}

	// Strip `name` from every OTHER container (sway/tree/container.c:1645-1663).
	var walk func(n *sway.Node)
	walk = func(n *sway.Node) {
		if n != target {
			for i, m := range n.Marks {
				if m == name {
					n.Marks = append(n.Marks[:i], n.Marks[i+1:]...)
					break
				}
			}
		}
		for _, c := range n.Nodes {
			walk(c)
		}
		for _, c := range n.FloatingNodes {
			walk(c)
		}
	}
	walk(s.root)

	if !add {
		target.Marks = []string{name}
	} else {
		has := false
		for _, m := range target.Marks {
			if m == name {
				has = true
				break
			}
		}
		if !has {
			target.Marks = append(target.Marks, name)
		}
	}
	s.marks[name] = target.ID
	return nil
}

// cmdUnmark removes a mark (by name, or all marks on target).
func (s *SimSwayClient) cmdUnmark(target *sway.Node, args []string) error {
	if target == nil {
		return nil
	}
	if len(args) == 0 {
		for _, m := range target.Marks {
			delete(s.marks, m)
		}
		target.Marks = nil
		return nil
	}
	name := args[0]
	out := target.Marks[:0]
	for _, m := range target.Marks {
		if m != name {
			out = append(out, m)
		}
	}
	target.Marks = out
	delete(s.marks, name)
	return nil
}

// cmdSwap handles "swap container with con_id N".
//
// Sway's cmd_swap (sway/commands/swap.c:70-83) returns CMD_FAILURE when:
// target not found, current == other, or either is an ancestor of the
// other. We surface those as ErrSwayRejected so the fuzzer catches any
// engine emitting an ill-formed swap. Marks are NOT touched by swap —
// swap operates on tree position + geometry only (swap_places at
// container.c:1781+).
func (s *SimSwayClient) cmdSwap(target *sway.Node, args []string) error {
	if target == nil || len(args) < 3 {
		return fmt.Errorf("swap: need target and con_id")
	}
	if args[0] != "container" || args[1] != "with" {
		return fmt.Errorf("swap %v", args)
	}
	if len(args) >= 4 && args[2] == "con_id" {
		id, err := strconv.ParseInt(args[3], 10, 64)
		if err != nil {
			return err
		}
		other := s.root.FindByID(id)
		if other == nil {
			return fmt.Errorf("%w: swap target con_id %d not found", ErrSwayRejected, id)
		}
		if other == target {
			return fmt.Errorf("%w: swap with self", ErrSwayRejected)
		}
		if isAncestor(target, other) || isAncestor(other, target) {
			return fmt.Errorf("%w: swap with ancestor/descendant", ErrSwayRejected)
		}
		swapNodes(target, other)
		return nil
	}
	return fmt.Errorf("swap %v", args)
}

func isAncestor(ancestor, node *sway.Node) bool {
	for p := node.Parent; p != nil; p = p.Parent {
		if p == ancestor {
			return true
		}
	}
	return false
}

// swapNodes exchanges a and b in their respective parents.
//
// Floatingness in real sway is POSITIONAL — membership in the workspace's
// floating list, not a flag. swap_places (sway/tree/container.c:1718-1764)
// has no floating guard: when one endpoint is floating and the other
// tiled, each container takes the other's slot, so the tiled one lands in
// the floating list (it floats) and the floating one takes the tiled slot
// (it tiles). Swap emits NO window::floating and NO window::move events
// (the only window::floating emitter in the sway tree is
// container_set_floating, which swap never calls; the journal's move
// events during a dance come from `move to mark` steps). That silent
// transfer is the mechanism that scrambled live ws7 on 2026-06-12: a
// master-insert dance swapped a tracked tiled window with a portal
// save-dialog that had already floated, bleeding floatingness onto the
// tracked window with no corrective event.
//
// Geometry travels with the slot (swap_places copies x/y/width/height and
// width_fraction), so Rect/Percent and the Floating string are exchanged
// along with list membership. Fullscreen migrates to the partner too:
// container_swap saves both endpoints' fullscreen modes, disables, swaps,
// and re-applies each saved mode to the OTHER container — emitting
// window::fullscreen_mode (which the daemon filters) but, as above,
// never window::floating or window::move.
func swapNodes(a, b *sway.Node) {
	pa, pb := a.Parent, b.Parent
	if pa == nil || pb == nil {
		return
	}
	la, ia := findInParentLists(pa, a)
	lb, ib := findInParentLists(pb, b)
	if ia < 0 || ib < 0 {
		return
	}
	(*la)[ia] = b
	(*lb)[ib] = a
	a.Parent, b.Parent = pb, pa
	a.Floating, b.Floating = b.Floating, a.Floating
	a.Rect, b.Rect = b.Rect, a.Rect
	a.Percent, b.Percent = b.Percent, a.Percent
	a.FullscreenMode, b.FullscreenMode = b.FullscreenMode, a.FullscreenMode
}

// findInParentLists locates n in p.Nodes or p.FloatingNodes, returning
// the containing list and index (-1 if absent from both).
func findInParentLists(p *sway.Node, n *sway.Node) (*[]*sway.Node, int) {
	for i, c := range p.Nodes {
		if c == n {
			return &p.Nodes, i
		}
	}
	for i, c := range p.FloatingNodes {
		if c == n {
			return &p.FloatingNodes, i
		}
	}
	return nil, -1
}

// cmdFloating handles "floating enable|disable|toggle".
//
// Real sway's container_set_floating (sway/tree/container.c:1004-1079)
// does NOT just flip a flag — enabling detaches the container from its
// tiled parent, appends it to workspace->floating, and calls
// container_reap_empty(old_parent). Disabling does the reverse: detach
// from floating list, re-insert into the tiled tree.
//
// The flag-only approximation in earlier versions hid the real structural
// consequences from the fuzzer — e.g. a `for_window floating enable` rule
// firing on the master would leave the master visibly in the tiled tree
// in sim while real sway had already moved it to workspace.floating.
func (s *SimSwayClient) cmdFloating(target *sway.Node, args []string) error {
	if target == nil || len(args) == 0 {
		return fmt.Errorf("floating: no target or mode")
	}
	mode := args[0]
	if mode == "toggle" {
		if target.IsFloating() {
			mode = "disable"
		} else {
			mode = "enable"
		}
	}
	switch mode {
	case "enable":
		// Positional early-return like container_set_floating: already
		// floating (user_on OR auto_on) means no-op — a string-equality
		// check against "user_on" only would re-detach auto_on floats.
		if target.IsFloating() {
			return nil
		}
		ws := target.FindWorkspace()
		oldParent := detachFromAnyList(target)
		target.Floating = "user_on"
		if ws != nil {
			ws.FloatingNodes = append(ws.FloatingNodes, target)
			target.Parent = ws
		}
		// Floating a tiled window removes it from its split row, so sway's
		// arrange rescales the remaining tiled siblings to refill the row
		// (a row of three 0.33 windows becomes two 0.5 windows when one
		// floats). Without this the survivors keep their old fractions and
		// the row no longer sums to 1 — a divergence the difftest surfaces.
		rescaleTiledRowToFull(oldParent)
		if oldParent != nil && oldParent != ws && oldParent.Type == "con" {
			s.cascadeFlatten(oldParent)
		}
		return nil
	case "disable":
		// Same positional check: a tiled node (Floating "", "auto_off",
		// or "user_off") must no-op — real sway never restructures here,
		// while the old string check detached-and-reappended it.
		if !target.IsFloating() {
			return nil
		}
		ws := target.FindWorkspace()
		detachFromAnyList(target)
		target.Floating = "user_off"
		if ws != nil {
			ws.Nodes = append(ws.Nodes, target)
			target.Parent = ws
		}
		return nil
	}
	return fmt.Errorf("floating %s", args[0])
}
