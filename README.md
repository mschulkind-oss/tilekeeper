<div align="center">

# tilekeeper

**A layout manager for Sway/Wayland — manage windows, not sessions**

Single-binary tiling layouts with per-workspace control, session manager integration, and full layout freedom.

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.26+-00ADD8.svg)](https://go.dev/)

[Features](#features) · [Quick Start](#-quick-start) · [Layouts](#-layouts) · [Configuration](#-configuration) · [Commands](#-commands) · [Session Manager Integration](#-session-manager-integration)

</div>

---

## Why?

Per-workspace tiling layouts are the right abstraction for Sway. tilekeeper takes that idea further:

- **Go** — single static binary, no runtime dependencies, instant startup
- **Sway-native** — pure Wayland, no i3 compatibility baggage
- **Session manager friendly** — designed to work alongside a session manager that handles *projects*; we handle *windows*
- **Layout freedom** — per-workspace layouts driven by declarative specs (user-defined custom layouts are on the [roadmap](docs/ROADMAP.md))

## Features

- 🖥️ **Per-workspace layouts** — a different layout per workspace: MasterStack, Tabbed, per-project (ProjectTabs), or none
- ⚡ **Single binary** — `go install` and done. No Python, no virtualenv, no pip
- 🔌 **Session manager integration** — your session manager owns project lifecycle; we arrange the windows
- 🎛️ **IPC control** — switch layouts, move windows, query state via Unix socket or CLI
- 💾 **Minimal footprint** — single daemon, subscribes to Sway events, <10MB RSS
- 🚧 **More on the way** — additional layouts, user-defined custom layouts, and snapshot capture/restore are [planned](docs/ROADMAP.md)

## 🚀 Quick Start

### Install

```bash
go install github.com/mschulkind-oss/tilekeeper/cmd/tilekeeper@latest
```

Or build from source:

```bash
git clone https://github.com/mschulkind-oss/tilekeeper
cd tilekeeper
just build
```

### Configure

```bash
mkdir -p ~/.config/tilekeeper
```

```toml
# ~/.config/tilekeeper/config.toml

[tilekeeper]
defaultLayout = "none"
masterWidth = 75
visibleStackLimit = 3

[workspace.4]
defaultLayout = "MasterStack"
stackSide = "left"

[workspace.6]
defaultLayout = "MasterStack"

[workspace.9]
defaultLayout = "MasterStack"
stackSide = "left"
```

### Add to Sway config

```bash
# ~/.config/sway/config

# Start the daemon
exec tilekeeper daemon

# Layout commands via nop bindings (zero-overhead — no process spawning)
bindsym $mod+Return nop tilekeeper swap-master
bindsym $mod+bracketleft nop tilekeeper rotate ccw
bindsym $mod+bracketright nop tilekeeper rotate cw
bindsym $mod+h nop tilekeeper master shrink
bindsym $mod+l nop tilekeeper master grow

# Or use CLI (spawns a process each time)
bindsym $mod+m exec tilekeeper msg layout MasterStack
```

### Start

Press `$mod+Shift+c` to reload Sway — tilekeeper starts automatically.

## 🎯 Layouts

Set per workspace via `defaultLayout` in config, or at runtime with
`tilekeeper msg layout <name>` (equivalently `nop tilekeeper layout <name>`).
**Available today:** MasterStack, Tabbed, ProjectTabs, none — the rest are
[planned](docs/ROADMAP.md) and marked 🚧.

### MasterStack

One primary window, stack on the side. Classic tiling.

```
┌─────────┬──────────┐
│         │ Stack 1  │
│ Master  ├──────────┤
│         │ Stack 2  │
│         ├──────────┤
│         │ Stack 3  │
└─────────┴──────────┘
```

`swap-master` promotes the focused window to master in MRU (alt-tab) order:
the old master becomes Stack 1 and the windows the promoted one passed each
shift down a slot — it is not a two-window trade. Because the previous master
is always waiting at the top of the stack, focusing the stack and promoting
(`$mod+o` then `$mod+Return`) alternates between the same two windows, leaving
the rest of the stack undisturbed.

### Tabbed

Every window becomes a tab in one flat tab strip; directional `focus`/`move`
navigate and reorder tabs.

### ProjectTabs

Per-project groups on a single workspace — each a terminal with an optional
browser side by side — driven by the session-manager integration (see
[docs/session-manager-integration.md](docs/session-manager-integration.md)).

### None

Disable layout management — fall back to Sway's default behavior.

---

### 🚧 Planned

On the [roadmap](docs/ROADMAP.md), not yet selectable:

#### Grid

Balanced grid that grows naturally.

```
┌─────────┬─────────┐
│    1    │    2    │
├─────────┼─────────┤
│    3    │    4    │
└─────────┴─────────┘
```

#### Columns

Fixed columns with configurable ratios. Windows fill columns left-to-right, stacking vertically within each column.

```
┌────┬──────────┬────┐
│    │          │    │
│ 30%│   40%    │30% │
│    │          │    │
└────┴──────────┴────┘
```

#### Spiral (Autotiling)

Alternating splits based on container dimensions. Natural golden-ratio feel.

```
┌───┬───────────────────┐
│   │         2         │
│   ├─────────┬─────────┤
│ 1 │    3    │    4    │
│   │         ├────┬────┤
│   │         │ 5  │ 6  │
└───┴─────────┴────┴────┘
```

#### DualTabbed

Two side-by-side tabbed groups. Navigate between groups horizontally, switch tabs within each group.

```
┌──────────────┬──────────────┐
│ [tab1] tab2  │ [tab3] tab4  │
│              │              │
│  primary     │  secondary   │
│  group       │  group       │
│              │              │
└──────────────┴──────────────┘
```

#### Custom Layouts

Layouts are **declarative data** — a tree of containers and slots. Define your own in JSON:

```json
{
  "name": "my-layout",
  "root": {
    "id": "root",
    "kind": "container",
    "layout": "splith",
    "children": [
      {"size": 30, "node": {"id": "sidebar", "kind": "slot", "role": "sidebar"}},
      {"size": 70, "node": {
        "id": "main-area",
        "kind": "container",
        "layout": "splitv",
        "children": [
          {"size": 70, "node": {"id": "editor", "kind": "slot", "role": "editor"}},
          {"size": 30, "node": {"id": "terminal", "kind": "slot", "role": "terminal"}}
        ]
      }}
    ]
  },
  "policy": {
    "default_slot": "editor",
    "master_slot": "editor",
    "master_count": 1,
    "overflow_slot": "sidebar"
  }
}
```

This creates:
```
┌──────┬───────────────┐
│      │    editor     │
│ side ├───────────────┤
│ bar  │   terminal    │
└──────┴───────────────┘
```

The tree model supports arbitrary nesting — containers (splith, splitv, tabbed, stacking) hold children with size ratios; slots are leaves that receive windows.

## 📸 Layout Capture & Restore 🚧

> **Planned** ([roadmap](docs/ROADMAP.md)) — the `capture`/`restore` CLI isn't
> wired up yet; the snapshot model below is the design.

Capture a running layout to a snapshot and restore it later:

```bash
# Capture current workspace layout
tilekeeper capture > my-layout.json

# Restore a captured layout
tilekeeper restore < my-layout.json
```

A snapshot records which windows are in which slots:

```json
{
  "spec_name": "master-stack",
  "workspace": "1",
  "slots": {
    "master": [{"app_id": "Alacritty", "instance_id": "editor-1", "focused": true}],
    "stack": [
      {"app_id": "firefox", "marks": ["browser"]},
      {"app_id": "Alacritty", "instance_id": "term-2"}
    ]
  },
  "captured_at": "2026-04-08T02:00:00Z"
}
```

Windows are matched on restore using (in priority order): session-manager instance ID → sway marks → app\_id + title pattern.

## ⚙️ Configuration

Single TOML file at `~/.config/tilekeeper/config.toml` (or `$XDG_CONFIG_HOME/tilekeeper/config.toml`).

```toml
# ~/.config/tilekeeper/config.toml

[tilekeeper]
defaultLayout = "none"
masterWidth = 75
visibleStackLimit = 3
debug = true

[workspace.4]
defaultLayout = "MasterStack"
stackSide = "left"

[workspace.6]
defaultLayout = "MasterStack"

[workspace.7]
defaultLayout = "MasterStack"

[workspace.8]
defaultLayout = "none"   # Session manager controls this workspace

[workspace.9]
defaultLayout = "MasterStack"
stackSide = "left"
```

### General Options

| Option | Default | Description |
|--------|---------|-------------|
| `defaultLayout` | `"none"` | Layout for unconfigured workspaces |
| `masterWidth` | `50` | Master window width (1–99%) |
| `stackSide` | `"right"` | Stack side: `left` or `right` |
| `stackLayout` | `"splitv"` | Stack arrangement: `splitv`, `splith`, `tabbed`, `stacking` |
| `visibleStackLimit` | `0` | Max visible stack windows (0 = unlimited) |
| `debug` | `false` | Enable debug logging |
| `logLevel` | `"info"` | `trace`, `debug`, `info`, `warn`, `error` |
| `ipcSocket` | `$XDG_RUNTIME_DIR/tilekeeper.sock` | IPC socket path |

Per-workspace overrides use `[workspace.<name-or-number>]` sections with the same options plus `defaultLayout`.

## 📋 Commands

### CLI

```bash
tilekeeper daemon                       # Start the daemon
tilekeeper msg swap-master              # send a layout command …
tilekeeper msg focus left               # … same strings as the nop bindings
tilekeeper msg layout MasterStack --workspace 4
tilekeeper status                       # all workspace states (JSON)
tilekeeper doctor                       # check environment
tilekeeper version                      # version info
```

The full command list is in [docs/COMMANDS.md](docs/COMMANDS.md).

### Sway `nop` bindings

All commands work as `nop` bindings for zero-overhead dispatch — the daemon intercepts binding events via Sway IPC, no process spawning:

```bash
# Layout switching
bindsym $mod+m nop tilekeeper swap-master
bindsym $mod+bracketleft nop tilekeeper rotate ccw
bindsym $mod+bracketright nop tilekeeper rotate cw

# Master size
bindsym $mod+h nop tilekeeper master grow
bindsym $mod+l nop tilekeeper master shrink

# Layout type
bindsym $mod+F1 nop tilekeeper layout MasterStack
bindsym $mod+F2 nop tilekeeper layout none
```

## 🔌 Session Manager Integration

tilekeeper is designed to work alongside a **session manager** — a separate tool that manages projects, their windows, and workspace assignments. The session manager handles *what* runs and *where*; tilekeeper handles *how windows are arranged*.

### Integration points

- **Workspace assignment**: Session manager assigns workspaces to projects; tilekeeper tiles windows on those workspaces
- **Layout hints**: Session manager can set preferred layouts per project via IPC
- **State queries**: Session manager can query which windows are on which workspace and how they're arranged
- **Event notifications**: tilekeeper emits events when layouts change, windows move, etc.

### IPC Protocol

Newline-delimited JSON over Unix domain socket:

```bash
# Set layout for a workspace
echo '{"command":"layout MasterStack","workspace":"4"}' | \
  socat - UNIX-CONNECT:$XDG_RUNTIME_DIR/tilekeeper.sock

# Query workspace state
echo '{"command":"status"}' | \
  socat - UNIX-CONNECT:$XDG_RUNTIME_DIR/tilekeeper.sock
```

See [docs/session-manager-integration.md](docs/session-manager-integration.md) for full integration details.

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                     tilekeeper                      │
│                                                      │
│  ┌──────────┐  ┌───────────┐  ┌──────────────────┐  │
│  │  Config   │  │   Sway    │  │  Layout Engine   │  │
│  │  (TOML)   │  │   IPC     │  │                  │  │
│  │           │  │  Client   │  │  ┌────────────┐  │  │
│  └─────┬─────┘  └─────┬─────┘  │  │MasterStack │  │  │
│        │              │         │  │Tabbed      │  │  │
│        ▼              ▼         │  │ProjectTabs │  │  │
│  ┌─────────────────────────┐   │  │none        │  │  │
│  │    Workspace Manager     │   │  │            │  │  │
│  │                         │   │  └────────────┘  │  │
│  │  ws1: MasterStack       │◄──┤                  │  │
│  │  ws2: Tabbed            │   └──────────────────┘  │
│  │  ws3: none              │                         │
│  └────────────┬────────────┘                         │
│               │                                      │
│  ┌────────────▼────────────┐                         │
│  │      IPC Server         │◄── Session Manager      │
│  │   (Unix Socket)         │◄── CLI commands         │
│  └─────────────────────────┘                         │
└─────────────────────────────────────────────────────┘
         │                    ▲
         │ sway IPC           │ sway events
         ▼                    │
    ┌─────────────────────────┐
    │         Sway            │
    └─────────────────────────┘
```

## 🔧 Running as a Service

From a checkout, one command builds, installs, writes the unit, enables it,
and restarts the daemon — then verifies the new build is actually the one
running:

```bash
just deploy
```

It is idempotent, so it is also the way to ship a change to a machine that
is already running tilekeeper. `just uninstall` reverses it. Installing from
a release binary instead? `tilekeeper install-service` writes the unit
(pointing at the installed binary), and you enable it yourself:

```bash
tilekeeper install-service
systemctl --user enable --now tilekeeper
```

The unit it writes looks like this:

```ini
# ~/.config/systemd/user/tilekeeper.service
[Unit]
Description=tilekeeper — a layout manager for Sway/Wayland
After=graphical-session.target

[Service]
ExecStart=%h/.local/bin/tilekeeper daemon
Restart=on-failure

[Install]
WantedBy=graphical-session.target
```

## Roadmap

Planned work and known gaps (config hot-reload, uniform command error
semantics, richer session-manager protocol) live in
[docs/ROADMAP.md](docs/ROADMAP.md).

## License

[Apache 2.0](LICENSE)
