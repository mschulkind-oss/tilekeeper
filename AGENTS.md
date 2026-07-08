# General Development Instructions

Welcome to **tilekeeper** — a Sway/Wayland layout manager written in Go.

## Project Structure

- `cmd/tilekeeper/` — Binary entry point (daemon, CLI commands, doctor, install-service)
- `internal/config/` — TOML config parsing
- `internal/daemon/` — Main daemon event loop (sway events + IPC server)
- `internal/ipc/` — Unix socket IPC server and client
- `internal/layout/` — Layout engine interface + MasterStack implementation
- `internal/sway/` — Sway IPC client (binary protocol, event subscription)
- `internal/workspace/` — Workspace Hub (event routing, nop command parsing, IPC handler)
- `docs/` — Documentation (session-manager integration, design)
- `trash/` — Deleted files (safety net)
- `scratch/` — Local working notes (gitignored)

## Core Tools

- **mise**: Tool manager (Go, just)
- **just**: Command runner (see `justfile`)
- **go**: Build toolchain

## Best Practices

- **Verification**: Always run `just check` after changes. This runs format, lint, and tests.
- **Safety**: **NEVER** use `rm`. Move files to `trash/` instead.
- **Regression Testing**: Always keep tests used to fix bugs. Integrate them into the permanent suite.
- **README-driven**: The README describes the desired behavior. Implementation should match the README.

## Testing Standards

- **Coverage**: Maintain **80%+ test coverage** in each file and overall.
- **TDD**: Write tests first (Red → Green → Refactor).
- **SOLID**: Single responsibility, dependency injection via interfaces.
- **Low Cyclomatic Complexity**: Extract helpers, avoid deep nesting.
- **DRY**: Factor out common patterns into helpers (e.g., `sway.CreateWorkspace`, `sway.CreateWindow`).
- **Integration tests**: Use `just test-integration` for tests requiring sway.
- **Mock strategy**: Use `sway.Mock` for unit tests. Real sway connection for integration tests only.

## TDD Workflow

1. **Red**: Write a failing test for the new functionality.
2. **Green**: Write the minimum code to pass.
3. **Refactor**: Clean up while keeping tests green.

**Bug Fixing — start from the fuzzer:**

The property-based fuzzer at `internal/harness/fuzz` and the in-memory
Sway model at `internal/harness/sim` exist so that regressions get caught
before they reach production. When a bug turns up — whether from a user
report, a crash, or your own review — resist the urge to jump straight to
the fix. Walk through this order instead:

1. **Ask why the fuzzer missed it.** Which invariant *should* have caught
   this? If the fuzzer would have flagged it with an existing invariant,
   that's a generator-coverage gap. If no invariant even describes the
   bad behavior, that's an invariant gap. Usually it's both.
2. **Close the detection gap first.** Add the missing invariant (either
   in the sim as an `ErrSwayRejected` for bad commands, or in
   `checkInvariants`/`checkTrackedMatchesLeaves` for bad states), or
   widen the generator so the bug's scenario is reachable. Confirm the
   fuzzer now fails — the fix-first-verify-later pattern is how bugs
   come back.
3. **Lift a failing seed to a unit test.** `trace_test.go` has helpers
   for tracing a seed at a step; use them to minimize the scenario, then
   encode it as a regression test in the relevant `*_test.go`. The test
   must fail on the unfixed code.
4. **Fix production.** Only now change `internal/layout/...`, etc.
5. **Verify (invariant gate).** Unit test passes, `just check` is green,
   and `just fuzz-gate` (`go run ./cmd/fuzz-gate`) is GREEN. The gate is the
   authority, not a manual count comparison: it FAILS CI if ANY invariant
   exceeds its checked-in floor in `internal/harness/fuzz/floors.json`, OR
   if a brand-new invariant class appears (no floor entry = implicit floor 0
   = automatic failure). A fix that LOWERS a residual is welcome — lower the
   floor in a follow-up; never RAISE a floor to silence a regression. If an
   increase is genuinely intentional (e.g. a new aggressive generator),
   re-baseline via `go run ./cmd/fuzz-gate -update` with a one-line
   justification. Each floor is tagged `(clean)/(sim)/(gen)/(prod)` so
   reviewers know which residuals are real bugs to drive down. The
   master-width invariant is split: `master-width-honored` is the real-bug
   class (floor 0); `master-width-degenerate` is the structural
   false-positive class — a real master-width regression must show as a
   non-zero delta on `master-width-honored`. NEVER delete the reproduction
   test.

Why this order: fuzzer-first forces you to articulate the invariant the
bug violates. That articulation is what keeps the same bug — or a
close cousin — from shipping again. Skipping step 1 leaves the fuzzer
blind to the next instance.

If the fuzzer genuinely cannot reach the scenario (e.g. it lives outside
the simulated event stream), say so in the test comment and in the
commit message, so the coverage limit is explicit rather than implicit.

**Capture & replay real incidents.** When a real incident is hit in
production, capture it: run the daemon with `TK_EVENT_CAPTURE=<path>`,
reproduce, then `go run ./cmd/replay-journal <capture>`. If replay flags an
invariant, that capture IS the regression corpus — commit it under
`testdata/captures/` with a replay test asserting it stays clean. New
invariants MUST be added via `fuzz.CheckStep`
(`internal/harness/fuzz/checkers.go`) so the replay harness and the fuzzer
stay in lockstep — never duplicate a checker into replay.

**Differential-test sim fidelity.** For any sim-vs-sway fidelity change to
`internal/harness/sim/apply.go`, run `go run ./cmd/sway-difftest` against
headless sway — every scenario must stay OK (or remain a documented
KnownGap). A KnownGap that flips to FIXED means the sim now models the
behavior; remove the annotation. Before fixing a divergence in apply.go,
run `just fuzz-gate` and confirm no per-invariant regression — insert-time
percent redistribution in particular is owned by the layout managers, NOT
the sim (modeling it in apply.go spikes `master-width-honored` ~40x).

**This order is not optional and not up for negotiation per-bug.** Never
ask the user "do you want me to start from the fuzzer?" — they have
already said yes, always. Walk the steps without checking in.

## Architecture Notes

- **Manager Interface**: Layout engines implement `layout.Manager` — an event-driven, stateful interface.
  - `WindowAdded/Removed/Focused(ws)`: React to sway window events
  - `Command(cmd, ws)`: Handle user commands (swap-master, rotate, etc.)
  - `ArrangeAll(ws)`: Full rearrangement from scratch
- **Workspace Hub**: Routes sway events to the correct workspace's Manager instance.
- **IPC**: Newline-delimited JSON over Unix socket. Hub implements `ipc.Handler`.
- **nop bindings**: Format is `nop tilekeeper <cmd> [workspace <name>]`. Parsed by `workspace.ParseNopCommand`.
- **Sway IPC**: Binary protocol over `$SWAYSOCK`. Subscribe to `window`, `workspace`, `binding` events.
- **Mark-based movement**: MasterStack uses `mark --add move_target` → `move to mark` → `unmark` for reliable window placement.
- **Session manager integration**: See `docs/session-manager-integration.md`. SM identifies windows by `sm:{project}` title prefix.

## Key Types

- `layout.Manager` — Interface for layout engines
- `layout.MasterStack` — Main layout implementation (~750 lines)
- `workspace.Hub` — Event router and IPC handler
- `sway.Client` — Interface for sway IPC (real + mock)
- `config.Config` — TOML configuration

## Committing

1. Run `just format` before committing
2. Use conventional commits: feat:, fix:, docs:, chore:, refactor:, test:
3. Commit straight to main
4. If the commit is rejected by the pre-commit hook, fix and retry — do
   not bypass the hook
