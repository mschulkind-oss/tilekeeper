package layout

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/mschulkind-oss/tilekeeper/internal/sway"
)

// ProjectGroup tracks the windows belonging to a single project.
type ProjectGroup struct {
	Name       string
	TerminalID int64
	BrowserID  int64
	Mode       string // "split" or "tabbed"
}

// HasBrowser returns whether a browser window is associated.
func (g *ProjectGroup) HasBrowser() bool {
	return g.BrowserID != 0
}

// ProjectTabs is a layout manager for workspace 8 that provides per-project
// containers within a tabbed workspace. Each project gets a tab containing
// a terminal and (optionally) a browser side-by-side.
type ProjectTabs struct {
	client     sway.Client
	log        *slog.Logger
	groups     map[string]*ProjectGroup // project name → group
	conToGroup map[int64]string         // con ID → project name

	// Config
	splitRatio   int    // terminal width percentage (default 50)
	terminalSide string // "left" or "right"
	defaultMode  string // "split" or "tabbed"
	autoDetect   bool   // auto-detect sm: windows
}

// ProjectTabsConfig holds configuration for the ProjectTabs layout manager.
type ProjectTabsConfig struct {
	SplitRatio   int
	TerminalSide string
	DefaultMode  string
	AutoDetect   bool
}

// NewProjectTabs creates a new ProjectTabs layout manager.
func NewProjectTabs(client sway.Client, cfg ProjectTabsConfig) *ProjectTabs {
	if cfg.SplitRatio <= 0 || cfg.SplitRatio > 99 {
		cfg.SplitRatio = 50
	}
	if cfg.TerminalSide == "" {
		cfg.TerminalSide = "left"
	}
	if cfg.DefaultMode == "" {
		cfg.DefaultMode = "split"
	}

	return &ProjectTabs{
		client:       client,
		log:          slog.Default(),
		groups:       make(map[string]*ProjectGroup),
		conToGroup:   make(map[int64]string),
		splitRatio:   cfg.SplitRatio,
		terminalSide: cfg.TerminalSide,
		defaultMode:  cfg.DefaultMode,
		autoDetect:   cfg.AutoDetect,
	}
}

func (pt *ProjectTabs) Name() string { return "ProjectTabs" }

// SetLogger attaches a component-scoped logger for this manager.
func (pt *ProjectTabs) SetLogger(l *slog.Logger) {
	if l != nil {
		pt.log = l
	}
}

// WindowAdded handles a new window appearing on the workspace.
// If autoDetect is enabled, it matches sm:{project} titles for terminals
// and [project] title prefixes or sm:{project}:browser marks for browsers.
func (pt *ProjectTabs) WindowAdded(ws *sway.Node, window *sway.Node) error {
	if !pt.autoDetect {
		return nil
	}

	tree, err := pt.client.GetTree()
	if err != nil {
		return err
	}
	tree.SetParents()

	wsNode := findWSByName(tree, ws.Name)
	if wsNode == nil {
		return nil
	}

	// Scan all tiling leaves for sm: windows not yet tracked.
	for _, leaf := range TilingLeaves(wsNode) {
		if _, tracked := pt.conToGroup[leaf.ID]; tracked {
			continue
		}

		if project := extractProjectName(leaf); project != "" {
			pt.addTerminal(project, leaf.ID)
		} else if project := extractBrowserProject(leaf); project != "" {
			pt.addBrowser(project, leaf.ID)
		}
	}

	pt.arrange(ws.Name)
	return nil
}

// WindowRemoved handles a window disappearing from the workspace.
func (pt *ProjectTabs) WindowRemoved(ws *sway.Node, window *sway.Node) error {
	// Find which tracked windows are gone.
	tree, err := pt.client.GetTree()
	if err != nil {
		return err
	}
	tree.SetParents()

	live := make(map[int64]bool)
	wsNode := findWSByName(tree, ws.Name)
	if wsNode != nil {
		for _, leaf := range TilingLeaves(wsNode) {
			live[leaf.ID] = true
		}
	}

	for conID, project := range pt.conToGroup {
		if live[conID] {
			continue
		}
		group := pt.groups[project]
		if group == nil {
			delete(pt.conToGroup, conID)
			continue
		}
		if group.TerminalID == conID {
			// Terminal gone — remove entire group.
			delete(pt.conToGroup, conID)
			if group.BrowserID != 0 {
				delete(pt.conToGroup, group.BrowserID)
			}
			delete(pt.groups, project)
		} else if group.BrowserID == conID {
			group.BrowserID = 0
			delete(pt.conToGroup, conID)
		}
	}
	return nil
}

// WindowFocused handles focus change — no-op for ProjectTabs.
func (pt *ProjectTabs) WindowFocused(ws *sway.Node, window *sway.Node) error {
	return nil
}

// Command handles user and IPC commands.
func (pt *ProjectTabs) Command(cmd string, ws *sway.Node) error {
	wsName := ws.Name
	switch {
	case cmd == "toggle-split":
		pt.toggleSplit(wsName)
	case cmd == "focus terminal":
		pt.focusPane(wsName, true)
	case cmd == "focus browser":
		pt.focusPane(wsName, false)
	case strings.HasPrefix(cmd, "project add "):
		pt.handleProjectAdd(cmd[len("project add "):], wsName)
	case strings.HasPrefix(cmd, "project remove "):
		pt.handleProjectRemove(cmd[len("project remove "):])
	case strings.HasPrefix(cmd, "project set-browser "):
		pt.handleProjectSetBrowser(cmd[len("project set-browser "):], wsName)
	default:
		pt.log.Warn("unknown ProjectTabs command", "cmd", cmd)
	}
	return nil
}

// ArrangeAll does a full rearrangement of the workspace.
func (pt *ProjectTabs) ArrangeAll(ws *sway.Node) error {
	if pt.autoDetect {
		pt.discoverWindows(ws.Name)
	}
	pt.arrange(ws.Name)
	return nil
}

// WindowIDs returns all tracked window IDs.
func (pt *ProjectTabs) WindowIDs() []int64 {
	var ids []int64
	for id := range pt.conToGroup {
		ids = append(ids, id)
	}
	return ids
}

// --- Internal ---

func (pt *ProjectTabs) addTerminal(project string, conID int64) {
	group, exists := pt.groups[project]
	if !exists {
		group = &ProjectGroup{
			Name: project,
			Mode: pt.defaultMode,
		}
		pt.groups[project] = group
	}
	group.TerminalID = conID
	pt.conToGroup[conID] = project
}

func (pt *ProjectTabs) addBrowser(project string, conID int64) {
	group, exists := pt.groups[project]
	if !exists {
		group = &ProjectGroup{
			Name: project,
			Mode: pt.defaultMode,
		}
		pt.groups[project] = group
	}
	group.BrowserID = conID
	pt.conToGroup[conID] = project
}

func (pt *ProjectTabs) discoverWindows(wsName string) {
	tree, err := pt.client.GetTree()
	if err != nil {
		return
	}
	tree.SetParents()

	wsNode := findWSByName(tree, wsName)
	if wsNode == nil {
		return
	}

	for _, leaf := range TilingLeaves(wsNode) {
		if _, tracked := pt.conToGroup[leaf.ID]; tracked {
			continue
		}
		if project := extractProjectName(leaf); project != "" {
			pt.addTerminal(project, leaf.ID)
		} else if project := extractBrowserProject(leaf); project != "" {
			pt.addBrowser(project, leaf.ID)
		}
	}
}

func (pt *ProjectTabs) arrange(wsName string) {
	// Set workspace to tabbed layout.
	pt.client.RunCommand(fmt.Sprintf("[workspace=%s] focus", wsName))
	pt.client.RunCommand("layout tabbed")

	// For each project with a browser, create a splith container.
	for _, group := range pt.groups {
		if !group.HasBrowser() {
			continue
		}
		pt.arrangeGroup(group)
	}
}

func (pt *ProjectTabs) arrangeGroup(group *ProjectGroup) {
	termID := group.TerminalID
	browID := group.BrowserID

	// Focus terminal, set split horizontal, move browser next to it.
	pt.client.RunCommand(fmt.Sprintf("[con_id=%d] focus", termID))
	pt.client.RunCommand("split horizontal")
	pt.client.RunCommand(fmt.Sprintf("[con_id=%d] move to mark pt:%s", browID, group.Name))

	// Mark the terminal so we can find the project container.
	pt.client.RunCommand(fmt.Sprintf("[con_id=%d] mark --add pt:%s", termID, group.Name))

	// Order: terminal side preference.
	if pt.terminalSide == "right" {
		pt.client.RunCommand(fmt.Sprintf("[con_id=%d] move right", termID))
	}

	// Set container layout based on mode.
	pt.client.RunCommand(fmt.Sprintf("[con_id=%d] focus", termID))
	if group.Mode == "tabbed" {
		pt.client.RunCommand("layout tabbed")
	} else {
		pt.client.RunCommand("layout splith")
	}

	// Apply split ratio.
	if pt.splitRatio != 50 {
		pt.applySplitRatio(group)
	}
}

func (pt *ProjectTabs) applySplitRatio(group *ProjectGroup) {
	// Resize terminal to desired ratio.
	// Sway resize uses pixels or ppt (percentage points).
	termID := group.TerminalID
	diff := pt.splitRatio - 50
	if diff == 0 {
		return
	}
	direction := "grow"
	if diff < 0 {
		direction = "shrink"
		diff = -diff
	}
	pt.client.RunCommand(fmt.Sprintf(
		"[con_id=%d] resize %s width %d ppt", termID, direction, diff,
	))
}

func (pt *ProjectTabs) toggleSplit(wsName string) {
	group := pt.focusedGroup()
	if group == nil || !group.HasBrowser() {
		return
	}
	if group.Mode == "split" {
		group.Mode = "tabbed"
		pt.client.RunCommand("layout tabbed")
	} else {
		group.Mode = "split"
		pt.client.RunCommand("layout splith")
		pt.applySplitRatio(group)
	}
}

func (pt *ProjectTabs) focusPane(wsName string, terminal bool) {
	group := pt.focusedGroup()
	if group == nil {
		return
	}
	var targetID int64
	if terminal {
		targetID = group.TerminalID
	} else {
		targetID = group.BrowserID
	}
	if targetID == 0 {
		return
	}
	pt.client.RunCommand(fmt.Sprintf("[con_id=%d] focus", targetID))
}

func (pt *ProjectTabs) focusedGroup() *ProjectGroup {
	tree, err := pt.client.GetTree()
	if err != nil {
		return nil
	}
	focused := tree.FindFocused()
	if focused == nil {
		return nil
	}
	project, ok := pt.conToGroup[focused.ID]
	if !ok {
		return nil
	}
	return pt.groups[project]
}

// handleProjectAdd processes: "project add <name> <terminal_id> [browser_id]"
func (pt *ProjectTabs) handleProjectAdd(args string, wsName string) {
	parts := strings.Fields(args)
	if len(parts) < 2 {
		pt.log.Warn("project add: need name and terminal_id")
		return
	}
	name := parts[0]
	var termID, browID int64
	fmt.Sscanf(parts[1], "%d", &termID)
	if len(parts) >= 3 {
		fmt.Sscanf(parts[2], "%d", &browID)
	}

	pt.addTerminal(name, termID)
	if browID != 0 {
		pt.addBrowser(name, browID)
	}
	pt.arrange(wsName)
}

func (pt *ProjectTabs) handleProjectRemove(args string) {
	name := strings.TrimSpace(args)
	group, exists := pt.groups[name]
	if !exists {
		return
	}
	delete(pt.conToGroup, group.TerminalID)
	if group.BrowserID != 0 {
		delete(pt.conToGroup, group.BrowserID)
	}
	delete(pt.groups, name)
}

func (pt *ProjectTabs) handleProjectSetBrowser(args string, wsName string) {
	parts := strings.Fields(args)
	if len(parts) < 2 {
		pt.log.Warn("project set-browser: need name and browser_id")
		return
	}
	name := parts[0]
	var browID int64
	fmt.Sscanf(parts[1], "%d", &browID)
	pt.addBrowser(name, browID)
	pt.arrange(wsName)
}

// findWSByName finds a workspace node by name in the tree.
func findWSByName(root *sway.Node, name string) *sway.Node {
	for _, ws := range root.Workspaces() {
		if ws.Name == name {
			return ws
		}
	}
	return nil
}

// extractProjectName extracts a project name from a sm:{project} window title.
func extractProjectName(node *sway.Node) string {
	name := node.Name
	if !strings.HasPrefix(name, "sm:") {
		return ""
	}
	project := strings.TrimPrefix(name, "sm:")
	// Ignore sub-identifiers like sm:kitchen:browser
	if strings.Contains(project, ":") {
		return ""
	}
	return project
}

// extractBrowserProject matches browser windows to projects.
// Checks for: sway mark sm:{project}:browser, or title prefix [{project}].
func extractBrowserProject(node *sway.Node) string {
	// Check marks first (most reliable).
	for _, mark := range node.Marks {
		if strings.HasPrefix(mark, "sm:") && strings.HasSuffix(mark, ":browser") {
			return mark[3 : len(mark)-8]
		}
	}
	// Fall back to title prefix: [project] ...
	name := node.Name
	if strings.HasPrefix(name, "[") {
		end := strings.Index(name, "]")
		if end > 1 {
			return name[1:end]
		}
	}
	return ""
}
