package main

import (
	"os"
	"path/filepath"
	"strings"
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

	// The unit template embeds the ExecStart path, so a changed install
	// location has to be picked up, not skipped as "same".
	if err := writeServiceUnit(path, "ExecStart=/old/path daemon\n"); err != nil {
		t.Fatal(err)
	}
	if got := planServiceWrite(path, "ExecStart=/new/path daemon\n"); got != serviceUpdated {
		t.Errorf("relocated ExecStart: got %v, want serviceUpdated", got)
	}
}

// TestServiceUnitNoOrderingCycle pins the boot-time ordering-cycle regression:
// a unit that used After=graphical-session.target while enabled into
// sway-session.target.wants closed a cycle, and systemd silently *deleted*
// tilekeeper's start job at every boot — the unit stayed enabled, nothing was
// logged, and the daemon just never ran. When sway-session.target is present we
// order and enable against it; PartOf=graphical-session.target must remain so
// the daemon still stops when the session ends.
func TestServiceUnitNoOrderingCycle(t *testing.T) {
	sway := serviceUnitContent("/opt/tilekeeper", true)
	for _, want := range []string{
		"After=sway-session.target",
		"WantedBy=sway-session.target",
		"PartOf=graphical-session.target",
	} {
		if !strings.Contains(sway, want) {
			t.Errorf("sway-session unit missing %q:\n%s", want, sway)
		}
	}
	for _, bad := range []string{
		"After=graphical-session.target",
		"WantedBy=graphical-session.target",
	} {
		if strings.Contains(sway, bad) {
			t.Errorf("sway-session unit has %q — reintroduces the boot cycle:\n%s", bad, sway)
		}
	}

	// Fallback: no sway-session.target → generic graphical-session.target
	// pattern, which is cycle-safe (no sway-session Before edge exists there).
	generic := serviceUnitContent("/opt/tilekeeper", false)
	for _, want := range []string{
		"After=graphical-session.target",
		"WantedBy=graphical-session.target",
	} {
		if !strings.Contains(generic, want) {
			t.Errorf("fallback unit missing %q:\n%s", want, generic)
		}
	}
	if strings.Contains(generic, "sway-session.target") {
		t.Errorf("fallback unit must not reference sway-session.target:\n%s", generic)
	}
}

// TestServiceUnitNoFrozenSwaysock pins the frozen-PID regression: the generator
// used to snapshot $SWAYSOCK (which embeds the live sway PID) into an
// Environment= line, so after a reboot the unit pointed at a dead socket. The
// unit must carry no Environment= lines at all — sway pushes the live values
// into the systemd user environment and a session-ordered unit inherits them.
func TestServiceUnitNoFrozenSwaysock(t *testing.T) {
	t.Setenv("SWAYSOCK", "/run/user/1000/sway-ipc.1000.2353.sock")
	t.Setenv("WAYLAND_DISPLAY", "wayland-1")
	unit := serviceUnitContent("/opt/tilekeeper", true)
	if strings.Contains(unit, "Environment=") {
		t.Errorf("unit must carry no Environment= lines:\n%s", unit)
	}
	if strings.Contains(unit, "2353") {
		t.Errorf("unit froze the live SWAYSOCK PID:\n%s", unit)
	}
}

// TestServiceUnitExecStartPortable pins the %h portability of ExecStart: a
// binary under $HOME is rendered as %h/... so the unit is host-portable and
// matches the documented form; a binary outside $HOME keeps its absolute path.
func TestServiceUnitExecStartPortable(t *testing.T) {
	home := "/home/alice"
	if got := execStartPath("/home/alice/.local/bin/tilekeeper", home); got != "%h/.local/bin/tilekeeper" {
		t.Errorf("home-relative: got %q, want %%h/.local/bin/tilekeeper", got)
	}
	if got := execStartPath("/usr/local/bin/tilekeeper", home); got != "/usr/local/bin/tilekeeper" {
		t.Errorf("outside home: got %q, want the absolute path", got)
	}
	if got := execStartPath("/opt/tilekeeper", ""); got != "/opt/tilekeeper" {
		t.Errorf("no HOME: got %q, want the absolute path", got)
	}
}

// TestSwaySessionListed pins the present/absent parse of `systemctl --user
// list-unit-files sway-session.target`, which decides which ordering target the
// generated unit uses. list-unit-files can exit 0 with "0 unit files listed."
// when nothing matches, so the unit name — not the exit code — is the signal.
func TestSwaySessionListed(t *testing.T) {
	present := "UNIT FILE           STATE  PRESET\n" +
		"sway-session.target static -\n\n1 unit files listed.\n"
	if !swaySessionListed(present) {
		t.Errorf("present output parsed as absent:\n%s", present)
	}
	absent := "UNIT FILE STATE PRESET\n\n0 unit files listed.\n"
	if swaySessionListed(absent) {
		t.Errorf("absent output parsed as present:\n%s", absent)
	}
}

// TestFixedGeneratorRewritesBadUnit pins the migration path: an already-broken
// unit on disk (old graphical-session.target ordering plus a frozen SWAYSOCK)
// must be seen as stale so the next `just deploy` rewrites it. If planServiceWrite
// reported serviceUpToDate the bad unit would persist forever.
func TestFixedGeneratorRewritesBadUnit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tilekeeper.service")

	const bad = "[Unit]\nDescription=tilekeeper — a layout manager for Sway/Wayland\n" +
		"After=graphical-session.target\nPartOf=graphical-session.target\n\n" +
		"[Service]\nType=simple\nExecStart=/opt/tilekeeper daemon\n" +
		"Environment=SWAYSOCK=/run/user/1000/sway-ipc.1000.2353.sock\n\n" +
		"[Install]\nWantedBy=graphical-session.target\n"
	if err := writeServiceUnit(path, bad); err != nil {
		t.Fatal(err)
	}

	fixed := serviceUnitContent("/opt/tilekeeper", true)
	if got := planServiceWrite(path, fixed); got != serviceUpdated {
		t.Errorf("stale broken unit: got %v, want serviceUpdated — the fix must rewrite it", got)
	}
}
