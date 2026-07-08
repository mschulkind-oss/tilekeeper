package layout

import (
	"fmt"
	"log/slog"
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

func (t *Tabbed) WindowRemoved(_ *sway.Node, win *sway.Node) error {
	if win != nil {
		t.log().Debug("window removed", "con_id", win.ID, "name", win.Name)
	}
	return nil
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
func (t *Tabbed) Command(cmd string, ws *sway.Node) error {
	switch cmd {
	case "focus up", "focus down", "focus left", "focus right",
		"move up", "move down", "move left", "move right":
		if ws == nil || len(ws.Leaves()) == 0 {
			t.log().Debug("skipping nav on empty workspace", "command", cmd)
			return nil
		}
		t.log().Debug("forwarding nav command to sway", "command", cmd)
		return t.conn.RunCommand(cmd)
	default:
		t.log().Debug("command ignored for tabbed layout", "command", cmd)
		return nil
	}
}

// tabFlattenMark is the scratch mark used to lift nested leaves to the
// workspace level. Scoped to one ensure() call (added then removed).
const tabFlattenMark = "tk_tab_flatten"

// ensure makes the workspace tabbed AND flat: every leaf a direct tab
// child. `layout tabbed` alone does not collapse nested containers, so a
// container move that dumps a MasterStack subtree (splitv/stacked
// wrappers) onto a tabbed workspace leaves the windows nested inside the
// tabs — the no-wrapper-chain invariant fires and the user sees nested
// tabs instead of a flat tab strip. The old ensure() also early-returned
// once the workspace was already tabbed, so the nesting was never
// repaired on subsequent move-ins.
func (t *Tabbed) ensure(ws *sway.Node) error {
	if ws == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	// Set the workspace layout from the caller's node — no tree round-trip,
	// so this works with the command-recording mock and matches the prior
	// behavior exactly for the common "freshly-tabbed" case.
	if ws.Layout != "tabbed" {
		t.log().Info("ensure: setting workspace layout to tabbed",
			"workspace", ws.Name, "was", ws.Layout)
		if err := t.conn.RunCommand(fmt.Sprintf("[workspace=%s] layout tabbed", ws.Name)); err != nil {
			return err
		}
	}
	// Flatten any nested wrappers to flat tabs. Runs even when already
	// tabbed (the old early-return was the bug) — a no-op on a tree that's
	// already flat, and silently skipped when no tree is available (mock).
	return t.flattenToTabs(ws.Name)
}

// flattenToTabs lifts every nested leaf to the workspace level so the
// tabbed workspace is a flat tab strip.
//
// Lift strategy: mark a workspace-direct leaf and `move window to mark`
// every nested leaf onto it — sway re-parents the moved leaf as a sibling
// of the mark target, i.e. a workspace-direct tab. Emptied wrappers are
// reaped by sway. Re-fetch each round since the tree mutates; bounded so a
// pathological tree can't loop forever.
func (t *Tabbed) flattenToTabs(wsName string) error {
	for round := 0; round < 8; round++ {
		ws := t.freshWorkspace(wsName)
		if ws == nil {
			return nil
		}
		var anchor *sway.Node
		var nested []*sway.Node
		for _, leaf := range ws.Leaves() {
			if leaf.Parent == ws {
				if anchor == nil {
					anchor = leaf
				}
			} else {
				nested = append(nested, leaf)
			}
		}
		if len(nested) == 0 {
			return nil // already flat
		}
		if anchor == nil {
			// No workspace-direct leaf to anchor against. Collapse singleton
			// wrappers to expose one and retry; if that makes no progress
			// (rare all-nested multi-child shape, which the no-wrapper-chain
			// invariant does not flag anyway) bail rather than spin.
			before := len(ws.Nodes)
			FlattenSingletons(t.conn, ws)
			if after := t.freshWorkspace(wsName); after == nil || len(after.Nodes) == before {
				t.log().Debug("flatten: no anchor and no singleton progress",
					"workspace", wsName, "nested", len(nested))
				return nil
			}
			continue
		}
		t.log().Debug("flatten: lifting nested leaves to tabs",
			"workspace", wsName, "anchor", anchor.ID, "nested", len(nested))
		t.conn.RunCommand(fmt.Sprintf("[con_id=%d] mark --add %s", anchor.ID, tabFlattenMark))
		for _, leaf := range nested {
			t.conn.RunCommand(fmt.Sprintf("[con_id=%d] move window to mark %s", leaf.ID, tabFlattenMark))
		}
		t.conn.RunCommand(fmt.Sprintf("[con_id=%d] unmark %s", anchor.ID, tabFlattenMark))
	}
	return nil
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
