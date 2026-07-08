package workspace

import (
	"log/slog"
	"slices"
	"testing"

	"github.com/mschulkind-oss/tilekeeper/internal/config"
	"github.com/mschulkind-oss/tilekeeper/internal/sway"
)

func newTestHub(cfg config.Config) (*Hub, *sway.Mock) {
	mock := sway.NewMock()
	logger := slog.Default()
	hub := NewHub(mock, cfg, logger)
	return hub, mock
}

func userConfig() config.Config {
	return config.Config{
		General: config.GeneralConfig{
			DefaultLayout:     "none",
			MasterWidth:       75,
			VisibleStackLimit: 3,
			Debug:             true,
		},
		Workspaces: map[string]config.WorkspaceConfig{
			"4": {DefaultLayout: "MasterStack", StackSide: "left"},
			"6": {DefaultLayout: "MasterStack"},
			"7": {DefaultLayout: "MasterStack"},
			"8": {DefaultLayout: "tabbed"},
			"9": {DefaultLayout: "MasterStack", StackSide: "left"},
		},
	}
}

func TestNewHub(t *testing.T) {
	hub, _ := newTestHub(config.DefaultConfig())
	if hub == nil {
		t.Fatal("NewHub returned nil")
	}
	if len(hub.WorkspaceNames()) != 0 {
		t.Error("new hub should have no workspaces")
	}
}

func TestNewHubNilLogger(t *testing.T) {
	mock := sway.NewMock()
	hub := NewHub(mock, config.DefaultConfig(), nil)
	if hub == nil {
		t.Fatal("NewHub with nil logger returned nil")
	}
}

func TestInitialize(t *testing.T) {
	cfg := userConfig()
	hub, _ := newTestHub(cfg)
	hub.Initialize()

	names := hub.WorkspaceNames()
	// Workspaces 4,6,7,9 should have MasterStack managers
	// Workspace 8 (tabbed) is not yet implemented → nil
	masterStackCount := 0
	for _, name := range names {
		mgr := hub.Manager(name)
		if mgr != nil && mgr.Name() == "MasterStack" {
			masterStackCount++
		}
	}
	if masterStackCount != 4 {
		t.Errorf("expected 4 MasterStack managers, got %d (names: %v)", masterStackCount, names)
	}
}

func TestInitializeNoneLayout(t *testing.T) {
	cfg := config.Config{
		General: config.GeneralConfig{DefaultLayout: "none"},
		Workspaces: map[string]config.WorkspaceConfig{
			"1": {DefaultLayout: "none"},
		},
	}
	hub, _ := newTestHub(cfg)
	hub.Initialize()

	if mgr := hub.Manager("1"); mgr != nil {
		t.Error("'none' layout should not create a manager")
	}
}

func TestInitializeUnknownLayout(t *testing.T) {
	cfg := config.Config{
		General: config.GeneralConfig{},
		Workspaces: map[string]config.WorkspaceConfig{
			"1": {DefaultLayout: "unknown"},
		},
	}
	hub, _ := newTestHub(cfg)
	hub.Initialize()

	if mgr := hub.Manager("1"); mgr != nil {
		t.Error("unknown layout should not create a manager")
	}
}

func TestSetAndRemoveManager(t *testing.T) {
	hub, mock := newTestHub(config.DefaultConfig())

	ms := newMockManager("TestLayout")
	_ = mock // satisfy import
	hub.SetManager("test-ws", ms)

	if mgr := hub.Manager("test-ws"); mgr == nil || mgr.Name() != "TestLayout" {
		t.Error("SetManager/Manager failed")
	}

	hub.RemoveManager("test-ws")
	if mgr := hub.Manager("test-ws"); mgr != nil {
		t.Error("RemoveManager failed")
	}
}

func TestHandleWindowNewEvent(t *testing.T) {
	hub, mock := newTestHub(config.DefaultConfig())

	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 0)
	ws.Parent = &sway.Node{Type: "output", Nodes: []*sway.Node{ws}}
	mock.Tree = ws.Parent

	mgr := newMockManager("MasterStack")
	hub.SetManager("1", mgr)

	window := sway.CreateWindow("test-app")
	window.Parent = ws
	ws.Nodes = append(ws.Nodes, window)

	hub.HandleEvent(sway.Event{
		Type:      "window",
		Change:    "new",
		Container: window,
	})

	if mgr.addedCount != 1 {
		t.Errorf("WindowAdded called %d times, want 1", mgr.addedCount)
	}
}

func TestHandleWindowCloseEvent(t *testing.T) {
	hub, mock := newTestHub(config.DefaultConfig())

	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 1)
	ws.Parent = &sway.Node{Type: "output", Nodes: []*sway.Node{ws}}
	mock.Tree = ws.Parent

	mgr := newMockManager("MasterStack")
	hub.SetManager("1", mgr)

	window := ws.Nodes[0]

	hub.HandleEvent(sway.Event{
		Type:      "window",
		Change:    "close",
		Container: window,
	})

	if mgr.removedCount != 1 {
		t.Errorf("WindowRemoved called %d times, want 1", mgr.removedCount)
	}
}

func TestHandleWindowFocusEvent(t *testing.T) {
	hub, mock := newTestHub(config.DefaultConfig())

	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 1)
	ws.Parent = &sway.Node{Type: "output", Nodes: []*sway.Node{ws}}
	mock.Tree = ws.Parent

	mgr := newMockManager("MasterStack")
	hub.SetManager("1", mgr)

	window := ws.Nodes[0]

	hub.HandleEvent(sway.Event{
		Type:      "window",
		Change:    "focus",
		Container: window,
	})

	if mgr.focusedCount != 1 {
		t.Errorf("WindowFocused called %d times, want 1", mgr.focusedCount)
	}
}

func TestHandleWindowEventNilContainer(t *testing.T) {
	hub, _ := newTestHub(config.DefaultConfig())

	// Should not panic
	hub.HandleEvent(sway.Event{
		Type:   "window",
		Change: "new",
	})
}

func TestHandleWindowEventNoManager(t *testing.T) {
	hub, mock := newTestHub(config.DefaultConfig())

	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 1)
	ws.Parent = &sway.Node{Type: "output", Nodes: []*sway.Node{ws}}
	mock.Tree = ws.Parent

	// No manager set for workspace "1"
	window := ws.Nodes[0]

	// Should not panic
	hub.HandleEvent(sway.Event{
		Type:      "window",
		Change:    "new",
		Container: window,
	})
}

func TestHandleWorkspaceInitEvent(t *testing.T) {
	cfg := userConfig()
	hub, _ := newTestHub(cfg)

	// Workspace "6" is configured but not yet initialized
	if mgr := hub.Manager("6"); mgr != nil {
		t.Error("workspace 6 should not be initialized yet")
	}

	hub.HandleEvent(sway.Event{
		Type:      "workspace",
		Change:    "init",
		Workspace: &sway.Node{Type: "workspace", Name: "6"},
	})

	if mgr := hub.Manager("6"); mgr == nil {
		t.Error("workspace 6 should be lazily initialized")
	}
}

func TestHandleWorkspaceInitAlreadyExists(t *testing.T) {
	cfg := userConfig()
	hub, _ := newTestHub(cfg)
	hub.Initialize()

	mgr := hub.Manager("6")
	if mgr == nil {
		t.Fatal("workspace 6 should exist after Initialize")
	}

	// Sending init again should not replace the manager
	hub.HandleEvent(sway.Event{
		Type:      "workspace",
		Change:    "init",
		Workspace: &sway.Node{Type: "workspace", Name: "6"},
	})

	if hub.Manager("6") != mgr {
		t.Error("init should not replace existing manager")
	}
}

func TestHandleWorkspaceInitUnconfigured(t *testing.T) {
	cfg := userConfig()
	hub, _ := newTestHub(cfg)

	// Workspace "99" is not in config
	hub.HandleEvent(sway.Event{
		Type:      "workspace",
		Change:    "init",
		Workspace: &sway.Node{Type: "workspace", Name: "99"},
	})

	if mgr := hub.Manager("99"); mgr != nil {
		t.Error("unconfigured workspace should not get a manager")
	}
}

func TestHandleWorkspaceInitNilWorkspace(t *testing.T) {
	hub, _ := newTestHub(userConfig())

	// Should not panic
	hub.HandleEvent(sway.Event{
		Type:   "workspace",
		Change: "init",
	})
}

func TestHandleBindingEvent(t *testing.T) {
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

	hub.HandleEvent(sway.Event{
		Type:    "binding",
		Binding: &sway.Binding{Command: "nop tilekeeper swap-master"},
	})

	if mgr.lastCommand != "swap-master" {
		t.Errorf("Command = %q, want %q", mgr.lastCommand, "swap-master")
	}
}

func TestHandleBindingEventWithWorkspace(t *testing.T) {
	hub, mock := newTestHub(config.DefaultConfig())

	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("4", 2)
	ws.Parent = &sway.Node{Type: "output", Nodes: []*sway.Node{ws}}
	mock.Tree = ws.Parent

	mgr := newMockManager("MasterStack")
	hub.SetManager("4", mgr)

	hub.HandleEvent(sway.Event{
		Type:    "binding",
		Binding: &sway.Binding{Command: "nop tilekeeper rotate cw workspace 4"},
	})

	if mgr.lastCommand != "rotate cw" {
		t.Errorf("Command = %q, want %q", mgr.lastCommand, "rotate cw")
	}
}

// TestNopDirectionalFallsThroughOnUnmanagedWorkspace covers the user's live
// bug: shift-mod-y/o ($mod+Shift+y = `nop tilekeeper move left`) did
// nothing on unmanaged workspaces like "B". Those simple directional commands
// should pass through to native sway when no layout manager is configured for
// the focused workspace.
func TestNopDirectionalFallsThroughOnUnmanagedWorkspace(t *testing.T) {
	hub, mock := newTestHub(config.DefaultConfig())

	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("B", 2)
	ws.Parent = &sway.Node{Type: "output", Nodes: []*sway.Node{ws}}
	mock.Tree = ws.Parent
	mock.WorkspaceList = []sway.Workspace{
		{Name: "B", Focused: true},
	}

	hub.HandleEvent(sway.Event{
		Type:    "binding",
		Binding: &sway.Binding{Command: "nop tilekeeper move left"},
	})

	if !slices.Contains(mock.Commands, "move left") {
		t.Errorf("expected 'move left' passthrough to sway for non-tilekeeper workspace; got commands=%v", mock.Commands)
	}
}

func TestHandleBindingNotNop(t *testing.T) {
	hub, _ := newTestHub(config.DefaultConfig())

	// Non-nop binding should be ignored
	hub.HandleEvent(sway.Event{
		Type:    "binding",
		Binding: &sway.Binding{Command: "exec firefox"},
	})
}

func TestHandleBindingNilBinding(t *testing.T) {
	hub, _ := newTestHub(config.DefaultConfig())

	// Should not panic
	hub.HandleEvent(sway.Event{
		Type: "binding",
	})
}

func TestHandleFloatingOn(t *testing.T) {
	hub, mock := newTestHub(config.DefaultConfig())

	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 1)
	ws.Parent = &sway.Node{Type: "output", Nodes: []*sway.Node{ws}}
	mock.Tree = ws.Parent

	mgr := newMockManager("MasterStack")
	hub.SetManager("1", mgr)

	window := ws.Nodes[0]
	window.Floating = "user_on"

	hub.HandleEvent(sway.Event{
		Type:      "window",
		Change:    "floating",
		Container: window,
	})

	if mgr.removedCount != 1 {
		t.Errorf("floating on should remove window; removedCount = %d", mgr.removedCount)
	}
}

func TestHandleFloatingOff(t *testing.T) {
	hub, mock := newTestHub(config.DefaultConfig())

	sway.ResetIDCounter()
	ws := sway.CreateWorkspace("1", 0)
	ws.Parent = &sway.Node{Type: "output", Nodes: []*sway.Node{ws}}
	mock.Tree = ws.Parent

	mgr := newMockManager("MasterStack")
	hub.SetManager("1", mgr)

	window := sway.CreateWindow("test")
	window.Floating = "user_off"
	window.Parent = ws

	hub.HandleEvent(sway.Event{
		Type:      "window",
		Change:    "floating",
		Container: window,
	})

	if mgr.addedCount != 1 {
		t.Errorf("floating off should add window; addedCount = %d", mgr.addedCount)
	}
}

func TestCreateMasterStackInheritsGlobals(t *testing.T) {
	cfg := config.Config{
		General: config.GeneralConfig{
			MasterWidth:       75,
			VisibleStackLimit: 3,
			StackLayout:       "splitv",
			StackSide:         "right",
		},
		Workspaces: map[string]config.WorkspaceConfig{
			"6": {DefaultLayout: "MasterStack"},
		},
	}
	hub, _ := newTestHub(cfg)
	hub.Initialize()

	mgr := hub.Manager("6")
	if mgr == nil {
		t.Fatal("expected manager for workspace 6")
	}
	if mgr.Name() != "MasterStack" {
		t.Errorf("Name = %q, want MasterStack", mgr.Name())
	}
}

func TestCreateMasterStackOverridesGlobals(t *testing.T) {
	cfg := config.Config{
		General: config.GeneralConfig{
			MasterWidth: 50,
			StackSide:   "right",
		},
		Workspaces: map[string]config.WorkspaceConfig{
			"4": {DefaultLayout: "MasterStack", StackSide: "left", MasterWidth: 75},
		},
	}
	hub, _ := newTestHub(cfg)
	hub.Initialize()

	mgr := hub.Manager("4")
	if mgr == nil {
		t.Fatal("expected manager for workspace 4")
	}
}

func TestHandleWindowMoveEvent(t *testing.T) {
	hub, mock := newTestHub(config.DefaultConfig())

	sway.ResetIDCounter()
	tree := sway.CreateTree(
		sway.WorkspaceSpec{Name: "1", WindowCount: 2},
		sway.WorkspaceSpec{Name: "2", WindowCount: 0},
	)
	ws1 := tree.Workspaces()[0]
	ws2 := tree.Workspaces()[1]
	mock.Tree = tree

	mgr1 := newMockManager("MasterStack")
	mgr2 := newMockManager("MasterStack")
	hub.SetManager("1", mgr1)
	hub.SetManager("2", mgr2)

	// Prime Hub tracking with a new-event for the window on ws1.
	window := ws1.Nodes[0]
	hub.HandleEvent(sway.Event{
		Type:      "window",
		Change:    "new",
		Container: &sway.Node{ID: window.ID, Type: "con"},
	})

	// After move, the tree shows the window on ws2.
	ws1.Nodes = ws1.Nodes[1:]
	ws2.Nodes = append(ws2.Nodes, window)
	window.Parent = ws2

	hub.HandleEvent(sway.Event{
		Type:      "window",
		Change:    "move",
		Container: &sway.Node{ID: window.ID, Type: "con"},
	})

	if mgr1.removedCount != 1 {
		t.Errorf("source workspace removedCount = %d, want 1", mgr1.removedCount)
	}
	if mgr2.addedCount != 1 {
		t.Errorf("dest workspace addedCount = %d, want 1", mgr2.addedCount)
	}
}

// TestHandleWindowMoveEventRealisticNilParent reproduces the scenario the
// fuzzer found at seed=36 step=7 (Bug J): a window moved cross-workspace by
// sway produces a window::move event whose Container has a nil Parent (that
// is what parseWindowEvent gives us — the JSON has no parent link). By the
// time HandleEvent runs, the tree already shows the window on the destination
// workspace, so any tree-only lookup of the container returns the NEW
// workspace. The Hub must remember where the container USED to live so it
// can drive WindowRemoved on the source manager.
//
// Historical bug: findWorkspaceForContainer(nilParent) walked the tree and
// returned the destination workspace; handleWindowMove then compared
// newWSName == wsName and bailed out, leaving the source manager's tracking
// permanently stale.
func TestHandleWindowMoveEventRealisticNilParent(t *testing.T) {
	hub, mock := newTestHub(config.DefaultConfig())

	sway.ResetIDCounter()
	tree := sway.CreateTree(
		sway.WorkspaceSpec{Name: "7", WindowCount: 1},
		sway.WorkspaceSpec{Name: "8", WindowCount: 0},
	)
	ws7 := tree.Workspaces()[0]
	ws8 := tree.Workspaces()[1]
	mock.Tree = tree

	mgr7 := newMockManager("MasterStack")
	mgr8 := newMockManager("MasterStack")
	hub.SetManager("7", mgr7)
	hub.SetManager("8", mgr8)

	// Simulate ws7 having already received WindowAdded — that's how the Hub
	// learns the window lives on ws7 in the first place. Real daemon: the
	// new-event is dispatched before any move.
	window := ws7.Nodes[0]
	hub.HandleEvent(sway.Event{
		Type:      "window",
		Change:    "new",
		Container: &sway.Node{ID: window.ID, Type: "con"},
	})
	if mgr7.addedCount != 1 {
		t.Fatalf("precondition: mgr7.addedCount = %d, want 1", mgr7.addedCount)
	}

	// Relocate in the tree: window is now on ws8.
	ws7.Nodes = ws7.Nodes[1:]
	ws8.Nodes = append(ws8.Nodes, window)
	window.Parent = ws8

	// The realistic move event: parseWindowEvent gives Container.Parent = nil.
	hub.HandleEvent(sway.Event{
		Type:      "window",
		Change:    "move",
		Container: &sway.Node{ID: window.ID, Type: "con"},
	})

	if mgr7.removedCount != 1 {
		t.Errorf("source workspace removedCount = %d, want 1 (Hub failed to detect cross-workspace move with nil-parent event container)", mgr7.removedCount)
	}
	if mgr8.addedCount != 1 {
		t.Errorf("dest workspace addedCount = %d, want 1", mgr8.addedCount)
	}
}

// TestSeedTrackingPreExistingWindowCrossMove reproduces a live bug
// observed in the journal: Chromium windows on ws7 appeared to "jump" to
// ws6 while the user was issuing rotate/swap commands on ws7. Root cause:
// wsForCon only gets populated on window::new events, so windows that
// exist at daemon startup (before any new event fires for them) have no
// entry in wsForCon. When one of those pre-existing windows later moves
// cross-workspace, handleWindowMove sees hadTracked=false and skips the
// WindowRemoved call on the source manager — leaving a stale id in the
// source manager's windowIDs. The next swap/move command on that stale
// id targets the window on its NEW workspace, yanking a ws6 window onto
// ws7 (or vice versa).
//
// Fix: Hub.SeedTracking walks the initial tree and populates wsForCon for
// every leaf, so handleWindowMove takes the hadTracked path even for
// windows that existed before daemon start.
func TestSeedTrackingPreExistingWindowCrossMove(t *testing.T) {
	hub, mock := newTestHub(config.DefaultConfig())

	sway.ResetIDCounter()
	tree := sway.CreateTree(
		sway.WorkspaceSpec{Name: "7", WindowCount: 1},
		sway.WorkspaceSpec{Name: "8", WindowCount: 0},
	)
	ws7 := tree.Workspaces()[0]
	ws8 := tree.Workspaces()[1]
	mock.Tree = tree

	mgr7 := newMockManager("MasterStack")
	mgr8 := newMockManager("MasterStack")
	hub.SetManager("7", mgr7)
	hub.SetManager("8", mgr8)

	// Pre-existing window at startup — NO window::new event is fired for
	// it. This is the scenario where tilekeeper starts up with
	// existing windows (daemon restart, fresh session, etc.).
	window := ws7.Nodes[0]

	// Daemon would have called arrangeExisting, which calls SeedTracking.
	hub.SeedTracking(tree)

	if got := hub.WorkspaceForContainer(window.ID); got != "7" {
		t.Fatalf("SeedTracking: wsForCon[%d] = %q, want \"7\"", window.ID, got)
	}

	// User moves the pre-existing window from ws7 to ws8.
	ws7.Nodes = ws7.Nodes[1:]
	ws8.Nodes = append(ws8.Nodes, window)
	window.Parent = ws8

	hub.HandleEvent(sway.Event{
		Type:      "window",
		Change:    "move",
		Container: &sway.Node{ID: window.ID, Type: "con"},
	})

	if mgr7.removedCount != 1 {
		t.Errorf("source mgr7.removedCount = %d, want 1 — without SeedTracking, Hub doesn't know the pre-existing window lived on ws7, so the source manager keeps a stale id",
			mgr7.removedCount)
	}
	if mgr8.addedCount != 1 {
		t.Errorf("dest mgr8.addedCount = %d, want 1", mgr8.addedCount)
	}
	if got := hub.WorkspaceForContainer(window.ID); got != "8" {
		t.Errorf("post-move wsForCon[%d] = %q, want \"8\"", window.ID, got)
	}
}

// TestHandleWindowMoveEventToNowhere covers the case where a tracked window
// leaves the tree entirely (e.g. moved to scratchpad). The Hub must drive
// WindowRemoved on the source manager and forget the container.
func TestHandleWindowMoveEventToNowhere(t *testing.T) {
	hub, mock := newTestHub(config.DefaultConfig())

	sway.ResetIDCounter()
	tree := sway.CreateTree(sway.WorkspaceSpec{Name: "1", WindowCount: 1})
	ws := tree.Workspaces()[0]
	mock.Tree = tree

	mgr := newMockManager("MasterStack")
	hub.SetManager("1", mgr)

	window := ws.Nodes[0]
	hub.HandleEvent(sway.Event{
		Type:      "window",
		Change:    "new",
		Container: &sway.Node{ID: window.ID, Type: "con"},
	})

	// Detach window from the tree entirely.
	ws.Nodes = nil

	hub.HandleEvent(sway.Event{
		Type:      "window",
		Change:    "move",
		Container: &sway.Node{ID: window.ID, Type: "con"},
	})

	if mgr.removedCount != 1 {
		t.Errorf("removedCount = %d, want 1", mgr.removedCount)
	}
}

func TestFindWorkspaceViaTree(t *testing.T) {
	hub, mock := newTestHub(config.DefaultConfig())

	sway.ResetIDCounter()
	tree := sway.CreateTree(
		sway.WorkspaceSpec{Name: "5", WindowCount: 1},
	)
	mock.Tree = tree

	ws := tree.Workspaces()[0]
	window := ws.Nodes[0]
	// Clear parent to force tree lookup
	window.Parent = nil

	found := hub.findWorkspaceForContainer(window)
	if found == nil || found.Name != "5" {
		t.Errorf("findWorkspaceForContainer = %v, want workspace '5'", found)
	}
}

func TestFindWorkspaceNotInTree(t *testing.T) {
	hub, mock := newTestHub(config.DefaultConfig())

	sway.ResetIDCounter()
	tree := sway.CreateTree(
		sway.WorkspaceSpec{Name: "1", WindowCount: 1},
	)
	mock.Tree = tree

	orphan := &sway.Node{ID: 99999, Type: "con"}
	found := hub.findWorkspaceForContainer(orphan)
	if found != nil {
		t.Error("expected nil for orphan container")
	}
}

func TestHandleBindingNoFocusedWorkspace(t *testing.T) {
	hub, mock := newTestHub(config.DefaultConfig())

	// WorkspaceList with no focused workspace
	mock.WorkspaceList = []sway.Workspace{
		{Num: 1, Name: "1", Focused: false},
	}

	mgr := newMockManager("MasterStack")
	hub.SetManager("1", mgr)

	hub.HandleEvent(sway.Event{
		Type:    "binding",
		Binding: &sway.Binding{Command: "nop tilekeeper swap-master"},
	})

	// Should have failed to find focused workspace; command not sent
	if mgr.lastCommand != "" {
		t.Errorf("should not have dispatched command, got %q", mgr.lastCommand)
	}
}

func TestHandleBindingNoManagerForWorkspace(t *testing.T) {
	hub, mock := newTestHub(config.DefaultConfig())

	mock.WorkspaceList = []sway.Workspace{
		{Num: 1, Name: "1", Focused: true},
	}

	// No manager set for workspace "1"
	hub.HandleEvent(sway.Event{
		Type:    "binding",
		Binding: &sway.Binding{Command: "nop tilekeeper swap-master"},
	})
	// Should gracefully do nothing
}

func TestHandleBindingWorkspaceNotInTree(t *testing.T) {
	hub, mock := newTestHub(config.DefaultConfig())

	sway.ResetIDCounter()
	tree := sway.CreateTree(
		sway.WorkspaceSpec{Name: "1", WindowCount: 1},
	)
	mock.Tree = tree

	mgr := newMockManager("MasterStack")
	hub.SetManager("99", mgr)

	hub.HandleEvent(sway.Event{
		Type:    "binding",
		Binding: &sway.Binding{Command: "nop tilekeeper swap-master workspace 99"},
	})

	// Workspace "99" not in tree → command should fail gracefully
	if mgr.lastCommand != "" {
		t.Error("should not dispatch when workspace not in tree")
	}
}

// --- Mock Manager for testing ---

type mockManager struct {
	name         string
	addedCount   int
	removedCount int
	focusedCount int
	lastCommand  string
	windowIDs    []int64
}

func newMockManager(name string) *mockManager {
	return &mockManager{name: name}
}

func (m *mockManager) Name() string { return m.name }

func (m *mockManager) WindowAdded(_ *sway.Node, window *sway.Node) error {
	m.addedCount++
	m.windowIDs = append(m.windowIDs, window.ID)
	return nil
}

func (m *mockManager) WindowRemoved(_ *sway.Node, _ *sway.Node) error {
	m.removedCount++
	return nil
}

func (m *mockManager) WindowFocused(_ *sway.Node, _ *sway.Node) error {
	m.focusedCount++
	return nil
}

func (m *mockManager) Command(cmd string, _ *sway.Node) error {
	m.lastCommand = cmd
	return nil
}

func (m *mockManager) ArrangeAll(_ *sway.Node) error { return nil }

func (m *mockManager) WindowIDs() []int64 { return m.windowIDs }
