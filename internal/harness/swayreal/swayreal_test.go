package swayreal

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeExe writes an executable file at dir/name and returns its path.
func fakeExe(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestFindSwayBinaryPrefersEnvOverPath pins the precedence fix: an explicit
// $SWAY_BIN must outrank whatever `sway` happens to be on PATH.
//
// The concrete failure this prevents: nix's `sway` on PATH is a wrapper that
// execs dbus-run-session, so in a container without dbus-daemon it dies
// before opening the IPC socket and every run fails with "socket did not
// appear" — while a working unwrapped binary sits in $SWAY_BIN, ignored,
// because PATH used to win.
func TestFindSwayBinaryPrefersEnvOverPath(t *testing.T) {
	tmp := t.TempDir()
	pathDir := filepath.Join(tmp, "path")
	pathSway := fakeExe(t, pathDir, "sway")
	envSway := fakeExe(t, filepath.Join(tmp, "env"), "sway-unwrapped")

	t.Setenv("PATH", pathDir)

	t.Setenv("SWAY_BIN", envSway)
	if got, err := FindSwayBinary(); err != nil || got != envSway {
		t.Errorf("with SWAY_BIN set: got (%q, %v), want %q — an explicit override\n"+
			"must beat PATH", got, err, envSway)
	}

	// Unset: PATH is the next-best source.
	t.Setenv("SWAY_BIN", "")
	if got, err := FindSwayBinary(); err != nil || got != pathSway {
		t.Errorf("without SWAY_BIN: got (%q, %v), want PATH's %q", got, err, pathSway)
	}

	// A SWAY_BIN pointing at nothing must not shadow a usable PATH sway.
	t.Setenv("SWAY_BIN", filepath.Join(tmp, "does-not-exist"))
	if got, err := FindSwayBinary(); err != nil || got != pathSway {
		t.Errorf("with a dangling SWAY_BIN: got (%q, %v), want fallback to PATH %q",
			got, err, pathSway)
	}
}
