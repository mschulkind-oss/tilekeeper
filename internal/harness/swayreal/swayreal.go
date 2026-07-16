// Package swayreal boots a real, headless sway instance and exposes it via
// the production internal/sway.Conn IPC client. It is the "ground truth"
// side of the differential test (cmd/sway-difftest): the in-memory sim is
// validated against this live sway, so every divergence is a real
// sim-fidelity gap rather than a guess about sway's behavior.
//
// The lifecycle is: locate the sway binary (PATH → $SWAY_BIN → known nix
// store fallback), create a private XDG_RUNTIME_DIR, launch
// `sway -c /dev/null` with the wlroots headless backend, wait for the IPC
// socket to appear, dial it with sway.Conn, and tear everything down on
// Close. Window creation uses a Wayland client (weston-terminal, with a
// kitty fallback) launched via `swaymsg exec` — sway has no built-in
// spawn, so a real client must map an xdg-toplevel for sway to tile.
//
// If no sway binary is found, Start returns ErrNoSway so callers (and the
// integration test) can Skip cleanly. This mirrors the `command -v sway`
// gate in `just test-integration`.
package swayreal

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mschulkind-oss/tilekeeper/internal/sway"
)

// ErrNoSway is returned by Start (and FindSwayBinary) when no usable sway
// binary can be located. Callers should treat it as a skip, not a failure.
var ErrNoSway = errors.New("swayreal: no sway binary found (PATH, $SWAY_BIN, known nix path)")

// knownSwayBin is the unwrapped sway verified to work headless.
// The wrapped sway needs dbus-run-session and fails under the headless
// backend, so we deliberately prefer the unwrapped binary here. PATH and
// $SWAY_BIN still take precedence so a host with sway installed normally
// works without this fallback.
const knownSwayBin = "/nix/store/qa2c4vl74px67y0v298dr2i1nykqn5dc-sway-unwrapped-1.11/bin/sway"

// Known Wayland clients usable to map a tiled window. weston-terminal is
// preferred (no session bus required); kitty is a fallback.
var knownClients = []string{
	"weston-terminal",
	"/nix/store/51k21jdkw6h7mnf6kshvx6j8f73zgzin-weston-14.0.1/bin/weston-terminal",
	"kitty",
	"/nix/store/msclaibiqsw1y11ycm5sydrivzm72a90-kitty-0.45.0/bin/kitty",
}

// FindSwayBinary resolves the sway binary path with the precedence
// $SWAY_BIN, then $PATH, then the known nix-store fallback. Returns
// ErrNoSway if none exist.
//
// $SWAY_BIN deliberately outranks $PATH. It used to be the other way round,
// which made the variable useless as an override and left the harness
// unusable wherever PATH's sway does not work headlessly: nix's `sway` is a
// wrapper that execs dbus-run-session, so in a container without
// dbus-daemon it dies before opening the IPC socket, and the run failed with
// "socket did not appear" while a perfectly good unwrapped binary sat in
// $SWAY_BIN being ignored. An explicit override losing to ambient PATH is
// the wrong way round.
func FindSwayBinary() (string, error) {
	if env := os.Getenv("SWAY_BIN"); env != "" {
		if fileExists(env) {
			return env, nil
		}
	}
	if p, err := exec.LookPath("sway"); err == nil {
		return p, nil
	}
	if fileExists(knownSwayBin) {
		return knownSwayBin, nil
	}
	return "", ErrNoSway
}

// findClient resolves the Wayland client used to spawn windows. The sway
// binary's directory is also probed for a co-located weston-terminal in
// case PATH is bare but the nix store has both.
func findClient(swayBin string) (string, error) {
	for _, c := range knownClients {
		if strings.ContainsRune(c, '/') {
			if fileExists(c) {
				return c, nil
			}
			continue
		}
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	return "", errors.New("swayreal: no Wayland client (weston-terminal/kitty) found to spawn windows")
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// Sway is a running headless sway instance plus a connected IPC client.
type Sway struct {
	Bin  string
	Conn *sway.Conn
	cmd  *exec.Cmd
	runtime
	clientBin string
	logPath   string
}

type runtime struct {
	dir         string // XDG_RUNTIME_DIR
	waylandName string // WAYLAND_DISPLAY
	sockPath    string // sway IPC socket
}

// Options tunes Start. The zero value is valid.
type Options struct {
	// StartTimeout bounds how long to wait for the IPC socket. Default 10s.
	StartTimeout time.Duration
	// Env adds extra environment variables to the sway process.
	Env []string
}

// Start launches a headless sway, waits for its IPC socket, and connects.
// On success the caller MUST call Close to terminate sway and remove the
// temp runtime dir. Returns ErrNoSway (wrapped) if no sway binary exists.
func Start(opts Options) (*Sway, error) {
	bin, err := FindSwayBinary()
	if err != nil {
		return nil, err
	}
	if opts.StartTimeout <= 0 {
		opts.StartTimeout = 10 * time.Second
	}

	dir, err := os.MkdirTemp("", "swayreal-xdg-")
	if err != nil {
		return nil, fmt.Errorf("swayreal: temp runtime dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("swayreal: chmod runtime dir: %w", err)
	}

	waylandName := fmt.Sprintf("wayland-difftest-%d", os.Getpid())
	logPath := filepath.Join(dir, "sway.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("swayreal: sway log file: %w", err)
	}

	cmd := exec.Command(bin, "-c", "/dev/null")
	cmd.Env = append(os.Environ(),
		"XDG_RUNTIME_DIR="+dir,
		"WLR_BACKENDS=headless",
		"WLR_LIBINPUT_NO_DEVICES=1",
		"WAYLAND_DISPLAY="+waylandName,
		"LIBGL_ALWAYS_SOFTWARE=1",
	)
	cmd.Env = append(cmd.Env, opts.Env...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// New process group so we can signal the whole tree on Close even if
	// sway spawned children (clients) that ignore the parent's signal.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("swayreal: start sway: %w", err)
	}
	logFile.Close()

	s := &Sway{
		Bin:     bin,
		cmd:     cmd,
		logPath: logPath,
		runtime: runtime{dir: dir, waylandName: waylandName},
	}

	sock, err := waitForSocket(dir, opts.StartTimeout)
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("swayreal: %w (sway log: %s)", err, s.tailLog())
	}
	s.runtime.sockPath = sock

	conn, err := sway.ConnectTo(sock)
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("swayreal: connect to %s: %w", sock, err)
	}
	s.Conn = conn

	// Resolve a window-spawning client now so SpawnWindows fails fast if
	// none is available, but tolerate its absence (some callers only run
	// command-only scenarios on a pre-seeded tree).
	if cb, err := findClient(bin); err == nil {
		s.clientBin = cb
	}

	return s, nil
}

// SocketPath returns the IPC socket sway is listening on.
func (s *Sway) SocketPath() string { return s.runtime.sockPath }

// RuntimeDir returns the private XDG_RUNTIME_DIR sway runs under.
func (s *Sway) RuntimeDir() string { return s.runtime.dir }

// waitForSocket polls dir for a sway-ipc.*.sock until it appears or the
// timeout elapses.
func waitForSocket(dir string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		matches, _ := filepath.Glob(filepath.Join(dir, "sway-ipc.*.sock"))
		if len(matches) > 0 {
			return matches[0], nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("sway IPC socket did not appear under %s within %s", dir, timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// tailLog returns the last chunk of sway's log for error context.
func (s *Sway) tailLog() string {
	data, err := os.ReadFile(s.logPath)
	if err != nil {
		return "<no log>"
	}
	const max = 800
	if len(data) > max {
		data = data[len(data)-max:]
	}
	return strings.TrimSpace(string(data))
}

// Close terminates sway (and its process group) and removes the temp
// runtime dir. Safe to call multiple times.
func (s *Sway) Close() error {
	if s.Conn != nil {
		s.Conn.Close()
		s.Conn = nil
	}
	if s.cmd != nil && s.cmd.Process != nil {
		// Signal the whole process group (negative pid) so client windows
		// die with sway.
		pgid, err := syscall.Getpgid(s.cmd.Process.Pid)
		if err == nil {
			syscall.Kill(-pgid, syscall.SIGTERM)
		} else {
			s.cmd.Process.Signal(syscall.SIGTERM)
		}
		done := make(chan struct{})
		go func() { s.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			if pgid, err := syscall.Getpgid(s.cmd.Process.Pid); err == nil {
				syscall.Kill(-pgid, syscall.SIGKILL)
			} else {
				s.cmd.Process.Kill()
			}
		}
		s.cmd = nil
	}
	if s.runtime.dir != "" {
		os.RemoveAll(s.runtime.dir)
		s.runtime.dir = ""
	}
	return nil
}

// GetTree fetches the live tree from sway via the production IPC client.
func (s *Sway) GetTree() (*sway.Node, error) {
	if s.Conn == nil {
		return nil, errors.New("swayreal: not connected")
	}
	return s.Conn.GetTree()
}

// RunCommand sends a command to sway via IPC.
func (s *Sway) RunCommand(cmd string) error {
	if s.Conn == nil {
		return errors.New("swayreal: not connected")
	}
	return s.Conn.RunCommand(cmd)
}
