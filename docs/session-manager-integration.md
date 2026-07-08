# Session Manager Integration

This document describes how `tilekeeper` integrates with `session-manager` — a separate tool that manages projects, terminal sessions, and workspaces.

## Architecture

```
┌─────────────────────┐     ┌──────────────────────┐
│   session-manager   │     │      tilekeeper      │
│                     │     │                       │
│  Manages projects   │────▶│  Manages window       │
│  Manages sessions   │     │  layouts per workspace │
│  Opens terminals    │     │                       │
│  Tracks workspaces  │     │  Subscribes to sway   │
│                     │     │  events               │
└─────────────────────┘     └──────────────────────┘
         │                           │
         ▼                           ▼
    ┌─────────┐                 ┌─────────┐
    │  Sway   │◀────────────────│  Sway   │
    │ (IPC)   │                 │ (IPC)   │
    └─────────┘                 └─────────┘
```

### Separation of Concerns

| Concern | Owner | Details |
|---------|-------|---------|
| Project lifecycle | session-manager | Open/close/switch projects |
| Terminal windows | session-manager | Spawn kitty terminals with titles |
| Window layout | tilekeeper | Arrange tiling windows on workspaces |
| Window identity | session-manager | `sm:{project}` title convention |
| Layout persistence | tilekeeper | Capture/restore layout snapshots |
| Workspace assignment | session-manager | Move windows to workspaces |
| Layout rules | tilekeeper | MasterStack, tabbed, etc. |

## Integration Points

### 1. Window Identification

Session manager identifies windows by title prefix: `sm:{project_name}`.

Example: `sm:kitchen` identifies a window belonging to the "kitchen" project.

Layout manager can use this to:
- Apply project-specific layout preferences
- Group windows by project in multi-window layouts
- Detect which project a window belongs to

### 2. Active Sessions File

Session manager writes active session state to:
```
~/.local/share/session-manager/active_sessions.json
```

Layout manager can read this file to:
- Discover which projects are active
- Learn which workspaces have session-manager windows
- Avoid managing workspaces that session-manager controls exclusively (e.g., workspace 8)

### 3. Workspace Coordination

The user's configuration assigns workspaces by role:

| Workspace | Manager | Layout |
|-----------|---------|--------|
| 4 | tilekeeper | MasterStack (left) |
| 6 | tilekeeper | MasterStack |
| 7 | tilekeeper | MasterStack |
| 8 | session-manager | Tabbed (all projects) |
| 9 | tilekeeper | MasterStack (left) |

**Workspace 8** is special: session-manager manages it with tabbed mode for all projects. Layout manager should **not** manage workspace 8 — config it as `defaultLayout = "tabbed"` or `"none"`.

### 4. IPC Communication

Layout manager exposes a Unix socket IPC server. Session manager can send commands to control layouts when opening/closing projects:

```json
// Request layout change when opening a project
{"command": "layout MasterStack", "workspace": "4"}

// Request window arrangement after moving windows
{"command": "arrange-all", "workspace": "6"}

// Query layout state
{"command": "status"}
```

### 5. Layout Hints

When session-manager opens a project on a workspace, it can send layout hints:

```json
{
  "command": "set-preference",
  "workspace": "4",
  "args": "{\"masterWidth\": 75, \"stackSide\": \"left\"}"
}
```

This allows project-specific layout preferences without requiring tilekeeper config changes.

## Future Integration: Snapshot Restore

When session-manager restores a project, it can request tilekeeper to restore a saved layout:

1. Session-manager opens project windows on workspace
2. Session-manager sends: `{"command": "restore-snapshot", "workspace": "4", "args": "project-kitchen"}`
3. tilekeeper loads the named snapshot and arranges windows accordingly

### Window Matching Strategy

When restoring a snapshot, windows are matched to layout slots via:

1. **Session manager ID**: `sm:{project}` title → exact match
2. **Sway marks**: If windows have marks, match by mark
3. **App ID + title regex**: Fall back to app_id and title pattern matching

## Protocol Considerations

### Why Unix Socket + JSON?

- Simple to implement in both Go and Python
- Session manager can use standard Python socket + json modules
- Supports request/response for synchronous operations
- Allows streaming events for async notifications (future)

### Authentication

The Unix socket file permissions restrict access to the owning user. No additional authentication is needed for local single-user setups.

### Notifications (Future)

Layout manager could notify session-manager of layout changes:

```json
{"event": "layout-changed", "workspace": "4", "layout": "MasterStack"}
{"event": "window-arranged", "workspace": "4", "windows": [100, 101, 102]}
```

This enables session-manager to track which windows are in which layout positions.

## CLI Integration

Session manager can call tilekeeper CLI directly:

```bash
# Set layout for a workspace
tilekeeper msg layout MasterStack --workspace 4

# Get current layout
tilekeeper status

# Trigger rearrangement
tilekeeper msg swap-master --workspace 4
```

## Configuration Example

```toml
[tilekeeper]
defaultLayout = "none"
masterWidth = 75
visibleStackLimit = 3

# SM-managed workspaces use MasterStack
[workspace.4]
defaultLayout = "MasterStack"
stackSide = "left"

[workspace.6]
defaultLayout = "MasterStack"

[workspace.7]
defaultLayout = "MasterStack"

# Workspace 8: managed by session-manager, not tilekeeper
[workspace.8]
defaultLayout = "none"

[workspace.9]
defaultLayout = "MasterStack"
stackSide = "left"
```
