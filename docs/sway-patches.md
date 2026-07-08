# Patching sway: diagnostic logging and candidate upstream fixes

Purpose: track bugs in sway that tilekeeper bumps into, with enough
detail to (a) run a locally-patched sway to observe them live and
(b) submit clean upstream patches when a root cause is confirmed.

Reference sway checkout for line numbers: `.sway-ref/` (shallow
`swaywm/sway@main` clone, gitignored).

---

## 1. Silent no-op in `workspace_switch` when focus-inactive is obstructed by fullscreen

### Symptom

`swaymsg workspace number 7` returns `[{"success": true}]` but does not
switch the visible workspace. Observed 2026-04-20 on host `terrapin`
after `just deploy` restarted tilekeeper while a Chromium window was
fullscreened on ws7.

State at time of repro:

- `DP-3.current_workspace = "8"` (never changes across repeated attempts)
- `get_workspaces` MRU list keeps ws7 at the end (least-recently focused)
- Every other workspace switches normally via the same command
- `swaymsg -r workspace number 7` → `[{"success": true}]`

ws7 tree at repro: `H[H[Chromium H[Chromium V[Chromium Chromium
S[7×Chromium]]]]]`. The top-level Chromium leaf `[30]` has
`fullscreen_mode=1`. The rest of the subtree contains ten non-fullscreen
Chromium leaves, one of which was the last-focused before the fullscreen
was engaged.

### Code path (sway main as of reference checkout)

1. `sway/commands/workspace.c:236` — `cmd_workspace` resolves argv to
   a workspace and calls `workspace_switch(ws)`, then returns
   `CMD_SUCCESS` unconditionally at line 239.
2. `sway/tree/workspace.c:731-743` — `workspace_switch`:
   ```c
   struct sway_node *next = seat_get_focus_inactive(seat, &workspace->node);
   if (next == NULL) next = &workspace->node;
   seat_set_focus(seat, next);
   arrange_workspace(workspace);
   return true;
   ```
   `seat_get_focus_inactive` returns the last-focused descendant — in
   our repro, a non-fullscreen sibling of the fullscreen container, not
   the fullscreen container itself.
3. `sway/input/seat.c:1130-1153` — `seat_set_workspace_focus`:
   ```c
   // Deny setting focus to a view which is hidden by a fullscreen container or global
   if (container && container_obstructing_fullscreen_container(container)) {
       return;
   }
   ```
   Early return. No log. No event. No state change.
4. `sway/tree/container.c:570-590` — the obstruction predicate returns
   `workspace->fullscreen` whenever the target is neither the fullscreen
   container itself nor a descendant of it.

Net effect: `workspace_switch` returns `true`, `seat_set_focus` silently
did nothing, and the caller chain reports success all the way back to
the IPC client.

### Why this only hits ws7 in our setup

The seat's focus-inactive pointer for a workspace survives across
fullscreen transitions and layout reshuffles. When tilekeeper's
`arrangeAll` runs on restart, it can move focus-inactive to a
non-fullscreen leaf while the fullscreen container stays in place. ws7
is the only workspace where (a) a container was fullscreened at deploy
time and (b) the deploy-time rearrangement landed focus-inactive on a
sibling rather than on the fullscreen container.

### Minimum-touch escape (no patch required)

`swaymsg '[con_id=<fullscreen_id>] focus'` targets the fullscreen
container directly — `container_is_fullscreen_or_child` is true for it,
so the obstruction check returns NULL and the focus change goes through.
Practically: find the `fs=1` leaf in the stuck workspace via
`swaymsg -t get_tree` and focus its con_id.

### Proposed diagnostic patch (drop-in, low risk)

Add a single `SWAY_DEBUG` log at the silent-return site so operators can
see why a `workspace` command no-op'd:

```c
// sway/input/seat.c, inside seat_set_workspace_focus near line 1151:
if (container && container_obstructing_fullscreen_container(container)) {
    sway_log(SWAY_DEBUG,
        "refusing focus of con %p (%s) — obstructed by fullscreen con %p (%s)",
        container, container->title ?: "(no title)",
        container_obstructing_fullscreen_container(container),
        container_obstructing_fullscreen_container(container)->title ?: "(no title)");
    return;
}
```

This alone would have told us the cause immediately from sway's journal.

### Proposed behavior patch (upstream candidate)

`workspace_switch` should not hand a focus target that is known to be
obstructed. Two reasonable fixes:

**Option A — redirect to the fullscreen container.** Before calling
`seat_set_focus`, check whether the target is obstructed and, if so,
swap in `workspace->fullscreen`. This matches user intent: switching to
a workspace with a fullscreen window should show (and focus) that
fullscreen window.

```c
// sway/tree/workspace.c, around line 736:
struct sway_node *next = seat_get_focus_inactive(seat, &workspace->node);
if (next == NULL) next = &workspace->node;
if (workspace->fullscreen && next->type == N_CONTAINER) {
    struct sway_container *c = next->sway_container;
    if (!container_is_fullscreen_or_child(c)) {
        next = &workspace->fullscreen->node;
    }
}
seat_set_focus(seat, next);
```

**Option B — have `seat_get_focus_inactive` skip obstructed nodes.**
More invasive; affects every caller of the helper. Option A is the
narrow, intent-preserving fix.

Upstream-submission notes: include the repro recipe above (fullscreen +
layout-reshuffle → stuck workspace). Maintainers will likely want the
diagnostic log landed separately from the behavior fix.

---

## 2. Instrumentation patches (general diagnostics)

These are "silent failure" sites we've hit or expect to hit. Each one
is a two-line `sway_log(SWAY_DEBUG, ...)` addition; together they make
the journal much more useful when debugging from tilekeeper's side.

| Site | File:line | What to log |
|---|---|---|
| Obstructed focus (above) | `sway/input/seat.c:1151` | source + fullscreen container identity |
| `split none` rejection | `sway/commands/split.c:42` | con_id, parent's child count, sibling ids |
| `move to workspace <self>` | `sway/commands/move.c` (find the self-check) | con_id, ws_name — today this is a silent no-op |
| `seat_set_focus` no-op when `last_focus == node` | `sway/input/seat.c:1132` | con_id; helps distinguish "already focused" from "refused" |

Log these at `SWAY_DEBUG` so they cost nothing in production and show
up with `SWAYSOCK=... sway -d` or systemd's debug level.

---

## 3. Build and deploy a patched sway locally

Placeholder — fill in once the user picks a path. Options:

- **Arch AUR `sway-git`**: clone the PKGBUILD, apply patches as
  `source` entries, `makepkg -si`. Easy rollback via `pacman -U` of the
  vendored package. Most convenient for day-to-day; matches the host
  distro on `terrapin`.
- **Out-of-tree build**: `meson setup build && meson compile -C build`
  from the sway source, install to `/opt/sway-patched/bin/sway`,
  override the session exec line. Keeps the distro sway untouched.
- **Nix overlay**: if we migrate `terrapin` to NixOS at some point,
  carry the patches as an overlay. Not relevant today.

Whichever path: keep patches as separate `.patch` files in
`docs/sway-patches/` so they can be cleanly rebased against upstream
and submitted as individual PRs.

---

## 4. Upstream submission queue

- [ ] Diagnostic log at the obstructed-focus return (§1) — send first,
      no behavior change, easy to land.
- [ ] Behavior fix for obstructed workspace switch (§1 Option A) —
      depends on maintainer preference; include repro.
- [ ] Diagnostic logs for the other silent sites (§2) — bundle as one
      PR once we've verified each at least once.

Track PR URLs here when filed.
