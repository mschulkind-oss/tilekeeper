# Handoff: `install-service` writes a unit that never starts at boot

**Status:** host-side workaround applied 2026-07-21; the generator in this repo is still broken.
**Owner:** tilekeeper agent
**Severity:** high — every `just deploy` silently disables the daemon at next boot.

## Symptom

After a `just deploy`, tilekeeper runs for the rest of that session. On the next
boot it is `inactive (dead)` with no failure and no process, and the user's
windows are unmanaged. `systemctl --user status tilekeeper` shows:

```
tilekeeper.service: Found ordering cycle: graphical-session.target/verify-active
  after sway-session.target/start after tilekeeper.service/start - after graphical-session.target
tilekeeper.service: Job tilekeeper.service/start deleted to break ordering cycle
  starting with tilekeeper.service/start
```

Note the failure mode: **the start job is deleted, not failed.** The unit stays
`enabled`, nothing is logged as an error, and there is no restart. It just never
runs. This is why it went unnoticed from 2026-07-16 to 2026-07-21.

## Root cause

Three defects in the unit template at `cmd/tilekeeper/main.go:308-322`.

### 1. `graphical-session.target` closes an ordering cycle

The generated unit uses `After=graphical-session.target` and
`WantedBy=graphical-session.target`. On this host (and on any sway setup using
the standard `sway-session.target` pattern) that produces a cycle:

| Edge | Source |
|---|---|
| `sway-session.target` after `tilekeeper.service` | targets implicitly gain `After=` on everything they `Wants=`, per `systemd.target(5)` |
| `tilekeeper.service` after `graphical-session.target` | the generated `After=` line |
| `graphical-session.target` after `sway-session.target` | `sway-session.target` declares `Before=graphical-session.target` |

systemd breaks the loop by deleting a job, and the job it picks is
tilekeeper's.

**Fix:** generate `After=sway-session.target` and `WantedBy=sway-session.target`.
`PartOf=graphical-session.target` is correct and should stay — it's what stops
the daemon when the session ends.

This is the documented convention for every sway service on this host; see
`~/projects/sysadmin/AGENTS.md`, "Sway Services → systemd User Units", which
calls out this exact anti-pattern:

> **DO NOT** use `After=graphical-session.target` for services in
> `sway-session.target.wants` — this creates an ordering cycle that silently
> drops the service at boot.

Sibling units (`cliphist.service`, `sway-transparency.service`) are the
reference shape.

### 2. The sway env block freezes a dead PID

`cmd/tilekeeper/main.go:299` snapshots `SWAYSOCK`, `WAYLAND_DISPLAY`,
`XDG_RUNTIME_DIR` and `DISPLAY` out of the *installing process's* environment
into literal `Environment=` lines. `SWAYSOCK` contains the sway PID:

```ini
Environment=SWAYSOCK=/run/user/1000/sway-ipc.1000.2353.sock
```

PID 2353 was dead by the next boot (the live socket was `...2129.sock`). So even
with the cycle fixed, the daemon would start against a stale socket path.

**Fix:** drop the `Environment=` block entirely. Sway already pushes these into
the systemd user manager's environment via
`dbus-update-activation-environment --all --systemd`, so a unit ordered after
`sway-session.target` inherits the correct, live values. Verified after the
host-side fix — the daemon came up on `...2129.sock` with no `Environment=`
lines in the unit.

`internal/sway/conn.go:72-79` already has socket-discovery fallback for exactly
this "stale env" case; the generator shouldn't be manufacturing the staleness it
then has to work around.

### 3. `os.WriteFile` clobbers through an rcm symlink

`writeServiceUnit` (`cmd/tilekeeper/main.go:383`) is `os.WriteFile`, which
follows symlinks. On this host `~/.config/systemd/user/tilekeeper.service` is a
symlink into `~/.dotfiles` (managed by rcm), so `install-service` wrote *through*
the link and silently overwrote the tracked dotfiles source. The corrupted unit
was then picked up by a routine `git add -A` in dotfiles commit `bfa97cc`,
reverting a fix that had been in place since `26e02b5`.

**Fix:** before writing, `os.Lstat` the target. If it is a symlink pointing
outside `~/.config/systemd/user`, do not write — print the resolved path and the
unit content and tell the user to update the source themselves. A config
manager's file is not ours to overwrite.

## Required changes

1. `cmd/tilekeeper/main.go:308-322` — template: `After=sway-session.target`,
   `WantedBy=sway-session.target`, keep `PartOf=graphical-session.target`, delete
   the `envBlock` interpolation.
2. `cmd/tilekeeper/main.go:297-306` — delete the env-snapshot loop and `envBlock`.
3. `cmd/tilekeeper/main.go:383` — `writeServiceUnit` refuses to follow an
   out-of-tree symlink.
4. `README.md:454-466` — the documented unit shows the broken form. Update it.
5. Consider generating `ExecStart=%h/.local/bin/tilekeeper daemon` rather than an
   absolute resolved path when the binary is under `$HOME` — cosmetic, but it
   keeps the unit host-portable. `installedBinaryPath()` semantics and
   `TestInstalledBinaryPath` should be preserved either way.

## Tests

TDD, please — write each of these red first:

- **Ordering:** assert the generated unit contains `After=sway-session.target`
  and `WantedBy=sway-session.target`, and contains neither
  `After=graphical-session.target` nor `WantedBy=graphical-session.target`.
  Name it for the bug, not the assertion — this is a regression pin.
- **No frozen env:** with `SWAYSOCK` set in the test env, assert the generated
  unit contains no `Environment=SWAYSOCK=` line.
- **Symlink safety:** point the target path at a symlink into another temp dir,
  run the write, assert the symlink is intact and the far-side file is unchanged.

`cmd/tilekeeper/install_service_test.go` is the right file; it already pins two
prior install-service regressions and explains each in a comment. Match that
style. Note that `planServiceWrite` returns `serviceUpdated` for a changed
template, so a fixed generator will correctly rewrite existing bad units on the
next deploy — that path is worth an assertion too.

## Host state (already done — do not redo)

- `~/.dotfiles/config/systemd/user/tilekeeper.service` restored to the sway-session
  convention; committed as `07d3f23` in the dotfiles repo.
- Stale enablement cleared: the unit had been enabled into *both*
  `graphical-session.target.wants/` and `sway-session.target.wants/` (the second
  left over from the pre-clobber `[Install]`). It is now in `sway-session.target.wants/`
  only.
- Daemon verified running against the live socket, IPC listening, 22 windows
  arranged on workspace 8.

The host is fixed but **not durable**: `just deploy` re-runs `install-service`,
which will rewrite the dotfiles source through the symlink and reintroduce all
three defects. Until this repo is fixed, `just deploy` is unsafe on this host.

## Open questions

1. Should `install-service` refuse to run at all when the target is a symlink, or
   write to a `.new` sibling and print a diff? The refuse-and-print option is
   simpler and hard to get wrong.
2. Is `sway-session.target` a safe assumption for all users, or should the
   generator detect it (`systemctl --user list-unit-files sway-session.target`)
   and fall back to `graphical-session.target` only when it's absent? Detection
   is more correct; a flag is more predictable.
