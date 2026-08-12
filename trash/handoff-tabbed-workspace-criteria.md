# Handoff: Tabbed is built on a command that does not do what it looks like

**Opened**: 2026-08-12, while fixing the ws8 "move left pops a tab out of the
strip" bug. That bug is fixed (`layout.Tabbed.moveStaysInStrip`). This doc
covers what the fix uncovered and deliberately did not touch.

## The finding

`Tabbed.ensure` makes a workspace tabbed with:

```
[workspace=8] layout tabbed
```

Sway criteria match **views**, never containers and never workspaces
(`sway/criteria.c`). So that command runs `layout tabbed` once per *window* on
ws8, and `cmd_layout`, finding no container parent above a workspace-direct
window, wraps every workspace child in a **new tabbed container**
(`workspace_wrap_children`, `sway/tree/workspace.c:898-910`). The workspace
container itself stays `splith`.

Measured live against headless sway (`cmd/sway-difftest`,
`workspace-criteria-layout`):

```
sim                          sway
workspace tabbed             workspace splith
  win  win  win                con tabbed
                                 win  win  win
```

Two things follow.

**1. `flattenToTabs` is chasing an unreachable shape.** It works to make every
leaf a *direct child of the workspace* ("flat tab strip"). With `ensure`'s own
command that can never happen: the windows live one level down, inside the
wrapper. On a healthy workspace this is harmless — the wrapper fills the
workspace and sway draws exactly the tab bar the user wants — but every piece
of code that reasons about "direct tab child" is reasoning about the wrong
tree.

**2. The sim is hiding a real Tabbed bug.** `internal/harness/sim` resolves a
`[workspace=N]` scope to the workspace *node* and sets its layout, so the
fuzzer sees the flat shape production never has. Teaching the sim the real
criteria semantics (a ~20-line change in `sim.apply`, tried and reverted on
2026-08-12) immediately turns `TestTabbedFlatten_ContainerMoveIn` red:

```
workspace splith
  con tabbed          <- original strip
    win win win
  con tabbed          <- second strip, created by ensure on the move-in
    win
    con tabbed        <- nested tabs
      win win win
```

A window that arrives while focus is outside the strip lands beside it, and
the next `ensure` wraps *that* pair into a second tabbed container instead of
merging it into the existing strip.

## The fix, when someone takes it

Do all three together — the sim change alone just turns the suite red:

1. **Tabbed targets the strip, not the workspace.** Find (or create) the
   single tiled container holding the tabs and address it by `con_id`:
   `[con_id=<strip>] layout tabbed`. Creating it from a flat workspace is the
   same `layout tabbed` on one window; joining a stray window to an existing
   strip is `move window to mark` onto any tab, exactly as `flattenToTabs`
   already does for nested leaves.
2. **Redefine flat.** "Every leaf is a direct child of *the strip*", not of the
   workspace. `checkTabbedStripFlat` (`internal/harness/fuzz/fuzz.go`) already
   accepts both shapes and can be narrowed to the real one once production
   guarantees it.
3. **Model the criteria in the sim** (`sim.apply`, see the KNOWN DIVERGENCE
   note there) and drop the `workspace-criteria-layout` KnownGap.

Related open items surfaced at the same time, both recorded in
`internal/harness/fuzz/floors.json`:

- **`tabbed-strip-flat` floor 481.** Switching a chaotic MasterStack workspace
  to tabbed leaves the windows nested: `flattenToTabs` needs a
  workspace-direct leaf to anchor its `move to mark` lift, and bails when
  every leaf is nested and `FlattenSingletons` cannot make progress. Needs an
  anchor of last resort.
- **`no-wrapper-chain` floor 906.** The hub's `nativeSwayFallback` forwards a
  scopeless `move left` for an *unmanaged* workspace, and sway applies it to
  whatever is focused — possibly a window on a different, managed workspace,
  restructuring it behind that manager's back. Guarding the fallback on "the
  named workspace actually holds focus" measured 906 → 624 on the reference
  sweep.

## Verification tools

- `go run ./cmd/sway-difftest` — spawns headless sway; scenarios
  `workspace-criteria-layout`, `move-dir-promote-out-of-strip`,
  `move-dir-reorient-workspace` cover this area.
- `go run ./cmd/fuzz-gate` — invariant floors; `tabbed-strip-flat` is the
  class that owns "the strip is intact".
- `docs/sway-model-verification.md` §12 and §13 record the underlying sway
  behaviors with source references.
