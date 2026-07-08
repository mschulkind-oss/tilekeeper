# Roadmap

Post-open-source ideas and known gaps. A living document, not a set of
commitments — priorities shift with real use.

## Config hot-reload

There is currently **no way to reload configuration in a running daemon**. The
`reload` command and the "hot reload / SIGHUP" claim were removed before release
because neither was implemented (no IPC handler, no signal handler) — better to
ship an honest surface than a broken command.

**Plan:** on `tilekeeper reload` (and on `SIGHUP`), re-read the config file,
diff it against the live config, and reconfigure the running `Hub` — swap
per-workspace managers whose layout changed, apply new master-width / stack
settings, and re-arrange affected workspaces. Needs a `Hub.Reconfigure(cfg)`
that is careful about in-flight tracking state. Until then, restart the service
to pick up config changes (`systemctl --user restart tilekeeper`).

## Uniform command error semantics

The three layout managers disagree on unknown commands: **MasterStack returns an
error**, while **Tabbed and ProjectTabs silently no-op**. This is two reasonable
behaviors colliding:

- A *gibberish* command (`tilekeeper msg bogus`) should **error** — visibly.
- A *valid verb that doesn't apply to the current layout* (e.g. `swap-master`
  while focused on a Tabbed workspace) should **no-op** — so cross-layout
  keybindings don't spam errors.

The managers don't distinguish these, so each conflates them one way. The
fuzzer's `no-handler-error` invariant deliberately depends on the no-op behavior
(a valid binding must never error in production).

**Plan:** a central command registry the `Hub` validates against once — unknown
verbs error uniformly across every layout; known-but-unhandled verbs no-op.
Removes the per-manager inconsistency without weakening the invariant. Until
then, `tilekeeper msg <bogus>` may report success on a Tabbed/ProjectTabs
workspace.

## Richer ProjectTabs ↔ session-manager protocol

`project add` / `project set-browser` currently take explicit window ids and are
driven by the session manager over IPC. `docs/project-tabs-layout.md` sketches a
fuller protocol (structured registration, browser auto-matching). Reconcile the
implementation with that design, or trim the design to match, once the
session-manager integration is exercised end-to-end.

## More layouts, custom layouts, and snapshot capture/restore

Shipping today: **MasterStack, Tabbed, ProjectTabs, none**. The README describes
more that aren't selectable yet — the groundwork exists but isn't wired up:

- **Grid, Columns, Spiral (autotiling), DualTabbed** — `LayoutSpec`s exist in
  `internal/layout/builtin.go`, but `createManager` (in `internal/workspace`)
  only builds MasterStack/Tabbed/ProjectTabs/none, so `layout Grid` etc. return
  "unknown layout". Wire the built-in specs to a generic spec-driven manager.
- **User-defined custom layouts** — the declarative container/slot spec model
  exists (`internal/layout/spec.go`), but there's no config key or command to
  load a user-supplied spec at runtime. Add one (e.g. a config path, or
  `layout <file>`).
- **Snapshot capture/restore** — `CaptureSnapshot` exists
  (`internal/layout/snapshot.go`) but isn't wired to a `capture`/`restore` CLI
  or IPC command. Expose it once the spec-driven managers land (restore needs a
  target layout to place windows into).
