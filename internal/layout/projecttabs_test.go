package layout

import (
	"fmt"
	"testing"

	"github.com/mschulkind-oss/tilekeeper/internal/sway"
)

func newTestProjectTabs(mock *sway.Mock, cfg ProjectTabsConfig) *ProjectTabs {
	if cfg.SplitRatio == 0 {
		cfg.SplitRatio = 50
	}
	if cfg.AutoDetect {
		// default true for most tests
	}
	return NewProjectTabs(mock, cfg)
}

func ws8Node() *sway.Node {
	return &sway.Node{Type: "workspace", Name: "8", Layout: "tabbed"}
}

func makeProjectWorkspace(projects ...string) *sway.Node {
	sway.ResetIDCounter()
	ws := &sway.Node{
		ID:     99,
		Type:   "workspace",
		Name:   "8",
		Layout: "tabbed",
	}
	for _, p := range projects {
		win := sway.CreateWindow("kitty")
		win.Name = "sm:" + p
		ws.Nodes = append(ws.Nodes, win)
	}
	root := &sway.Node{Type: "root", Nodes: []*sway.Node{
		{Type: "output", Nodes: []*sway.Node{ws}},
	}}
	root.SetParents()
	return root
}

func addBrowserToWorkspace(root *sway.Node, project string) *sway.Node {
	ws := findWSByName(root, "8")
	browser := sway.CreateWindow("chromium")
	browser.Name = "[" + project + "] " + project + ".localhost"
	browser.Parent = ws
	ws.Nodes = append(ws.Nodes, browser)
	return browser
}

func addMarkedBrowserToWorkspace(root *sway.Node, project string) *sway.Node {
	ws := findWSByName(root, "8")
	browser := sway.CreateWindow("chromium")
	browser.Name = project + ".localhost"
	browser.Marks = []string{"sm:" + project + ":browser"}
	browser.Parent = ws
	ws.Nodes = append(ws.Nodes, browser)
	return browser
}

// --- Tests ---

func TestProjectTabsName(t *testing.T) {
	mock := sway.NewMock()
	pt := newTestProjectTabs(mock, ProjectTabsConfig{})
	if pt.Name() != "ProjectTabs" {
		t.Errorf("Name() = %q, want ProjectTabs", pt.Name())
	}
}

func TestProjectTabsDefaultConfig(t *testing.T) {
	mock := sway.NewMock()
	pt := newTestProjectTabs(mock, ProjectTabsConfig{})
	if pt.splitRatio != 50 {
		t.Errorf("splitRatio = %d, want 50", pt.splitRatio)
	}
	if pt.terminalSide != "left" {
		t.Errorf("terminalSide = %q, want left", pt.terminalSide)
	}
	if pt.defaultMode != "split" {
		t.Errorf("defaultMode = %q, want split", pt.defaultMode)
	}
}

func TestProjectTabsInvalidRatio(t *testing.T) {
	mock := sway.NewMock()
	pt := NewProjectTabs(mock, ProjectTabsConfig{SplitRatio: 150})
	if pt.splitRatio != 50 {
		t.Errorf("splitRatio = %d, want 50 (clamped)", pt.splitRatio)
	}

	pt2 := NewProjectTabs(mock, ProjectTabsConfig{SplitRatio: -5})
	if pt2.splitRatio != 50 {
		t.Errorf("splitRatio = %d, want 50 (clamped)", pt2.splitRatio)
	}
}

func TestExtractProjectName(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"sm:kitchen", "kitchen"},
		{"sm:webapp", "webapp"},
		{"sm:session-manager", "session-manager"},
		{"sm:kitchen:browser", ""},  // sub-identifier
		{"kitty", ""},               // no prefix
		{"firefox", ""},             // no prefix
		{"[kitchen] localhost", ""}, // browser title
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := &sway.Node{Name: tt.name}
			got := extractProjectName(node)
			if got != tt.want {
				t.Errorf("extractProjectName(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestExtractBrowserProject(t *testing.T) {
	tests := []struct {
		desc  string
		name  string
		marks []string
		want  string
	}{
		{"mark match", "kitchen.localhost", []string{"sm:kitchen:browser"}, "kitchen"},
		{"title prefix", "[kitchen] kitchen.localhost", nil, "kitchen"},
		{"title prefix with space", "[my-project] site", nil, "my-project"},
		{"no match", "firefox", nil, ""},
		{"sm title not browser", "sm:kitchen", nil, ""},
		{"empty marks empty title", "", nil, ""},
		{"mark priority over title", "[wrong] page", []string{"sm:correct:browser"}, "correct"},
	}
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			node := &sway.Node{Name: tt.name, Marks: tt.marks}
			got := extractBrowserProject(node)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAutoDetectTerminals(t *testing.T) {
	mock := sway.NewMock()
	root := makeProjectWorkspace("kitchen", "webapp")
	mock.Tree = root

	pt := newTestProjectTabs(mock, ProjectTabsConfig{AutoDetect: true})
	pt.ArrangeAll(ws8Node())

	if len(pt.groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(pt.groups))
	}
	g := pt.groups["kitchen"]
	if g == nil {
		t.Fatal("kitchen group not found")
	}
	if g.TerminalID == 0 {
		t.Error("kitchen terminal ID should be set")
	}
	if g.HasBrowser() {
		t.Error("kitchen should not have browser yet")
	}
}

func TestAutoDetectBrowserByTitle(t *testing.T) {
	mock := sway.NewMock()
	root := makeProjectWorkspace("kitchen")
	addBrowserToWorkspace(root, "kitchen")
	mock.Tree = root

	pt := newTestProjectTabs(mock, ProjectTabsConfig{AutoDetect: true})
	pt.ArrangeAll(ws8Node())

	g := pt.groups["kitchen"]
	if g == nil {
		t.Fatal("kitchen group not found")
	}
	if !g.HasBrowser() {
		t.Error("kitchen should have browser detected by title prefix")
	}
}

func TestAutoDetectBrowserByMark(t *testing.T) {
	mock := sway.NewMock()
	root := makeProjectWorkspace("kitchen")
	addMarkedBrowserToWorkspace(root, "kitchen")
	mock.Tree = root

	pt := newTestProjectTabs(mock, ProjectTabsConfig{AutoDetect: true})
	pt.ArrangeAll(ws8Node())

	g := pt.groups["kitchen"]
	if g == nil {
		t.Fatal("kitchen group not found")
	}
	if !g.HasBrowser() {
		t.Error("kitchen should have browser detected by mark")
	}
}

func TestArrangeSetsTabbed(t *testing.T) {
	mock := sway.NewMock()
	root := makeProjectWorkspace("kitchen")
	mock.Tree = root

	pt := newTestProjectTabs(mock, ProjectTabsConfig{AutoDetect: true})
	pt.ArrangeAll(ws8Node())

	// Should have issued "layout tabbed" for the workspace.
	found := false
	for _, cmd := range mock.Commands {
		if cmd == "layout tabbed" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'layout tabbed' command, got: %v", mock.Commands)
	}
}

func TestArrangeGroupWithBrowser(t *testing.T) {
	mock := sway.NewMock()
	root := makeProjectWorkspace("kitchen")
	addBrowserToWorkspace(root, "kitchen")
	mock.Tree = root

	pt := newTestProjectTabs(mock, ProjectTabsConfig{AutoDetect: true})
	pt.ArrangeAll(ws8Node())

	// Should have issued split horizontal and layout commands.
	hasSplit := false
	for _, cmd := range mock.Commands {
		if cmd == "split horizontal" {
			hasSplit = true
		}
	}
	if !hasSplit {
		t.Errorf("expected 'split horizontal' for project with browser, got: %v", mock.Commands)
	}
}

func TestWindowAddedAutoDetect(t *testing.T) {
	mock := sway.NewMock()
	root := makeProjectWorkspace("kitchen")
	mock.Tree = root

	pt := newTestProjectTabs(mock, ProjectTabsConfig{AutoDetect: true})
	pt.WindowAdded(ws8Node(), nil)

	if len(pt.groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(pt.groups))
	}
}

func TestWindowAddedNoAutoDetect(t *testing.T) {
	mock := sway.NewMock()
	root := makeProjectWorkspace("kitchen")
	mock.Tree = root

	pt := newTestProjectTabs(mock, ProjectTabsConfig{AutoDetect: false})
	pt.WindowAdded(ws8Node(), nil)

	if len(pt.groups) != 0 {
		t.Fatalf("groups = %d, want 0 (autoDetect off)", len(pt.groups))
	}
}

func TestWindowRemoved(t *testing.T) {
	mock := sway.NewMock()
	root := makeProjectWorkspace("kitchen", "webapp")
	mock.Tree = root

	pt := newTestProjectTabs(mock, ProjectTabsConfig{AutoDetect: true})
	pt.ArrangeAll(ws8Node())

	if len(pt.groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(pt.groups))
	}

	// Remove kitchen's terminal from the tree.
	ws := findWSByName(root, "8")
	ws.Nodes = ws.Nodes[1:] // remove first child (kitchen)

	pt.WindowRemoved(ws8Node(), nil)

	if len(pt.groups) != 1 {
		t.Fatalf("groups after remove = %d, want 1", len(pt.groups))
	}
	if _, exists := pt.groups["kitchen"]; exists {
		t.Error("kitchen group should have been removed")
	}
}

func TestWindowRemovedBrowser(t *testing.T) {
	mock := sway.NewMock()
	root := makeProjectWorkspace("kitchen")
	browser := addBrowserToWorkspace(root, "kitchen")
	mock.Tree = root

	pt := newTestProjectTabs(mock, ProjectTabsConfig{AutoDetect: true})
	pt.ArrangeAll(ws8Node())

	g := pt.groups["kitchen"]
	if !g.HasBrowser() {
		t.Fatal("kitchen should have browser")
	}

	// Remove browser from tree.
	ws := findWSByName(root, "8")
	var filtered []*sway.Node
	for _, n := range ws.Nodes {
		if n.ID != browser.ID {
			filtered = append(filtered, n)
		}
	}
	ws.Nodes = filtered

	pt.WindowRemoved(ws8Node(), nil)

	g = pt.groups["kitchen"]
	if g == nil {
		t.Fatal("kitchen group should still exist")
	}
	if g.HasBrowser() {
		t.Error("kitchen should no longer have browser")
	}
}

func TestToggleSplit(t *testing.T) {
	mock := sway.NewMock()
	root := makeProjectWorkspace("kitchen")
	addBrowserToWorkspace(root, "kitchen")
	mock.Tree = root

	pt := newTestProjectTabs(mock, ProjectTabsConfig{AutoDetect: true})
	pt.ArrangeAll(ws8Node())

	// Set a focused window.
	ws := findWSByName(root, "8")
	ws.Nodes[0].Focused = true

	g := pt.groups["kitchen"]
	if g.Mode != "split" {
		t.Fatalf("initial mode = %q, want split", g.Mode)
	}

	// Toggle to tabbed.
	mock.Commands = nil
	pt.Command("toggle-split", ws8Node())

	if g.Mode != "tabbed" {
		t.Errorf("mode after toggle = %q, want tabbed", g.Mode)
	}

	// Toggle back to split.
	pt.Command("toggle-split", ws8Node())
	if g.Mode != "split" {
		t.Errorf("mode after second toggle = %q, want split", g.Mode)
	}
}

func TestFocusTerminal(t *testing.T) {
	mock := sway.NewMock()
	root := makeProjectWorkspace("kitchen")
	browser := addBrowserToWorkspace(root, "kitchen")
	mock.Tree = root

	pt := newTestProjectTabs(mock, ProjectTabsConfig{AutoDetect: true})
	pt.ArrangeAll(ws8Node())

	// Focus the browser.
	browser.Focused = true

	mock.Commands = nil
	pt.Command("focus terminal", ws8Node())

	g := pt.groups["kitchen"]
	found := false
	expected := fmt.Sprintf("[con_id=%d] focus", g.TerminalID)
	for _, cmd := range mock.Commands {
		if cmd == expected {
			found = true
		}
	}
	if !found {
		t.Errorf("expected focus terminal command %q, got: %v", expected, mock.Commands)
	}
}

func TestFocusBrowser(t *testing.T) {
	mock := sway.NewMock()
	root := makeProjectWorkspace("kitchen")
	addBrowserToWorkspace(root, "kitchen")
	mock.Tree = root

	pt := newTestProjectTabs(mock, ProjectTabsConfig{AutoDetect: true})
	pt.ArrangeAll(ws8Node())

	// Focus terminal.
	ws := findWSByName(root, "8")
	ws.Nodes[0].Focused = true

	mock.Commands = nil
	pt.Command("focus browser", ws8Node())

	g := pt.groups["kitchen"]
	found := false
	expected := fmt.Sprintf("[con_id=%d] focus", g.BrowserID)
	for _, cmd := range mock.Commands {
		if cmd == expected {
			found = true
		}
	}
	if !found {
		t.Errorf("expected focus browser command %q, got: %v", expected, mock.Commands)
	}
}

func TestFocusBrowserNoBrowser(t *testing.T) {
	mock := sway.NewMock()
	root := makeProjectWorkspace("kitchen")
	mock.Tree = root

	pt := newTestProjectTabs(mock, ProjectTabsConfig{AutoDetect: true})
	pt.ArrangeAll(ws8Node())

	ws := findWSByName(root, "8")
	ws.Nodes[0].Focused = true

	mock.Commands = nil
	pt.Command("focus browser", ws8Node())

	// Should issue no focus commands since there's no browser.
	for _, cmd := range mock.Commands {
		if cmd != "" {
			t.Errorf("expected no commands for focus browser without browser, got: %v", mock.Commands)
			break
		}
	}
}

func TestProjectAddIPC(t *testing.T) {
	mock := sway.NewMock()
	root := makeProjectWorkspace("kitchen")
	mock.Tree = root

	pt := newTestProjectTabs(mock, ProjectTabsConfig{AutoDetect: false})

	ws := findWSByName(root, "8")
	termID := ws.Nodes[0].ID
	pt.Command(fmt.Sprintf("project add kitchen %d", termID), ws8Node())

	g := pt.groups["kitchen"]
	if g == nil {
		t.Fatal("kitchen group should exist after project add")
	}
	if g.TerminalID != termID {
		t.Errorf("terminal ID = %d, want %d", g.TerminalID, termID)
	}
}

func TestProjectAddWithBrowserIPC(t *testing.T) {
	mock := sway.NewMock()
	root := makeProjectWorkspace("kitchen")
	browser := addBrowserToWorkspace(root, "kitchen")
	mock.Tree = root

	pt := newTestProjectTabs(mock, ProjectTabsConfig{AutoDetect: false})

	ws := findWSByName(root, "8")
	termID := ws.Nodes[0].ID
	pt.Command(fmt.Sprintf("project add kitchen %d %d", termID, browser.ID), ws8Node())

	g := pt.groups["kitchen"]
	if g == nil {
		t.Fatal("kitchen group should exist")
	}
	if !g.HasBrowser() {
		t.Error("kitchen should have browser from IPC")
	}
}

func TestProjectRemoveIPC(t *testing.T) {
	mock := sway.NewMock()
	root := makeProjectWorkspace("kitchen")
	mock.Tree = root

	pt := newTestProjectTabs(mock, ProjectTabsConfig{AutoDetect: true})
	pt.ArrangeAll(ws8Node())

	pt.Command("project remove kitchen", ws8Node())

	if _, exists := pt.groups["kitchen"]; exists {
		t.Error("kitchen should be removed")
	}
}

func TestProjectSetBrowserIPC(t *testing.T) {
	mock := sway.NewMock()
	root := makeProjectWorkspace("kitchen")
	browser := addBrowserToWorkspace(root, "kitchen")
	mock.Tree = root

	pt := newTestProjectTabs(mock, ProjectTabsConfig{AutoDetect: true})
	pt.ArrangeAll(ws8Node())

	// Initially auto-detected browser.
	g := pt.groups["kitchen"]
	if !g.HasBrowser() {
		t.Fatal("should have auto-detected browser")
	}

	// Now set a different browser via IPC.
	newBrowser := sway.CreateWindow("firefox")
	ws := findWSByName(root, "8")
	newBrowser.Parent = ws
	ws.Nodes = append(ws.Nodes, newBrowser)

	pt.Command(fmt.Sprintf("project set-browser kitchen %d", newBrowser.ID), ws8Node())

	if g.BrowserID != newBrowser.ID {
		t.Errorf("browser ID = %d, want %d", g.BrowserID, newBrowser.ID)
	}
	_ = browser // original browser is orphaned
}

func TestWindowIDs(t *testing.T) {
	mock := sway.NewMock()
	root := makeProjectWorkspace("kitchen", "webapp")
	addBrowserToWorkspace(root, "kitchen")
	mock.Tree = root

	pt := newTestProjectTabs(mock, ProjectTabsConfig{AutoDetect: true})
	pt.ArrangeAll(ws8Node())

	ids := pt.WindowIDs()
	// kitchen terminal + kitchen browser + webapp terminal = 3
	if len(ids) != 3 {
		t.Errorf("WindowIDs() = %d, want 3", len(ids))
	}
}

func TestSplitRatioApplied(t *testing.T) {
	mock := sway.NewMock()
	root := makeProjectWorkspace("kitchen")
	addBrowserToWorkspace(root, "kitchen")
	mock.Tree = root

	pt := newTestProjectTabs(mock, ProjectTabsConfig{
		AutoDetect: true,
		SplitRatio: 70,
	})
	pt.ArrangeAll(ws8Node())

	// Should have a resize command with 20 ppt (70 - 50).
	found := false
	for _, cmd := range mock.Commands {
		if cmd != "" && contains(cmd, "resize grow width 20 ppt") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected resize command for 70%% ratio, got: %v", mock.Commands)
	}
}

func TestTerminalSideRight(t *testing.T) {
	mock := sway.NewMock()
	root := makeProjectWorkspace("kitchen")
	addBrowserToWorkspace(root, "kitchen")
	mock.Tree = root

	pt := newTestProjectTabs(mock, ProjectTabsConfig{
		AutoDetect:   true,
		TerminalSide: "right",
	})
	pt.ArrangeAll(ws8Node())

	found := false
	g := pt.groups["kitchen"]
	expected := fmt.Sprintf("[con_id=%d] move right", g.TerminalID)
	for _, cmd := range mock.Commands {
		if cmd == expected {
			found = true
		}
	}
	if !found {
		t.Errorf("expected terminal move right command, got: %v", mock.Commands)
	}
}

func TestDefaultModeTabbed(t *testing.T) {
	mock := sway.NewMock()
	root := makeProjectWorkspace("kitchen")
	addBrowserToWorkspace(root, "kitchen")
	mock.Tree = root

	pt := newTestProjectTabs(mock, ProjectTabsConfig{
		AutoDetect:  true,
		DefaultMode: "tabbed",
	})
	pt.ArrangeAll(ws8Node())

	g := pt.groups["kitchen"]
	if g.Mode != "tabbed" {
		t.Errorf("mode = %q, want tabbed", g.Mode)
	}
}

func TestProjectTabsUnknownCommand(t *testing.T) {
	mock := sway.NewMock()
	pt := newTestProjectTabs(mock, ProjectTabsConfig{})
	// Should not panic on unknown command.
	pt.Command("nonexistent", ws8Node())
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && stringContains(s, substr))
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
