// Package logging centralizes slog level parsing and component-scoped
// logger construction for tilekeeper.
//
// The daemon uses one root logger with version/commit attached as default
// attributes; every subsystem derives a child logger via Component() so
// log lines can be filtered by `component=<name>` in journalctl.
//
// An extra "TRACE" level (slog.LevelDebug-4) is available for ultra-
// verbose per-IPC-message traces that would drown the output at DEBUG.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// LevelTrace is one step below slog.LevelDebug. Enable with logLevel = "trace".
const LevelTrace slog.Level = slog.LevelDebug - 4

// ParseLevel maps a config/env string to a slog.Level.
//
// Accepts (case-insensitive): trace, debug, info, warn, warning, error.
// Unknown values fall back to Info and return a descriptive error so the
// caller can log the fallback.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace":
		return LevelTrace, nil
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error", "err":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("unknown log level %q, defaulting to info", s)
	}
}

// LevelName returns the human-readable name for a level, including TRACE.
func LevelName(l slog.Level) string {
	if l <= LevelTrace {
		return "TRACE"
	}
	return l.String()
}

// NewRoot returns a *slog.Logger writing to w at level, with TRACE support
// and a ReplaceAttr that renders the custom TRACE level as "TRACE" rather
// than slog's default "DEBUG-4".
func NewRoot(w io.Writer, level slog.Level) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.LevelKey {
				if lvl, ok := a.Value.Any().(slog.Level); ok {
					a.Value = slog.StringValue(LevelName(lvl))
				}
			}
			return a
		},
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

// Component returns a logger tagged with component=<name>.
// Pass nil parent to fall back to slog.Default().
func Component(parent *slog.Logger, name string) *slog.Logger {
	if parent == nil {
		parent = slog.Default()
	}
	return parent.With("component", name)
}

// Trace emits a log record at LevelTrace through logger. slog.Logger has
// no Trace method; this is a thin helper so callers don't need to spell
// out LogAttrs every time.
func Trace(logger *slog.Logger, msg string, args ...any) {
	if logger == nil {
		return
	}
	logger.Log(context.Background(), LevelTrace, msg, args...)
}
