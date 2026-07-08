# Sway Model Verification

The harness sim (`internal/harness/sim`) is a hand-rolled in-memory approximation
of a real sway tree. Every behavior the sim implements is an **assumption**
about how real sway behaves. If the sim diverges, the fuzzer either misses real
bugs or hallucinates false ones.

This doc tracks every such assumption, the sway source that confirms (or refutes)
it, and the sim code that relies on it.

Reference sway checkout: `/workspace/.sway-ref` (gitignored shallow clone of
`swaywm/sway@main`).

## Conventions

Each entry:

- **Assumption**: one-line claim the sim bakes in.
- **Sim site**: file:line where we rely on it.
- **Sway source**: file:line in `.sway-ref/` that confirms (or refutes) it.
- **Status**: confirmed / refuted / pending / partial.
- **Notes**: caveats, edge cases, follow-ups.

---

## 1. Focus transfers to a sibling when the focused container is destroyed

- **Assumption**: When a focused leaf is closed, sway promotes another leaf
  in the same workspace to focused. The sim picks the first remaining leaf
  under the workspace.
- **Sim site**: `internal/harness/sim/sim.go` `CloseLeaf` (focus-transfer loop).
- **Sway source**: `sway/input/seat.c:240-319` (`seat_node_destroy` handler).
  Line 263: `needs_new_focus = focus && (focus == node || node_has_ancestor(...))`.
  Lines 274-295 walk up the tree looking for a `focus_inactive_view` sibling,
  falling back to the workspace itself if none.
- **Status**: confirmed.
- **Notes**: Real sway uses a per-seat focus-inactive stack (LRU). Our sim
  picks "first remaining leaf", which is strictly simpler but still satisfies
  the *something is always focused* invariant that the bug-A fix depends on.
  If a manager ever reads the *identity* of the new focus, we'll need to
  model focus-inactive properly. Today no manager does.

## 2a. Newly mapped view gets focus by default

- **Assumption**: when a new window appears, it becomes the focused leaf
  (unless criteria explicitly say otherwise).
- **Sim site**: `internal/harness/fuzz/generator.go` `genNew` — must mark
  the new leaf `Focused = true` and clear others on the workspace.
- **Sway source**: `sway/tree/view.c:945-957` (`view_map`).
  `should_focus(view)` defaults to true; focus moves via
  `input_manager_set_focus(&view->container->node)`.
- **Status**: confirmed. Sim updated to match.
- **Notes**: the IPC `window:new` event fires at line 903, *before* the focus
  change at 955. So managers reading the tree during their `window:new`
  handler still see the old focus. Today our managers do not depend on this
  ordering; if they ever did, we'd need to split the focus-update into a
  separate event.

## 2. `ipc_event_window("close")` fires BEFORE the tree mutation

- **Assumption**: managers handling `window::close` still see the destroyed
  container's original `.Parent` / siblings.
- **Sim site**: `internal/harness/fuzz/fuzz.go` — calls `hub.HandleEvent(ev)`
  before `s.CloseLeaf(ev.Container)`.
- **Sway source**: `sway/tree/container.c:475-505` (`container_begin_destroy`).
  Line 477 fires the IPC event before `container_detach` at line 504.
- **Status**: confirmed.

## 3. Singleton intermediate containers auto-flatten on child destroy

- **Assumption**: when a container is reduced to one child, that child takes
  its slot; the empty wrapper is destroyed. Walks up recursively.
- **Sim site**: `internal/harness/sim/apply.go` `cascadeFlatten`.
- **Sway source**: `sway/tree/container.c:526-538` (`container_flatten`).
  Walks while `children->length == 1`, calling `container_replace` then
  `container_begin_destroy` on the old parent, then steps up.
- **Status**: confirmed.
- **Notes**: sway skips flattening containers that *have a view* (line 527).
  Our sim's structural containers (type=con with children) never have a
  view, so the check is moot for us — but if we ever fabricate view-bearing
  containers in fixtures, double-check.

## 4. Empty (zero-child) intermediate containers are destroyed

- **Assumption**: a non-workspace container with zero children gets pruned.
- **Sim site**: `internal/harness/sim/apply.go` `cascadeFlatten` (len==0 branch).
- **Sway source**: `sway/tree/container.c:510-524` (`container_reap_empty`).
  Walks up parents; for each empty, non-workspace, non-view container,
  calls `container_begin_destroy`.
- **Status**: confirmed.
- **Notes**: sway also reaps empty *workspaces* via `workspace_consider_destroy`
  (line 522). We deliberately do NOT destroy workspaces on empty — the
  fuzzer needs stable workspace identity across close events.

## 4b. Layout JSON field: input "stacking" serializes as "stacked"

- **Assumption**: the `layout` field on a sway node in IPC JSON uses the
  string `"stacked"` for an L_STACKED container, even though the command
  parser accepts `"stacking"` as input.
- **Sim site**: `internal/harness/sim/apply.go` `cmdLayout` — normalizes
  input `"stacking"` to stored `"stacked"`.
- **Sway source**:
  - `sway/commands/layout.c:18-19` — `strcasecmp(s, "stacking") == 0`
    → `L_STACKED` (command parser accepts "stacking").
  - `sway/ipc-json.c:55-56` — `case L_STACKED: return "stacked";`
    (IPC serializer emits "stacked").
- **Status**: confirmed. Sim updated 2026-04-19 after the fuzzer found that
  `IsExcluded` (which checks `parent.Layout == "stacked"`) disagreed with
  sim-stored `"stacking"`, causing 4000+ false "tracked-matches-leaves"
  violations per 50-seed sweep.
- **Notes**: this is a real trap — the command language (user-facing) and
  the IPC shape (tool-facing) use different spellings. Any new sim code
  that reads or writes `Layout` must use `"stacked"` not `"stacking"`.

## 5. `split none` on a node with siblings is rejected by sway

- **Assumption**: `split none` aka flatten is only valid on a container that
  has no siblings.
- **Sim site**: `internal/harness/sim/apply.go` — returns `ErrFlattenSiblings`
  wrapped as `ErrSwayRejected`.
- **Sway source**: `sway/commands/split.c:35-50` (`do_unsplit`).
  Line 39: `if (con && con->pending.parent && con->pending.parent->pending.children->length == 1)`
  — flatten only proceeds when the parent has exactly one child (i.e. the
  target has no siblings). Line 42 returns the literal
  `"Can only flatten a child container with no siblings"`.
- **Status**: confirmed.
- **Notes**: our `ErrFlattenSiblings` string matches sway's literal, so log
  scraping against real sway output will continue to match.

## 6. `workspace:init` event fires when a workspace is first created

- **Assumption**: the workspace tree node exists by the time managers see
  `workspace:init` — they can GetTree and find the new workspace.
- **Sim site**: `internal/harness/fuzz/generator.go` `initWorkspace` — mutates
  tree then returns the event.
- **Sway source**: `sway/tree/workspace.c:177-269` (`workspace_create`).
  Line 258 calls `output_add_workspace(output, ws)` which wires the new
  workspace into the tree; line 268 then fires
  `ipc_event_workspace(NULL, ws, "init")`. The workspace is already
  GetTree-visible when the init event reaches subscribers.
- **Status**: confirmed.

## 7. Compound commands (`a, b, c`) propagate the scope prefix

- **Assumption**: `[con_id=N] a, b, c` applies `[con_id=N]` to every segment,
  not just the first.
- **Sim site**: `internal/harness/sim/sim.go` `splitCompound`.
- **Sway source**: `sway/commands.c:205-329` (`execute_command`).
  Line 233 scopes the criteria parse to `matched_delim == ';'`; the
  `using_criteria` flag and `containers` list are only (re)set on `;`
  boundaries. `argsep(&head, ";,", ...)` at line 254 splits on either,
  so a leading `[scope]` set on the first segment carries across every
  `,`-separated sub-command until the next `;` resets it.
- **Status**: confirmed.
- **Notes**: sway treats `;` as scope-reset, `,` as scope-propagate. Our
  `splitCompound` only handles `,` and assumes callers pre-split on `;` —
  that matches how the layout managers emit commands today (we never issue
  `;`-chained commands with different scopes).

## 8. `mark --add NAME` creates the mark even if NAME already maps elsewhere

- **Assumption**: re-marking an existing name rebinds it (the old mark is
  dropped). Sim's `marks[name] = id` overwrite behavior.
- **Sim site**: `internal/harness/sim/apply.go` mark handler.
- **Sway source**: `sway/commands/mark.c:46-58`.
  Line 54 calls `container_find_and_unmark(mark)` *unconditionally* —
  even for `--add`. `container_find_and_unmark`
  (`sway/tree/container.c:1582`) does a global `root_find_container` and
  removes the name from whichever container currently holds it. Only
  then does line 57 `container_add_mark(container, mark)` bind it to
  the scope target. Net effect: a mark name is globally unique and
  `--add` rebinds.
- **Status**: confirmed. Sim's map-overwrite matches.
- **Notes**: Bug E (mark --add on vanished con_id) depended on scope
  resolving to a real node; this entry covers the rebind semantics only.

---

## 9. `split none` restores the wrapper's percent (verified live)

- **Assumption**: `split none` destroys the single-child wrapper and the
  survivor INHERITS the wrapper's slot geometry (percent + rect), not 1.0.
- **Sim site**: `internal/harness/sim/apply.go` `flattenSplit` (sets
  `target.Percent = p.Percent`, `target.Rect = p.Rect`).
- **Sway source**: `sway/tree/container.c` `container_replace` /
  `container_flatten` copy `width_fraction` onto the replacement.
- **Status**: confirmed live (sway 1.11, `cmd/sway-difftest`
  `split-none-flatten`). `splitv` on a 50% leaf → wrapper takes the 50% slot,
  child becomes 100% of wrapper; `split none` → survivor inherits 50%. Sim
  fixed to match.

---

## 10. Floating a tiled window rescales the remaining tiled row (verified live)

- **Assumption**: floating a tiled window (or move-to-mark onto a floating
  dest) removes it from the split row; sway rescales the survivors to sum to
  1.0 proportionally (three 0.33 → two 0.5). The floated node's own percent
  becomes an arbitrary output fraction and is layout-irrelevant.
- **Sim site**: `internal/harness/sim/apply.go` `rescaleTiledRowToFull`,
  called from `cmdFloating` enable and `moveToMark` floating-dest.
- **Sway source**: sway `arrange` re-normalizes the tiled row after removal.
- **Status**: confirmed live (sway 1.11, `cmd/sway-difftest`
  `move-to-mark-floating-{src,dest}`). Sim fixed to match.

---

## 11. Insert-arrange percent is NOT modeled by the sim (KNOWN GAP)

- **Assumption (deliberately NOT modeled)**: re-tiling a window (floating
  disable, move-in) makes sway redistribute the row — the newcomer gets 1/N
  and existing children scale by (N-1)/N (verified live: a 70/30 row + 3rd
  window → 0.466/0.20/0.333, NOT an equalize).
- **Sim site**: intentionally absent in `apply.go`. The sim leaves
  insert-time percents to the layout manager.
- **Sway source**: sway `arrange` eager redistribution on insert.
- **Status**: KNOWN GAP. Modeling it in `apply.go` regressed the master-width
  fuzzer invariant ~40x (because MasterStack owns the 75% master on insert),
  so it was reverted. Tracked loudly by the `floating-toggle` KnownGap
  scenario in `cmd/sway-difftest`. The correct fix is layout managers
  re-asserting their intended percents post-insert, or a master-aware sim
  special-case.

---

## Follow-ups

- Trace `move to workspace <self>` to see if sway fires a `window:move`
  event when the workspace doesn't change (fuzzer genMove assumes yes).
- Trace `layout MasterStack` → tree shape diff (Bug D: tracked-matches-
  leaves drift).
