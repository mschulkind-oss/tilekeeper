package workspace

import (
	"slices"
	"testing"

	"github.com/mschulkind-oss/tilekeeper/internal/config"
	"github.com/mschulkind-oss/tilekeeper/internal/ipc"
	"github.com/mschulkind-oss/tilekeeper/internal/sway"
)

// TestHandleIPCDirectionalFallsThroughOnUnmanagedWorkspace is the IPC/CLI twin
// of TestNopDirectionalFallsThroughOnUnmanagedWorkspace: a directional command
// on a workspace with no manager must fall through to native sway and report
// OK — so `nop tilekeeper move left` and `tilekeeper msg move left` behave
// identically (the transport-parity contract).
func TestHandleIPCDirectionalFallsThroughOnUnmanagedWorkspace(t *testing.T) {
	hub, mock := newTestHub(config.DefaultConfig())
	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("B", 2)
	ws.Parent = &sway.Node{Type: "output", Nodes: []*sway.Node{ws}}
	mock.Tree = ws.Parent
	mock.WorkspaceList = []sway.Workspace{{Name: "B", Focused: true}}

	resp := hub.HandleIPC(ipc.Request{Command: "move left", Workspace: "B"})
	if !resp.OK {
		t.Fatalf("directional command on unmanaged workspace should fall through to sway (OK): %v", resp.Error)
	}
	if !slices.Contains(mock.Commands, "move left") {
		t.Errorf("expected 'move left' passthrough to sway via IPC; got commands=%v", mock.Commands)
	}
}

func TestHandleIPCStatus(t *testing.T) {
	hub, _ := newTestHub(config.DefaultConfig())
	mgr := newMockManager("MasterStack")
	mgr.windowIDs = []int64{100, 101}
	hub.SetManager("1", mgr)

	resp := hub.HandleIPC(ipc.Request{Command: "status"})
	if !resp.OK {
		t.Error("status should return OK")
	}
	if resp.Data == nil {
		t.Error("status should return data")
	}
}

func TestHandleIPCLayoutSet(t *testing.T) {
	cfg := config.Config{
		General: config.GeneralConfig{MasterWidth: 50},
		Workspaces: map[string]config.WorkspaceConfig{
			"1": {},
		},
	}
	hub, mock := newTestHub(cfg)

	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 2)
	ws.Parent = &sway.Node{Type: "output", Nodes: []*sway.Node{ws}}
	mock.Tree = ws.Parent
	mock.WorkspaceList = []sway.Workspace{
		{Num: 1, Name: "1", Focused: true},
	}

	resp := hub.HandleIPC(ipc.Request{Command: "layout MasterStack"})
	if !resp.OK {
		t.Errorf("layout-set should return OK: %v", resp.Error)
	}

	mgr := hub.Manager("1")
	if mgr == nil || mgr.Name() != "MasterStack" {
		t.Error("manager should be set after layout-set")
	}
}

func TestHandleIPCLayoutSetNone(t *testing.T) {
	hub, mock := newTestHub(config.DefaultConfig())
	mock.WorkspaceList = []sway.Workspace{
		{Num: 1, Name: "1", Focused: true},
	}
	hub.SetManager("1", newMockManager("MasterStack"))

	resp := hub.HandleIPC(ipc.Request{Command: "layout none"})
	if !resp.OK {
		t.Error("layout-set none should return OK")
	}

	if mgr := hub.Manager("1"); mgr != nil {
		t.Error("manager should be removed after layout-set none")
	}
}

func TestHandleIPCLayoutSetUnknown(t *testing.T) {
	hub, mock := newTestHub(config.DefaultConfig())
	mock.WorkspaceList = []sway.Workspace{
		{Num: 1, Name: "1", Focused: true},
	}

	resp := hub.HandleIPC(ipc.Request{Command: "layout bogus"})
	if resp.OK {
		t.Error("layout-set unknown should return error")
	}
}

func TestHandleIPCCommand(t *testing.T) {
	hub, mock := newTestHub(config.DefaultConfig())

	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 2)
	ws.Parent = &sway.Node{Type: "output", Nodes: []*sway.Node{ws}}
	mock.Tree = ws.Parent
	mock.WorkspaceList = []sway.Workspace{
		{Num: 1, Name: "1", Focused: true},
	}

	mgr := newMockManager("MasterStack")
	hub.SetManager("1", mgr)

	resp := hub.HandleIPC(ipc.Request{Command: "swap-master"})
	if !resp.OK {
		t.Errorf("swap-master should return OK: %v", resp.Error)
	}
	if mgr.lastCommand != "swap-master" {
		t.Errorf("command = %q, want swap-master", mgr.lastCommand)
	}
}

func TestHandleIPCCommandNoManager(t *testing.T) {
	hub, mock := newTestHub(config.DefaultConfig())
	mock.WorkspaceList = []sway.Workspace{
		{Num: 1, Name: "1", Focused: true},
	}

	resp := hub.HandleIPC(ipc.Request{Command: "swap-master"})
	if resp.OK {
		t.Error("command with no manager should return error")
	}
}

func TestHandleIPCCommandNoFocused(t *testing.T) {
	hub, mock := newTestHub(config.DefaultConfig())
	mock.WorkspaceList = []sway.Workspace{}

	resp := hub.HandleIPC(ipc.Request{Command: "swap-master"})
	if resp.OK {
		t.Error("command with no focused workspace should return error")
	}
}
