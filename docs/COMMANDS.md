# tilekeeper commands

One command language, three transports. The **exact same string** works as a
Sway keybinding, from the CLI, and over the IPC socket — like `swaymsg`.

```
bindsym $mod+Return nop tilekeeper swap-master     # keybinding
tilekeeper msg swap-master                         # CLI
{"command": "swap-master"}                         # IPC
```

**Token rule:** spaces separate a verb from its argument (`focus left`,
`master grow`, `layout none`); hyphens join only irreducible atomic actions
(`swap-master`, `side-toggle`).

## Actions

Bindable (`nop tilekeeper <action>`) and CLI (`tilekeeper msg <action>`). An
optional target workspace: `… workspace <name>` in a binding, `--workspace
<name>` on the CLI (defaults to the focused workspace).

| Action | Meaning |
|---|---|
| `swap-master` | promote the focused window to master; the old master becomes the top of the stack (MRU / alt-tab order) |
| `focus <left\|right\|up\|down>` | move focus directionally |
| `focus <master\|previous>` | focus the master / the previously focused window |
| `move <left\|right\|up\|down>` | move the focused window |
| `rotate <cw\|ccw>` | rotate windows through the layout |
| `master <grow\|shrink>` | widen / narrow the master column |
| `master <add\|remove>` | add / remove a master slot (count) |
| `stack toggle` | cycle the stack's inner layout (splitv → splith → stacking → tabbed) |
| `stack side-toggle` | flip the stack to the other side of the master |
| `maximize` | toggle fullscreen-maximize of the focused window |
| `layout <name>` | switch the workspace's layout (`layout none` disables) |

### ProjectTabs layout (session-manager integration)

`toggle-split` and `focus <terminal|browser>` bind like any other action. The
`project` commands are driven by the session manager over IPC — they carry
explicit window ids, so they aren't typically hand-bound.

| Action | Meaning |
|---|---|
| `toggle-split` | toggle the terminal/browser split orientation |
| `focus <terminal\|browser>` | focus the terminal / browser pane |
| `project add <name> <terminal_id> [browser_id]` | register a project group |
| `project remove <name>` | remove a project group |
| `project set-browser <name> <browser_id>` | attach a browser window to `<name>` |

## Queries & lifecycle (CLI only)

Not bindable — a `nop` binding is fire-and-forget and can't return output.

| Command | Meaning |
|---|---|
| `tilekeeper status` | per-workspace layout + tracked windows (JSON) |
| `tilekeeper daemon` | run the daemon (foreground) |
| `tilekeeper doctor` | environment / health check |
| `tilekeeper version` | version, commit, build info |
| `tilekeeper install-service` | write the systemd user unit |

## Example Sway config

```
exec tilekeeper daemon

bindsym $mod+Return       nop tilekeeper swap-master
bindsym $mod+j            nop tilekeeper focus down
bindsym $mod+k            nop tilekeeper focus up
bindsym $mod+Shift+j      nop tilekeeper move down
bindsym $mod+h            nop tilekeeper master shrink
bindsym $mod+l            nop tilekeeper master grow
bindsym $mod+bracketright nop tilekeeper rotate cw
bindsym $mod+space        nop tilekeeper maximize
bindsym $mod+s            nop tilekeeper stack toggle
bindsym $mod+F1           nop tilekeeper layout MasterStack
bindsym $mod+F2           nop tilekeeper layout none
```
