package workspace

import (
	"fmt"
	"strings"

	"github.com/mschulkind-oss/tilekeeper/internal/ipc"
	"github.com/mschulkind-oss/tilekeeper/internal/layout"
)

// HandleIPC implements ipc.Handler by dispatching commands to workspace
// managers. The command string is the same grammar as nop bindings: a bare
// `status` query, `layout <name>` to switch the workspace layout, or an action
// verb dispatched to the current manager.
func (h *Hub) HandleIPC(req ipc.Request) ipc.Response {
	if req.Command == "status" {
		return h.handleStatus()
	}
	if name, ok := strings.CutPrefix(req.Command, "layout "); ok {
		return h.handleLayoutSet(req.Workspace, strings.TrimSpace(name))
	}
	return h.handleLayoutCommand(req.Command, req.Workspace)
}

func (h *Hub) handleStatus() ipc.Response {
	type wsStatus struct {
		Name      string  `json:"name"`
		Layout    string  `json:"layout"`
		WindowIDs []int64 `json:"window_ids"`
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	var statuses []wsStatus
	for name, mgr := range h.managers {
		statuses = append(statuses, wsStatus{
			Name:      name,
			Layout:    mgr.Name(),
			WindowIDs: mgr.WindowIDs(),
		})
	}

	return ipc.Response{OK: true, Data: statuses}
}

func (h *Hub) handleLayoutSet(wsName, layoutName string) ipc.Response {
	if wsName == "" {
		ws, err := h.findFocusedWorkspace()
		if err != nil {
			return ipc.Response{OK: false, Error: err.Error()}
		}
		wsName = ws
	}

	if layoutName == "none" {
		h.RemoveManager(wsName)
		if wsNode, err := h.getWorkspaceNode(wsName); err == nil {
			layout.FlattenSingletons(h.client, wsNode)
		}
		return ipc.Response{OK: true}
	}

	wsCfg := h.cfg.Workspaces[wsName]
	wsCfg.DefaultLayout = layoutName
	mgr := h.createManager(wsName, wsCfg)
	if mgr == nil {
		return ipc.Response{OK: false, Error: fmt.Sprintf("unknown layout: %s", layoutName)}
	}

	h.SetManager(wsName, mgr)

	// Flatten wrappers left behind by the outgoing manager before the new
	// one runs ArrangeAll. Otherwise MasterStack's splitv/stacked wrappers
	// can persist into a tabbed layout as orphan singleton chains —
	// caught by the fuzzer's no-wrapper-chain invariant.
	wsNode, err := h.getWorkspaceNode(wsName)
	if err == nil {
		layout.FlattenSingletons(h.client, wsNode)
		// Re-fetch: FlattenSingletons mutated the tree.
		if refreshed, rerr := h.getWorkspaceNode(wsName); rerr == nil {
			wsNode = refreshed
		}
		mgr.ArrangeAll(wsNode)
	}

	return ipc.Response{OK: true}
}

func (h *Hub) handleLayoutCommand(cmd, wsName string) ipc.Response {
	if wsName == "" {
		ws, err := h.findFocusedWorkspace()
		if err != nil {
			return ipc.Response{OK: false, Error: err.Error()}
		}
		wsName = ws
	}

	mgr := h.Manager(wsName)
	if mgr == nil {
		// Parity with the nop binding path (handleBindingEvent): simple
		// directional commands have a native sway equivalent, so they still
		// work on an unmanaged workspace via msg/IPC too.
		if native, ok := nativeSwayFallback(cmd); ok {
			if err := h.client.RunCommand(native); err != nil {
				return ipc.Response{OK: false, Error: err.Error()}
			}
			return ipc.Response{OK: true}
		}
		return ipc.Response{OK: false, Error: fmt.Sprintf("no layout manager for workspace %s", wsName)}
	}

	wsNode, err := h.getWorkspaceNode(wsName)
	if err != nil {
		return ipc.Response{OK: false, Error: err.Error()}
	}

	if err := mgr.Command(cmd, wsNode); err != nil {
		return ipc.Response{OK: false, Error: err.Error()}
	}

	return ipc.Response{OK: true}
}

// Compile-time check that Hub implements ipc.Handler.
var _ ipc.Handler = (*Hub)(nil)
