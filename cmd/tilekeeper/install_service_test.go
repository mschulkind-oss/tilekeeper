package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestInstalledBinaryPath pins the install-service fix: the systemd unit's
// ExecStart must point at the stable install location (~/.local/bin or
// $GOBIN), not the repo build path — otherwise a repo rename/move orphans the
// service (the exact "restart killed the daemon and nothing started" bug).
func TestInstalledBinaryPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("GOBIN", "")
	t.Setenv("HOME", tmp)

	if got := installedBinaryPath(); got != "" {
		t.Fatalf("no installed binary yet: got %q, want empty", got)
	}

	bin := filepath.Join(tmp, ".local", "bin", "tilekeeper")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := installedBinaryPath(); got != bin {
		t.Errorf("~/.local/bin: got %q, want %q", got, bin)
	}

	// GOBIN takes precedence when set.
	gobin := filepath.Join(tmp, "gobin")
	gbin := filepath.Join(gobin, "tilekeeper")
	if err := os.MkdirAll(gobin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gbin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOBIN", gobin)
	if got := installedBinaryPath(); got != gbin {
		t.Errorf("with GOBIN: got %q, want %q", got, gbin)
	}
}
