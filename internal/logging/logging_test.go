package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		in     string
		want   slog.Level
		hasErr bool
	}{
		{"trace", LevelTrace, false},
		{"TRACE", LevelTrace, false},
		{"debug", slog.LevelDebug, false},
		{"info", slog.LevelInfo, false},
		{"", slog.LevelInfo, false},
		{"warn", slog.LevelWarn, false},
		{"warning", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{"err", slog.LevelError, false},
		{"bogus", slog.LevelInfo, true},
	}
	for _, tt := range tests {
		got, err := ParseLevel(tt.in)
		if got != tt.want {
			t.Errorf("ParseLevel(%q) = %v, want %v", tt.in, got, tt.want)
		}
		if (err != nil) != tt.hasErr {
			t.Errorf("ParseLevel(%q) err = %v, wantErr %v", tt.in, err, tt.hasErr)
		}
	}
}

func TestNewRootRendersTraceLabel(t *testing.T) {
	var buf bytes.Buffer
	logger := NewRoot(&buf, LevelTrace)
	Trace(logger, "hello", "k", "v")
	out := buf.String()
	if !strings.Contains(out, "level=TRACE") {
		t.Errorf("expected level=TRACE in %q", out)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("expected message in %q", out)
	}
}

func TestComponentAddsAttr(t *testing.T) {
	var buf bytes.Buffer
	root := NewRoot(&buf, slog.LevelDebug)
	comp := Component(root, "hub")
	comp.Debug("x")
	out := buf.String()
	if !strings.Contains(out, `component=hub`) {
		t.Errorf("expected component=hub in %q", out)
	}
}

// TestNoDuplicateKeys pins the dedup handler against the exact malformed lines
// seen in the daemon startup log: a child logger re-adding a key its parent
// already set (component=hub → component=layout.tabbed, workspace=8 twice,
// version/commit repeated) must emit each key once, last value winning.
func TestNoDuplicateKeys(t *testing.T) {
	var buf bytes.Buffer
	root := NewRoot(&buf, slog.LevelDebug).With("version", "abc123", "commit", "def456")
	mgr := Component(Component(root, "hub"), "layout.tabbed").With("workspace", "8")

	// Re-add keys the scoped logger already carries — the real call-site pattern.
	mgr.Info("ensure", "workspace", "8", "version", "abc123")

	line := buf.String()
	for _, key := range []string{"component=", "workspace=", "version=", "commit="} {
		if n := strings.Count(line, key); n != 1 {
			t.Errorf("key %q appears %d times, want 1\n  line: %s", key, n, line)
		}
	}
	if !strings.Contains(line, "component=layout.tabbed") {
		t.Errorf("last-wins failed: expected component=layout.tabbed, got: %s", line)
	}
}
