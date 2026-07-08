# ProjectTabs Layout Manager

A layout manager for **workspace 8** that provides per-project containers within a tabbed workspace. Each project gets its own "tab" containing a terminal and (optionally) a browser, with the ability to side-by-side or fullscreen either pane.

## Current State (Workspace 8 Today)

```
workspace "8" (tabbed)          ← sway tabbed layout
  ├── sm:kitchen     (kitty)    ← one tab per project
  ├── sm:webapp      (kitty)
  └── sm:session-mgr (kitty)
```

SM sets `layout tabbed` on workspace 8. Each project is a flat tab containing one kitty window. `$mod+y`/`$mod+o` switches between project tabs.

## Desired State

```
workspace "8" (tabbed)                    ← outer: project tabs
  ├── [pt:kitchen] (splith)               ← project container
  │   ├── sm:kitchen         (kitty)      ← terminal
  │   └── kitchen.localhost  (chromium)   ← browser
  ├── [pt:webapp] (splith)
  │   ├── sm:webapp          (kitty)
  │   └── webapp.localhost   (chromium)
  └── [pt:session-mgr] (splith)
      └── sm:session-mgr     (kitty)      ← no browser = fullscreen
```

Each project tab is a **container** that holds terminal + browser side-by-side. Projects without a browser show terminal fullscreen (the container is effectively transparent).

### View Modes

Within each project container:

| Mode | Container Layout | Effect |
|------|-----------------|--------|
| **Split** | `splith` | Terminal left, browser right (configurable ratio) |
| **Terminal focused** | `tabbed` | Terminal fills pane (browser tab header visible) |
| **Browser focused** | `tabbed` | Browser fills pane (terminal tab header visible) |

Toggle between split and focused modes with a keybinding. When in tabbed (focused) mode, whichever pane has focus is visible.

## Architecture

### Who Does What

| Responsibility | Owner | Mechanism |
|---------------|-------|-----------|
| Launch kitty window | Session Manager | `kitty --title sm:{project}` |
| Launch/assign browser | Session Manager | Chrome extension + native host |
| Move windows to ws 8 | Session Manager | `swaymsg [title=...] move to workspace 8` |
| Create project containers | **tilekeeper** | Sway IPC: move windows into splith container |
| Toggle split/fullscreen | **tilekeeper** | Sway IPC: switch container layout |
| Track project→window mapping | Session Manager | State file + IPC to tilekeeper |
| Set workspace tabbed | **tilekeeper** | On startup/arrange |

### Window Detection

tilekeeper identifies project windows two ways:

1. **Automatic**: Detect `sm:{project}` title prefix on windows arriving at workspace 8. Extract project name, group windows by project.

2. **IPC from SM** (preferred): SM explicitly tells tilekeeper which windows form a project group:
   ```json
   {"command": "project add", "args": "{\"name\":\"kitchen\",\"terminal\":1234,\"browser\":5678}"}
   ```

Automatic detection is the fallback — it works even without SM coordination. But SM-driven IPC is more reliable because SM knows the browser→project mapping from its Chrome extension.

### Browser Window Matching (Automatic Mode)

When automatic detection is used, tilekeeper needs to match browser windows to projects. Options (in priority order):

1. **Sway mark**: SM (or Chrome extension native host) sets mark `sm:{project}:browser` on the browser window
2. **Window title**: Chrome extension prefixes titles with `[{project}]` — tilekeeper can match `[kitchen]` prefix
3. **SM state file**: Read `active_sessions.json` and correlate window IDs

We recommend **option 1** (sway marks) — it's the most reliable and doesn't require polling.

## IPC Protocol Extensions

New commands for the ProjectTabs layout manager:

### From Session Manager → Layout Manager

```json
// Register a project with its windows
{"command": "project add", "workspace": "8", 
 "args": "{\"name\":\"kitchen\",\"terminal_id\":1234}"}

// Add browser window to existing project  
{"command": "project set-browser", "workspace": "8",
 "args": "{\"name\":\"kitchen\",\"browser_id\":5678}"}

// Remove a project (e.g., on sm close)
{"command": "project remove", "workspace": "8",
 "args": "{\"name\":\"kitchen\"}"}
```

### User Commands (via nop bindings or CLI)

```json
// Toggle between split and tabbed view for focused project
{"command": "toggle-split", "workspace": "8"}

// Focus the terminal pane in current project
{"command": "focus terminal", "workspace": "8"}

// Focus the browser pane in current project
{"command": "focus browser", "workspace": "8"}
```

### Sway Nop Bindings

```bash
# Toggle split/fullscreen for current project
bindsym $mod+v nop tilekeeper toggle-split

# Focus terminal or browser directly
bindsym $mod+t nop tilekeeper focus terminal
bindsym $mod+b nop tilekeeper focus browser
```

## Configuration

```toml
[workspace.8]
defaultLayout = "ProjectTabs"

[workspace.8.projectTabs]
splitRatio = 50          # terminal:browser width ratio (default 50:50)
terminalSide = "left"    # "left" or "right"
defaultMode = "split"    # "split" or "tabbed" (start in split or fullscreen)
autoDetect = true        # auto-detect sm: windows (vs IPC-only)
```

## Implementation Plan (tilekeeper side)

### Phase 1: Core ProjectTabs Manager

Implement `layout.ProjectTabs` satisfying the `layout.Manager` interface:

- **State**: `map[string]*ProjectGroup` — each group has project name, terminal con ID, browser con ID, current mode (split/tabbed)
- **WindowAdded**: If window title matches `sm:{project}`, create/update group. If browser mark/title matches, associate with group.
- **WindowRemoved**: Remove from group. If terminal removed, remove entire group.
- **ArrangeAll**: Set workspace to tabbed. For each group with 2+ windows, create splith container.
- **Command("toggle-split")**: Switch focused project's container between splith and tabbed.
- **Command("focus terminal")**: Focus the terminal window in current project.
- **Command("focus browser")**: Focus the browser window in current project.

### Phase 2: SM IPC Commands

Extend Hub's IPC handler for ProjectTabs-specific commands:
- `project add`, `project remove`, `project set-browser`
- These directly manipulate ProjectTabs state + trigger rearrangement

### Phase 3: Browser Detection

Implement automatic browser→project matching:
- Watch for windows with marks matching `sm:{project}:browser`
- Fallback: title prefix matching `[{project}]`

## Sway Tree Operations

### Creating a Project Container

When a project gets both terminal and browser:

```
1. Focus terminal:       [con_id=1234] focus
2. Set split horizontal: split horizontal
3. Move browser in:      [con_id=5678] move to workspace 8
4. (Browser appears next to terminal in a new splith container)
```

Or more reliably using marks:

```
1. Mark terminal:        [con_id=1234] mark --add pt:kitchen
2. Move browser:         [con_id=5678] move to mark pt:kitchen
3. Set container layout: [con_id=1234] focus; layout splith
```

### Toggling Split Mode

```
# Find the parent container of the focused window
# Switch its layout between splith and tabbed
[con_id=<focused>] focus
layout toggle splith tabbed
```

## Open Questions

### OQ-TK-1: Split Ratio Control
Should the terminal/browser split ratio be adjustable at runtime (like MasterStack's grow/shrink)?
Or is a fixed config value sufficient?

### OQ-TK-2: More Than Two Panes?
Should a project container support more than terminal + browser? E.g., a secondary terminal, a file manager? This would make it more like a mini-MasterStack per project.

### OQ-TK-3: Project Tab Order
Who controls the tab order of projects in workspace 8 — SM or tilekeeper?
Currently SM tracks `workspace_position`. tilekeeper should preserve whatever order exists.

### OQ-TK-4: Multiple Browsers Per Project
Can a project have multiple browser windows? If so, do they stack in the browser pane (tabbed/stacking), or does only one browser window belong to a project?

### OQ-TK-5: Backward Compatibility
When ProjectTabs is active on workspace 8, projects without browsers should behave identically to today (single kitty window = single tab). The layout manager should be invisible until a browser is added.

## SM Integration Checklist

For session-manager to fully use ProjectTabs:

- [ ] SM sets mark `sm:{project}:browser` on browser windows via Chrome extension native host
- [ ] SM calls `project add` IPC when opening a project (after kitty + browser are placed)
- [ ] SM calls `project remove` IPC when closing a project
- [ ] SM calls `project set-browser` when Chrome extension assigns a window to a project
- [ ] SM preserves compatibility with `tilekeeper` not running (fall back to flat tabbed)
- [ ] SM updates `active_sessions.json` with browser window IDs
