package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/mschulkind-oss/tilekeeper/internal/config"
	"github.com/mschulkind-oss/tilekeeper/internal/daemon"
	"github.com/mschulkind-oss/tilekeeper/internal/ipc"
	"github.com/mschulkind-oss/tilekeeper/internal/logging"
)

// Set via -ldflags at build time. Keep the zero-value defaults sensible so
// `go run` / `go build` without ldflags still produces a working binary.
var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
	treeState = "unknown"
)

func buildInfo() daemon.BuildInfo {
	return daemon.BuildInfo{
		Version:   version,
		Commit:    commit,
		BuildTime: buildTime,
		TreeState: treeState,
		GoVersion: runtime.Version(),
	}
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(0)
	}

	switch os.Args[1] {
	case "daemon":
		runDaemon()
	case "doctor":
		runDoctor()
	case "install-service":
		runInstallService()
	case "status":
		runClientCommand("status", "")
	case "msg":
		handleMsg()
	case "harness":
		runHarness(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Printf("tilekeeper %s\n", version)
		fmt.Printf("  commit:     %s (%s)\n", commit, treeState)
		fmt.Printf("  built:      %s\n", buildTime)
		fmt.Printf("  go:         %s\n", runtime.Version())
	case "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func runDaemon() {
	configPath := config.FindConfigFile()
	cfg, cfgErr := config.Load(configPath)
	socketPath := cfg.General.IPCSocket

	// Resolve log level. Priority: TK_LOG_LEVEL env > cfg.General.LogLevel
	// > cfg.General.Debug (legacy bool). Any parse error is surfaced as a
	// warning by the daemon after the logger exists.
	levelStr := cfg.General.LogLevel
	if env := os.Getenv("TK_LOG_LEVEL"); env != "" {
		levelStr = env
	}
	level, levelErr := logging.ParseLevel(levelStr)
	if levelStr == "" && cfg.General.Debug {
		level = slog.LevelDebug
		levelStr = "debug" // for the startup log line
	}

	if err := daemon.Run(context.Background(), cfg, daemon.Config{
		SocketPath:   socketPath,
		LogLevel:     level,
		LogLevelName: levelStr,
		LogLevelErr:  levelErr,
		ConfigPath:   configPath,
		ConfigErr:    cfgErr,
		Build:        buildInfo(),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "daemon error: %v\n", err)
		os.Exit(1)
	}
}

// handleMsg sends a layout command to the running daemon — the same command
// string you'd bind with `nop tilekeeper <command>`. Parallels `swaymsg`.
//
//	tilekeeper msg swap-master
//	tilekeeper msg focus left
//	tilekeeper msg layout MasterStack --workspace 4
func handleMsg() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: tilekeeper msg <command> [--workspace <n>]")
		fmt.Fprintln(os.Stderr, "Command list: `tilekeeper --help` or docs/COMMANDS.md")
		os.Exit(1)
	}
	ws := flagWorkspace(2)
	var words []string
	for i := 2; i < len(os.Args); i++ {
		if os.Args[i] == "--workspace" || os.Args[i] == "-w" {
			i++ // skip the flag value
			continue
		}
		words = append(words, os.Args[i])
	}
	runClientCommand(strings.Join(words, " "), ws)
}

func flagWorkspace(startIdx int) string {
	for i := startIdx; i < len(os.Args)-1; i++ {
		if os.Args[i] == "--workspace" || os.Args[i] == "-w" {
			return os.Args[i+1]
		}
	}
	return ""
}

func runClientCommand(command, workspace string) {
	socketPath := ipc.DefaultSocketPath()
	resp, err := ipc.SendRequest(socketPath, ipc.Request{
		Command:   command,
		Workspace: workspace,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "error: %s\n", resp.Error)
		os.Exit(1)
	}
	if resp.Data != nil {
		fmt.Printf("%v\n", resp.Data)
	}
}

func printUsage() {
	fmt.Printf("tilekeeper %s — a layout manager for Sway/Wayland\n", version)
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  tilekeeper daemon              Start the daemon")
	fmt.Println("  tilekeeper msg <command>       Send a layout command to the daemon")
	fmt.Println("  tilekeeper status              Show workspace states (JSON)")
	fmt.Println("  tilekeeper doctor              Check environment")
	fmt.Println("  tilekeeper install-service     Install systemd user service")
	fmt.Println("  tilekeeper harness fuzz        Property-based fuzzer")
	fmt.Println("  tilekeeper version             Show version")
	fmt.Println()
	fmt.Println("Layout commands (via `msg` or `nop tilekeeper <command>` bindings):")
	fmt.Println("  swap-master · focus <dir|master|previous> · move <dir> · rotate <cw|ccw>")
	fmt.Println("  master <grow|shrink|add|remove> · stack <toggle|side-toggle> · maximize · layout <name>")
	fmt.Println("Full reference: docs/COMMANDS.md")
}

func runDoctor() {
	fmt.Println("tilekeeper doctor")
	fmt.Println()

	checks := []struct {
		name  string
		check func() (bool, string)
	}{
		{"Sway running", checkSway},
		{"SWAYSOCK set", checkSwaysock},
		{"Config exists", checkConfig},
	}

	allOk := true
	for _, c := range checks {
		ok, msg := c.check()
		icon := "✅"
		if !ok {
			icon = "❌"
			allOk = false
		}
		fmt.Printf("  %s %s: %s\n", icon, c.name, msg)
	}

	fmt.Println()
	if allOk {
		fmt.Println("All checks passed!")
	} else {
		fmt.Println("Some checks failed. See above for details.")
		os.Exit(1)
	}
}

func checkSway() (bool, string) {
	sock := os.Getenv("SWAYSOCK")
	if sock == "" {
		return false, "SWAYSOCK not set — is Sway running?"
	}
	if _, err := os.Stat(sock); err != nil {
		return false, fmt.Sprintf("SWAYSOCK points to %s but file does not exist", sock)
	}
	return true, "connected"
}

func checkSwaysock() (bool, string) {
	sock := os.Getenv("SWAYSOCK")
	if sock == "" {
		return false, "not set"
	}
	return true, sock
}

func checkConfig() (bool, string) {
	paths := []string{
		os.Getenv("XDG_CONFIG_HOME") + "/tilekeeper/config.toml",
		os.Getenv("HOME") + "/.config/tilekeeper/config.toml",
	}
	for _, p := range paths {
		if p == "/tilekeeper/config.toml" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return true, p
		}
	}
	return false, "no config.toml found in ~/.config/tilekeeper/"
}

// installedBinaryPath returns the stable install location of the tilekeeper
// binary ($GOBIN/tilekeeper, else ~/.local/bin/tilekeeper) if a regular file
// exists there, or "" otherwise. This is where `just install` places it, and
// where the systemd unit's ExecStart should point so restarts survive the repo
// being moved, renamed, or rebuilt.
func installedBinaryPath() string {
	var dir string
	if g := os.Getenv("GOBIN"); g != "" {
		dir = g
	} else if h := os.Getenv("HOME"); h != "" {
		dir = filepath.Join(h, ".local", "bin")
	} else {
		return ""
	}
	p := filepath.Join(dir, "tilekeeper")
	if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
		return p
	}
	return ""
}

func runInstallService() {
	// Prefer the stable install location ($GOBIN or ~/.local/bin, where
	// `just install` places the binary) for the unit's ExecStart. Using the
	// running executable would bake in a throwaway build path like
	// <repo>/bin/tilekeeper — which breaks the service the moment the repo is
	// moved, renamed, or `just clean`ed.
	exePath := installedBinaryPath()
	if exePath == "" {
		var err error
		exePath, err = os.Executable()
		if err != nil {
			exePath, err = exec.LookPath("tilekeeper")
			if err != nil {
				fmt.Fprintln(os.Stderr, "Could not find tilekeeper executable.")
				fmt.Fprintln(os.Stderr, "Install it first with: just install")
				os.Exit(1)
			}
		}
	}
	// Resolve symlinks to get the real path
	exePath, _ = filepath.EvalSymlinks(exePath)

	home := os.Getenv("HOME")
	if home == "" {
		fmt.Fprintln(os.Stderr, "HOME not set")
		os.Exit(1)
	}

	serviceDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create %s: %v\n", serviceDir, err)
		os.Exit(1)
	}

	servicePath := filepath.Join(serviceDir, "tilekeeper.service")
	serviceContent := serviceUnitContent(execStartPath(exePath, home), swaySessionTargetPresent())

	switch planServiceWrite(servicePath, serviceContent) {
	case serviceUpToDate:
		fmt.Printf("systemd service already up to date: %s\n", servicePath)
		return
	case serviceUpdated:
		if err := writeServiceUnit(servicePath, serviceContent); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to write %s: %v\n", servicePath, err)
			os.Exit(1)
		}
		// The caller already has it enabled, so the enable/start recital
		// would be noise — but a changed unit does need a reload to take.
		fmt.Printf("Updated systemd service: %s\n", servicePath)
		fmt.Println("  systemctl --user daemon-reload && systemctl --user restart tilekeeper")
		return
	}

	if err := writeServiceUnit(servicePath, serviceContent); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write %s: %v\n", servicePath, err)
		os.Exit(1)
	}

	fmt.Printf("Created systemd service: %s\n", servicePath)
	fmt.Println()
	fmt.Println("To enable and start the service:")
	fmt.Println("  systemctl --user daemon-reload")
	fmt.Println("  systemctl --user enable tilekeeper")
	fmt.Println("  systemctl --user start tilekeeper")
	fmt.Println()
	fmt.Println("Or use: just deploy")
}

// serviceAction is what install-service should do with the unit on disk.
type serviceAction int

const (
	serviceUpToDate serviceAction = iota // identical content already there
	serviceUpdated                       // present but stale
	serviceCreated                       // absent (or unreadable)
)

// planServiceWrite decides whether the unit at path needs writing, given the
// content install-service wants there. Split out from the writing and the
// printing so `just deploy` staying quiet on a no-op is a tested property
// rather than a manual observation.
//
// An unreadable-but-present file counts as serviceCreated: the write either
// fixes it or fails loudly, and either beats reporting "up to date" about a
// file we could not read.
func planServiceWrite(path, content string) serviceAction {
	existing, err := os.ReadFile(path)
	if err != nil {
		return serviceCreated
	}
	if string(existing) == content {
		return serviceUpToDate
	}
	return serviceUpdated
}

// serviceUnitContent builds the systemd user unit for the daemon.
//
// Ordering is the whole ballgame here. When the host uses the sway convention
// (sway-session.target present) we order and enable against sway-session.target
// and leave PartOf=graphical-session.target. Using After=graphical-session.target
// while enabled into sway-session.target.wants closes an ordering cycle
// (sway-session→tilekeeper→graphical-session→sway-session) that systemd breaks
// by *deleting* tilekeeper's start job — enabled, no error logged, never runs.
// Without sway-session.target we fall back to the generic graphical-session.target
// pattern, which is cycle-safe because no sway-session Before edge exists there.
//
// No Environment= lines: sway pushes SWAYSOCK/WAYLAND_DISPLAY/etc. into the
// systemd user environment (dbus-update-activation-environment), so a unit
// ordered after the session inherits the live values. Snapshotting them here
// froze a dead sway PID into SWAYSOCK across a reboot.
func serviceUnitContent(binPath string, useSwaySession bool) string {
	target := "graphical-session.target"
	if useSwaySession {
		target = "sway-session.target"
	}
	return fmt.Sprintf(`[Unit]
Description=tilekeeper — a layout manager for Sway/Wayland
After=%[1]s
PartOf=graphical-session.target

[Service]
Type=simple
ExecStart=%[2]s daemon
Restart=on-failure
RestartSec=3

[Install]
WantedBy=%[1]s
`, target, binPath)
}

// execStartPath renders the ExecStart binary path. When the binary lives under
// $HOME it is emitted as %h/<rel> so the unit stays portable across machines
// (and matches the documented form); otherwise the absolute path is used.
func execStartPath(exePath, home string) string {
	if home == "" {
		return exePath
	}
	rel, err := filepath.Rel(home, exePath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return exePath
	}
	return "%h/" + filepath.ToSlash(rel)
}

// swaySessionTargetPresent reports whether the systemd user manager knows a
// sway-session.target unit, so install-service can order against it. A missing
// systemctl (or any error) is treated as "absent" → generic fallback.
func swaySessionTargetPresent() bool {
	out, err := exec.Command("systemctl", "--user", "list-unit-files", "sway-session.target").Output()
	if err != nil {
		return false
	}
	return swaySessionListed(string(out))
}

// swaySessionListed parses `systemctl --user list-unit-files sway-session.target`
// output. Split out from the shell-out so the present/absent decision is a
// tested property. `list-unit-files` prints "0 unit files listed." (and may exit
// 0) when nothing matches, so we look for the unit name rather than trusting the
// exit code alone.
func swaySessionListed(listUnitFilesOutput string) bool {
	return strings.Contains(listUnitFilesOutput, "sway-session.target")
}

func writeServiceUnit(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
