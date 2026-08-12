package layout

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/mschulkind-oss/tilekeeper/internal/sway"
)

// Tabbed is a thin pass-through layout: it asks sway to set the
// workspace container's layout to "tabbed" and otherwise stays out of
// the way. Sway draws the tab strip; new windows join the tabbed
// container automatically.
//
// The manager only emits a command when the workspace's current layout
// is not already "tabbed", so repeated WindowAdded / ArrangeAll calls
// are no-ops on a steady-state workspace.
type Tabbed struct {
	mu     sync.Mutex
	conn   sway.Client
	logger *slog.Logger // optional; nil silences logs
}

// NewTabbed constructs a Tabbed manager.
func NewTabbed(conn sway.Client) *Tabbed {
	return &Tabbed{conn: conn}
}

// SetLogger attaches a component-scoped logger for this manager.
func (t *Tabbed) SetLogger(l *slog.Logger) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.logger = l
}

func (t *Tabbed) log() *slog.Logger {
	if t.logger == nil {
		return slog.Default()
	}
	return t.logger
}

func (t *Tabbed) Name() string { return "tabbed" }

func (t *Tabbed) WindowIDs() []int64 { return nil }

func (t *Tabbed) WindowAdded(ws *sway.Node, win *sway.Node) error {
	if win != nil {
		t.log().Debug("window added", "con_id", win.ID, "name", win.Name, "app_id", win.AppID)
	}
	return t.ensure(ws)
}

// WindowRemoved re-establishes the strip. Losing a window can dissolve it:
// sway reaps the container when the last tab leaves, and a `split none` from
// elsewhere (e.g. FlattenSingletons during a layout switch) collapses a
// one-tab strip into a bare workspace-direct window — leaving the workspace
// with no tab bar at all until the next window happens to open.
func (t *Tabbed) WindowRemoved(ws *sway.Node, win *sway.Node) error {
	if win != nil {
		t.log().Debug("window removed", "con_id", win.ID, "name", win.Name)
	}
	return t.ensure(ws)
}

func (t *Tabbed) WindowFocused(_ *sway.Node, win *sway.Node) error {
	if win != nil {
		t.log().Debug("window focused", "con_id", win.ID, "name", win.Name)
	}
	return nil
}

func (t *Tabbed) ArrangeAll(ws *sway.Node) error {
	t.log().Debug("arrange-all", "workspace", wsName(ws))
	return t.ensure(ws)
}

// Command forwards navigation verbs to sway. Bindings come in as
// `nop tilekeeper move left`, so sway itself does nothing — the binding only
// fires to tell tilekeeper. For tabbed workspaces most bindings are
// meaningful as plain sway commands (move left/right reorders tabs, focus
// left/right cycles them), so we forward the whitelist below.
//
// If the workspace has no windows, we skip — real sway returns CMD_FAILURE
// ("Cannot move workspaces in a direction") when no container is focused,
// and forwarding produces noise without effect.
//
// Directional MOVES are additionally clamped to the tab strip — see
// moveStaysInStrip. Focus is not clamped: at an edge sway's directional
// focus either stays put or crosses to another output, and neither
// restructures the tree.
func (t *Tabbed) Command(cmd string, ws *sway.Node) error {
	switch cmd {
	case "focus up", "focus down", "focus left", "focus right":
		if ws == nil || len(ws.Leaves()) == 0 {
			t.log().Debug("skipping nav on empty workspace", "command", cmd)
			return nil
		}
		t.log().Debug("forwarding nav command to sway", "command", cmd)
		return t.conn.RunCommand(cmd)
	case "move up", "move down", "move left", "move right":
		if ws == nil || len(ws.Leaves()) == 0 {
			t.log().Debug("skipping nav on empty workspace", "command", cmd)
			return nil
		}
		dir := strings.TrimPrefix(cmd, "move ")
		if !t.moveStaysInStrip(ws, dir) {
			t.log().Debug("move would leave the tab strip; stopping at the edge",
				"command", cmd, "workspace", ws.Name)
			return nil
		}
		t.log().Debug("forwarding nav command to sway", "command", cmd)
		return t.conn.RunCommand(cmd)
	default:
		t.log().Debug("command ignored for tabbed layout", "command", cmd)
		return nil
	}
}

// moveStaysInStrip reports whether `move <dir>` on this workspace's focused
// window would REORDER it among its own siblings, rather than tear it out of
// the tab strip.
//
// Sway's `move` does not stop at the end of a container. When the focused
// window has no sibling in the requested direction,
// container_move_in_direction (sway/commands/move.c:301-413) walks up the
// ancestor chain and PROMOTES the window out of its parent — and when no
// ancestor is parallel to the direction at all (any vertical move on a tab
// strip) it first wraps every workspace child in a new container and
// re-orients the workspace itself. Either way the window lands beside the
// tabs at half width and the strip is broken; the hub ignores same-workspace
// window::move events, so nothing puts it back. That is the ws8 report from
// 2026-08-12, and it is why the last tab moving right is just as destructive
// as the first tab moving left.
//
// So the rule is narrow on purpose: forward only the swap-two-tabs case —
// the parent is laid out along the move axis AND the neighbour on that side
// is a WINDOW. Everything else stops at the edge, which is what a tab strip
// should do. It holds whichever shape the strip has: the workspace container
// itself tabbed (what ensure builds), or a tabbed container inside the
// workspace (what a plain `layout tabbed` binding builds).
//
// The neighbour must be a window because sway does not swap with a
// CONTAINER neighbour — container_move_to_container_from_direction reparents
// the mover INTO it (move.c:140-165), which empties the mover's old slot and
// leaves the wrapper chain behind. On a healthy strip every neighbour is a
// window, so this only bites on a workspace whose tree is already nested,
// where forwarding would deepen the mess rather than reorder a tab.
//
// Floating windows are exempt: sway moves them by a pixel delta and never
// re-parents them, so a floating `move left` cannot break the strip.
func (t *Tabbed) moveStaysInStrip(ws *sway.Node, dir string) bool {
	focused := ws.FindFocused()
	if focused == nil {
		// The binding named this workspace but focus is elsewhere. A bare
		// `move` would act on whatever IS focused — never what the user meant.
		return false
	}
	if focused.IsFloating() {
		return true
	}
	// A fullscreen container only moves BETWEEN OUTPUTS, never within the
	// strip (move.c:305-312).
	if focused.FullscreenMode != 0 {
		return false
	}
	parent := focused.Parent
	if parent == nil || !layoutParallelTo(parent.Layout, dir) {
		return false
	}
	for i, sib := range parent.Nodes {
		if sib != focused {
			continue
		}
		next := i + 1
		if dir == "left" || dir == "up" {
			next = i - 1
		}
		if next < 0 || next >= len(parent.Nodes) {
			return false // at the first/last tab: stop here
		}
		return len(parent.Nodes[next].Nodes) == 0 // a window, not a container
	}
	return false
}

// layoutParallelTo is sway's is_parallel (sway/commands/move.c:79-91): a
// tabbed container is laid out horizontally, a stacked one vertically.
func layoutParallelTo(layout, dir string) bool {
	switch dir {
	case "left", "right":
		return layout == "splith" || layout == "tabbed"
	case "up", "down":
		return layout == "splitv" || layout == "stacked"
	}
	return false
}

// tabGatherMark is the scratch mark used to pull a stray window into the
// strip. Scoped to one move (added, used, removed).
const tabGatherMark = "tk_tab_gather"

// maxEnsureRounds bounds the repair loop. Each round issues commands only
// when it changed something, so a healthy workspace costs one tree read.
const maxEnsureRounds = 8

// ensure drives the workspace toward its one defining shape: every tiled
// window is a tab in ONE strip, and that strip is the workspace's only tiled
// child.
//
// THE STRIP IS A CONTAINER, NOT THE WORKSPACE. `[workspace=N] layout tabbed`
// — what this used to issue — does not tab the workspace container at all:
// sway criteria match views (sway/criteria.c), so it runs `layout tabbed`
// once per window, and cmd_layout, finding no container parent above a
// workspace-direct window, wraps every workspace child in a new tabbed
// container instead. The workspace stays splith. So the old pair of
// "workspace layout tabbed" + "lift every leaf to workspace level" was
// chasing a shape sway will not build, and the lift step could not even find
// an anchor once every leaf had been wrapped. Verified against headless sway
// (cmd/sway-difftest workspace-criteria-layout); see
// docs/sway-model-verification.md §13.
//
// A workspace whose CONTAINER is genuinely tabbed (sway's workspace_layout,
// or a user command) is also a valid strip and is left as one — chooseStrip
// returns the workspace itself in that case.
func (t *Tabbed) ensure(ws *sway.Node) error {
	if ws == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	name := ws.Name
	lastShape := ""
	for round := range maxEnsureRounds {
		// Work from a fresh tree — every repair below mutates it. Fall back to
		// the caller's node once (command-recording mocks have no tree), which
		// is enough to emit the first repair.
		target := t.freshWorkspace(name)
		if target == nil {
			if round > 0 {
				return nil
			}
			target = ws
		}
		// Stop if the previous round's repair did not actually move the tree.
		// Real sway can refuse a command (or a racing event can undo it), and
		// re-issuing the same repair eight times would turn one failure into a
		// command storm.
		if shape := shapeSig(target); shape == lastShape {
			t.log().Debug("ensure: repair made no progress, stopping",
				"workspace", name, "round", round)
			return nil
		} else {
			lastShape = shape
		}
		changed, err := t.repair(target)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
	}
	t.log().Warn("ensure: workspace did not converge to a flat tab strip",
		"workspace", name, "rounds", maxEnsureRounds)
	return nil
}

// repair performs at most ONE repair on the workspace and reports whether it
// changed anything. ensure re-reads the tree and calls it again until it
// reports clean, so each step reasons about a real tree rather than a
// predicted one.
//
// The repairs, in order:
//
//  1. gather — pull every window that is not a direct child of the strip
//     into it, so all tabs live in one container.
//  2. tab — give the strip the tabbed layout.
//  3. lift — collapse any wrapper chain between the strip and the workspace.
//  4. wrap — when the tabs are still workspace-direct children of a
//     non-tabbed workspace, wrap them into a strip container.
func (t *Tabbed) repair(ws *sway.Node) (bool, error) {
	leaves := ws.Leaves()
	if len(leaves) == 0 {
		return false, nil
	}
	strip := chooseStrip(ws, leaves)

	// 1. gather.
	if anchor, strays := straysOutside(strip, leaves); len(strays) > 0 {
		t.log().Debug("ensure: gathering windows into the tab strip",
			"workspace", ws.Name, "strip", strip.ID, "anchor", anchor.ID, "strays", len(strays))
		for _, stray := range strays {
			// Re-mark on the window just placed so strays keep their relative
			// order: `move window to mark` inserts as the mark's NEXT sibling.
			t.runCmd("[con_id=%d] mark --add %s", anchor.ID, tabGatherMark)
			t.runCmd("[con_id=%d] move window to mark %s", stray.ID, tabGatherMark)
			t.runCmd("[con_id=%d] unmark %s", anchor.ID, tabGatherMark)
			anchor = stray
		}
		return true, nil
	}

	// 2. tab. Scoped to a window inside the strip: cmd_layout re-layouts that
	// window's PARENT, which is the strip.
	if strip != ws && strip.Layout != "tabbed" {
		t.log().Info("ensure: tabbing the strip",
			"workspace", ws.Name, "strip", strip.ID, "was", strip.Layout)
		t.runCmd("[con_id=%d] layout tabbed", leaves[0].ID)
		return true, nil
	}

	// 3. lift. `split none` collapses the whole singleton chain above the
	// strip in one call (container_flatten loops); sway rejects it when the
	// strip's parent has siblings, so only issue it when it is a singleton —
	// gather will have emptied the siblings by then anyway.
	if strip != ws && strip.Parent != nil && strip.Parent != ws && len(strip.Parent.Nodes) == 1 {
		t.log().Debug("ensure: lifting the strip to workspace level",
			"workspace", ws.Name, "strip", strip.ID, "wrapper", strip.Parent.ID)
		t.runCmd("[con_id=%d] split none", strip.ID)
		return true, nil
	}

	// 4. wrap. The windows are workspace-direct and the workspace container is
	// not itself tabbed, so there is no strip yet: `layout tabbed` on a
	// workspace-direct window wraps them all into one (workspace_wrap_children).
	if strip == ws && ws.Layout != "tabbed" {
		t.log().Info("ensure: wrapping workspace windows into a tab strip",
			"workspace", ws.Name, "windows", len(leaves))
		t.runCmd("[con_id=%d] layout tabbed", leaves[0].ID)
		return true, nil
	}

	return false, nil
}

// chooseStrip picks the container that should hold the tabs:
//
//   - the workspace container itself when it is already tabbed (or stacked —
//     a stacked workspace is the same strip drawn vertically);
//   - otherwise the workspace-direct tabbed container holding the most
//     windows, so an existing strip wins over a stray wrapper;
//   - otherwise the first window's parent, which repair tabs and lifts. When
//     that parent IS the workspace, repair wraps instead.
//
// Never nil: leaves is non-empty and every leaf has a parent.
func chooseStrip(ws *sway.Node, leaves []*sway.Node) *sway.Node {
	if ws.Layout == "tabbed" || ws.Layout == "stacked" {
		return ws
	}
	var best *sway.Node
	bestCount := 0
	for _, child := range ws.Nodes {
		if child.Type != "con" || len(child.Nodes) == 0 {
			continue
		}
		if child.Layout != "tabbed" && child.Layout != "stacked" {
			continue
		}
		if n := len(child.Leaves()); n > bestCount {
			best, bestCount = child, n
		}
	}
	if best != nil {
		return best
	}
	if p := leaves[0].Parent; p != nil {
		return p
	}
	return ws
}

// straysOutside returns an anchor to gather onto — the strip's last direct
// window — and every window that is not a direct child of the strip, in tree
// order. The anchor is nil only when there are no strays.
func straysOutside(strip *sway.Node, leaves []*sway.Node) (*sway.Node, []*sway.Node) {
	var anchor *sway.Node
	var strays []*sway.Node
	for _, leaf := range leaves {
		if leaf.Parent == strip {
			anchor = leaf // keep the last one: strays append after it
			continue
		}
		strays = append(strays, leaf)
	}
	if len(strays) == 0 {
		return nil, nil
	}
	if anchor == nil {
		// Nothing sits directly in the strip yet (it is about to be created
		// from this window's parent), so gather onto the first window.
		anchor = strays[0]
		strays = strays[1:]
	}
	return anchor, strays
}

// shapeSig renders the workspace's tiling shape as a string, so ensure can
// tell "the repair landed" from "nothing moved". Ids and layouts only —
// geometry is irrelevant to the strip invariant and changes constantly.
func shapeSig(n *sway.Node) string {
	var b strings.Builder
	var walk func(node *sway.Node)
	walk = func(node *sway.Node) {
		fmt.Fprintf(&b, "%d/%s(", node.ID, node.Layout)
		for _, c := range node.Nodes {
			walk(c)
		}
		b.WriteByte(')')
	}
	walk(n)
	return b.String()
}

func (t *Tabbed) runCmd(format string, args ...any) {
	cmd := fmt.Sprintf(format, args...)
	if err := t.conn.RunCommand(cmd); err != nil {
		t.log().Warn("sway cmd failed", "cmd", cmd, "error", err)
	}
}

// freshWorkspace re-fetches the tree and returns the named workspace node,
// or nil. The returned tree has parents wired (sim maintains them;
// Conn.GetTree calls SetParents).
func (t *Tabbed) freshWorkspace(name string) *sway.Node {
	fresh, err := t.conn.GetTree()
	if err != nil || fresh == nil {
		return nil
	}
	for _, w := range fresh.Workspaces() {
		if w.Name == name {
			return w
		}
	}
	return nil
}

func wsName(ws *sway.Node) string {
	if ws == nil {
		return ""
	}
	return ws.Name
}

var _ Manager = (*Tabbed)(nil)
