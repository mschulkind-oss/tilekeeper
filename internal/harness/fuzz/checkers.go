package fuzz

import (
	"github.com/mschulkind-oss/tilekeeper/internal/harness/sim"
	"github.com/mschulkind-oss/tilekeeper/internal/sway"
	"github.com/mschulkind-oss/tilekeeper/internal/workspace"
)

// CheckStep runs the full invariant battery for a single processed event
// against a (hub, sim) pair and appends any violations to res. It is the
// shared entrypoint the replay harness (internal/harness/replay) reuses so
// a real captured incident is judged by EXACTLY the same checkers the
// fuzzer uses — no duplicated invariant logic, no drift between the two.
//
// This mirrors the per-step block inside RunWithTrace: the always-on
// invariants (no-invalid-cmd, no-sway-reject, focus-convergence,
// no-wrapper-chain) plus the MasterStack-aware ones (tracked-matches-leaves,
// master-width-honored, master-stack-split, maximized-fold-intact). The no-crash invariant is the
// caller's responsibility (it must recover the panic around HandleEvent,
// which only the caller can wrap); errSink drives no-handler-error and is
// likewise driven by the caller, which owns the slog handler. Pass the
// event and step that produced the current state so violations carry a
// precise origin.
//
// Callers must have already dispatched ev to the hub (or chosen to drop it)
// before calling CheckStep — the checks read post-dispatch sim/hub state.
func CheckStep(hub *workspace.Hub, s *sim.SimSwayClient, wsNames []string, ev sway.Event, step int, res *Result) {
	checkInvariants(s, ev, step, res)
	checkTrackedMatchesLeaves(hub, s, wsNames, ev, step, res)
	checkMasterWidthHonored(hub, s, wsNames, ev, step, res)
	checkMasterStackSplit(hub, s, wsNames, ev, step, res)
	checkMaximizedFoldIntact(hub, s, wsNames, ev, step, res)
}

// NewErrorSink returns a slog.Handler that records Error-level log lines,
// so a caller can drive the no-handler-error invariant the same way
// RunWithTrace does. Call Drain after each step and wrap each returned
// message in a Violation{Invariant: "no-handler-error"}.
//
// Exposed for the replay harness, which needs the identical error-capture
// behavior but constructs its own hub.
func NewErrorSink() *ErrorSink { return &ErrorSink{} }

// ErrorSink is the exported alias of the fuzzer's internal error-capturing
// slog.Handler. Construct via NewErrorSink, pass slog.New(sink) as the
// hub's logger, and call Drain() after each step.
type ErrorSink = errorCaptureHandler

// Drain returns and clears the accumulated Error-level messages.
func (h *errorCaptureHandler) DrainMessages() []string { return h.drain() }
