# Handoff: `install-service` writes a unit that never starts at boot

**Status:** RESOLVED 2026-07-21 — the two tool-level defects are fixed in this
repo. The third (symlink clobber) is deliberately *not* a tool concern; it's a
host config matter (see below).
**Owner:** tilekeeper agent
**Severity:** high — every `just deploy` silently disabled the daemon at next boot.

## Resolution

The two defects that actually belong to the generator are fixed in
`cmd/tilekeeper/main.go`, with regression pins in
`cmd/tilekeeper/install_service_test.go` (each written red-first against the
broken template):

1. **Ordering cycle** — `serviceUnitContent` now emits `After=`/`WantedBy=`
   `sway-session.target` when that target exists (`swaySessionTargetPresent`
   probes `systemctl --user list-unit-files`), falling back to the generic
   `graphical-session.target` pattern otherwise. `PartOf=graphical-session.target`
   stays. Pinned by `TestServiceUnitNoOrderingCycle`.
2. **Frozen env** — the `Environment=` snapshot loop is gone; the unit carries no
   `Environment=` lines. Pinned by `TestServiceUnitNoFrozenSwaysock`.

Also: `ExecStart` renders as `%h/...` for a binary under `$HOME` (host-portable,
matches the README), and `README.md` now documents the corrected unit.

`install-service` writes its unit with a plain `os.WriteFile`, exactly as before
— no symlink/dotfiles awareness.

### On defect #3 (symlink clobber) — not the tool's job

The clobber happened because `~/.config/systemd/user/tilekeeper.service` was a
symlink into a dotfiles/rcm tree, so a plain write went *through* the link and
rewrote the tracked source. Teaching `install-service` to detect and refuse that
would bake host-specific dotfiles knowledge into a general-purpose tool — the
wrong layer. The generator installs the unit the normal way; whether that path
is a symlink into a config manager is the host's setup to decide.

The durable host fix is to stop tracking a generated file: let `install-service`
own `~/.config/systemd/user/tilekeeper.service` directly (not a dotfiles
symlink), so a `just deploy` writes the real file and there is nothing for
`git add -A` to clobber.

This bug lives entirely in the `install-service` unit generator, outside the
simulated sway event stream, so the fuzzer/sim harness has no reach here; the
regression coverage is the unit tests above (per the AGENTS.md note on scenarios
the fuzzer cannot reach).

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

**Fix (original proposal — NOT taken; see Resolution):** the handoff first
proposed making `writeServiceUnit` `os.Lstat` the target and refuse an
out-of-tree symlink. That was rejected as the wrong layer — the tool stays
dotfiles-agnostic and this is fixed host-side instead.

## Required changes

1. `cmd/tilekeeper/main.go:308-322` — template: `After=sway-session.target`,
   `WantedBy=sway-session.target`, keep `PartOf=graphical-session.target`, delete
   the `envBlock` interpolation.
2. `cmd/tilekeeper/main.go:297-306` — delete the env-snapshot loop and `envBlock`.
3. ~~`writeServiceUnit` refuses to follow an out-of-tree symlink.~~ NOT taken —
   dotfiles awareness is the wrong layer; `writeServiceUnit` stays a plain write.
   See Resolution.
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
- ~~**Symlink safety:**~~ dropped along with the symlink-refusal proposal (see
  Resolution). `writeServiceUnit` is a plain write with no symlink behavior to pin.

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

Now that the generator is fixed, `just deploy` writes the *correct* unit
(sway-session ordering, no frozen env). One host wart remains: while
`tilekeeper.service` is still a dotfiles symlink, the write lands on the dotfiles
source (the content is now correct, but a stray `git add -A` in dotfiles would
still track it). The durable host fix is to let `install-service` own
`~/.config/systemd/user/tilekeeper.service` directly rather than symlinking it
from dotfiles.

## Open questions — resolved

1. **Symlink handling:** neither refuse-and-print nor `.new`-and-diff — the tool
   does nothing symlink-specific. `install-service` writes its unit with a plain
   `os.WriteFile`. The dotfiles-symlink clobber is a host setup problem, fixed by
   not tracking the generated unit in dotfiles; encoding config-manager awareness
   in the generator is the wrong layer.
2. **`sway-session.target` assumption:** detect and fall back. The generator
   probes `systemctl --user list-unit-files sway-session.target` and only orders
   against it when present; otherwise it uses `graphical-session.target` for
   both `After=` and `WantedBy=` (cycle-safe, since no sway-session `Before` edge
   exists on such a host). A missing `systemctl` is treated as absent. The
   present/absent parse is unit-tested (`TestSwaySessionListed`); the thin
   shell-out wrapper is the only uncovered line.
