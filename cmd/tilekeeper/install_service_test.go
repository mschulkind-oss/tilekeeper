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

// TestPlanServiceWrite pins the "don't repeatedly install the service" fix:
// `just deploy` runs install-service every time, so an unchanged unit must be
// a no-op instead of a rewrite plus a wall of setup steps deploy already did.
func TestPlanServiceWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tilekeeper.service")
	const content = "[Unit]\nDescription=x\n"

	if got := planServiceWrite(path, content); got != serviceCreated {
		t.Errorf("absent unit: got %v, want serviceCreated", got)
	}

	if err := writeServiceUnit(path, content); err != nil {
		t.Fatal(err)
	}
	if got := planServiceWrite(path, content); got != serviceUpToDate {
		t.Errorf("identical unit: got %v, want serviceUpToDate — a repeat deploy\n"+
			"must not rewrite the unit", got)
	}

	if got := planServiceWrite(path, content+"Extra=1\n"); got != serviceUpdated {
		t.Errorf("stale unit: got %v, want serviceUpdated", got)
	}

	// The unit template embeds the ExecStart path and the sway env block, so
	// a changed install location has to be picked up, not skipped as "same".
	if err := writeServiceUnit(path, "ExecStart=/old/path daemon\n"); err != nil {
		t.Fatal(err)
	}
	if got := planServiceWrite(path, "ExecStart=/new/path daemon\n"); got != serviceUpdated {
		t.Errorf("relocated ExecStart: got %v, want serviceUpdated", got)
	}
}
