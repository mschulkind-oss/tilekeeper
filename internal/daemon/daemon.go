// Package daemon implements the main event loop for tilekeeper.
//
// The daemon connects to Sway IPC, subscribes to events, starts the
// IPC server, and routes events through the workspace Hub.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/mschulkind-oss/tilekeeper/internal/capture"
	"github.com/mschulkind-oss/tilekeeper/internal/config"
	"github.com/mschulkind-oss/tilekeeper/internal/ipc"
	"github.com/mschulkind-oss/tilekeeper/internal/logging"
	"github.com/mschulkind-oss/tilekeeper/internal/sway"
	"github.com/mschulkind-oss/tilekeeper/internal/workspace"
)

// EnvEventCapture names the env var that enables the optional JSONL
// event-capture sink. When set to a writable path, the daemon appends one
// JSON line per processed sway.Event so the replay harness
// (cmd/replay-journal) can reconstruct the exact event stream — faithfully,
// including the container snapshot fields the text journal omits. Unset =>
// zero overhead (nil Writer; a single nil-check on the hot path).
const EnvEventCapture = "TK_EVENT_CAPTURE"

// BuildInfo carries the ldflags-injected build metadata so every log line
// can be traced back to a specific binary.
type BuildInfo struct {
	Version   string
	Commit    string
	BuildTime string
	TreeState string // "clean" or "dirty" — whether the working tree was dirty at build time
	GoVersion string
}

// Config holds daemon runtime configuration.
type Config struct {
	SocketPath string
	LogLevel   slog.Level

	// LogLevelName is the raw string that produced LogLevel (for the
	// startup banner — so users see what the config says, not just the
	// resolved numeric level).
	LogLevelName string

	// LogLevelErr is a non-fatal parse error from ParseLevel. If set, the
	// daemon logs a warning at startup and falls back to Info.
	LogLevelErr error

	// ConfigPath is the absolute path of the config file that was loaded,
	// or empty if defaults were used. Logged in the startup banner so the
	// user can tell at a glance whether their edits were picked up.
	ConfigPath string
	ConfigErr  error

	Build BuildInfo
}

// Run starts the tilekeeper daemon. It blocks until a signal is received
// or the context is cancelled.
func Run(ctx context.Context, cfg config.Config, daemonCfg Config) error {
	// Root logger carries version+commit as default attrs so every log
	// line is traceable to a specific build.
	root := logging.NewRoot(os.Stderr, daemonCfg.LogLevel).With(
		"version", daemonCfg.Build.Version,
		"commit", daemonCfg.Build.Commit,
	)
	slog.SetDefault(root)

	logger := logging.Component(root, "daemon")

	logStartupBanner(logger, cfg, daemonCfg)

	if daemonCfg.LogLevelErr != nil {
		logger.Warn("log level parse error, defaulting to info",
			"requested", daemonCfg.LogLevelName, "error", daemonCfg.LogLevelErr)
	}
	if daemonCfg.ConfigErr != nil {
		logger.Warn("config load error, using defaults",
			"path", daemonCfg.ConfigPath, "error", daemonCfg.ConfigErr)
	}

	swayLogger := logging.Component(root, "sway")
	client, err := sway.Connect()
	if err != nil {
		return fmt.Errorf("connect to sway: %w", err)
	}
	client.SetLogger(swayLogger)
	logger.Info("connected to sway", "socket", client.SocketPath())

	hub := workspace.NewHub(client, cfg, logging.Component(root, "hub"))
	hub.Initialize()

	if err := arrangeExisting(client, hub, logger); err != nil {
		logger.Warn("initial arrange failed", "error", err)
	}

	// Optional event-capture sink. Opt-in via TK_EVENT_CAPTURE=<path>; when
	// unset, capWriter is nil and every capWriter.WriteEvent call below is a
	// no-op (one nil-check, no allocation), so the hot path pays nothing.
	// A failure to open is non-fatal — capture is a diagnostic, not a
	// dependency — so we log and proceed with a nil Writer.
	var capWriter *capture.Writer
	if capPath := os.Getenv(EnvEventCapture); capPath != "" {
		w, cerr := capture.Open(capPath)
		if cerr != nil {
			logger.Warn("event capture disabled: open failed", "path", capPath, "error", cerr)
		} else {
			capWriter = w
			defer capWriter.Close()
			meta := capture.MetaRecord{
				Version:       daemonCfg.Build.Version,
				Commit:        daemonCfg.Build.Commit,
				DefaultLayout: cfg.General.DefaultLayout,
				MasterWidth:   cfg.General.MasterWidth,
			}
			for name := range cfg.Workspaces {
				meta.Workspaces = append(meta.Workspaces, name)
			}
			// Seed replay's initial state with the startup tree shape so a
			// capture taken against pre-existing windows replays faithfully.
			if tree, terr := client.GetTree(); terr == nil {
				meta.Tree = tree
			}
			if werr := capWriter.WriteMeta(meta); werr != nil {
				logger.Warn("event capture meta write failed", "error", werr)
			}
			logger.Info("event capture enabled", "path", capPath)
		}
	}

	socketPath := daemonCfg.SocketPath
	if socketPath == "" {
		socketPath = ipc.DefaultSocketPath()
	}

	ipcServer, err := ipc.NewServer(socketPath, hub, logging.Component(root, "ipc"))
	if err != nil {
		return fmt.Errorf("start IPC server: %w", err)
	}
	defer ipcServer.Close()

	go func() {
		if err := ipcServer.Serve(); err != nil {
			logger.Error("IPC server error", "error", err)
		}
	}()
	logger.Info("IPC server listening", "socket", socketPath)

	// Channel sizing: 4096 is comfortable headroom over the 64 we used to
	// run with — burst pressure isn't from sway sending faster than we
	// can read, it's from MasterStack's own mark/move/swap command
	// sequence echoing back as window::mark + window::move events while
	// the consumer is still mid-pushWindow. A ~5-window move
	// rearrangement generates ~15 echo events, so 4096 absorbs roughly a
	// 270-window-rearrangement storm before pressure shows.
	//
	// Combined with the subscribe-side filter dropping window::mark /
	// title / urgent (events nothing reacts to) the steady-state inflow
	// is small. There is intentionally NO drop-recovery mechanism: drops
	// are silent corruption sources, so we log them at ERROR and rely on
	// prevention. If you see a drop in the journal that's a real bug,
	// not a transient — find why and fix it, do not paper over it.
	eventCh := make(chan sway.Event, 4096)

	subClient, err := sway.Connect()
	if err != nil {
		return fmt.Errorf("connect for subscribe: %w", err)
	}
	subClient.SetLogger(logging.Component(root, "sway-sub"))

	// Per-daemon monotonic event sequence. Assigned in the subscribe
	// callback BEFORE any branch decision (queue or drop) so dispatched
	// and dropped events share the same sequence space. Logged on every
	// event line — a `seq=N seq=N+2` skip in the journal pinpoints the
	// dropped event without ambiguity. Resets per daemon lifetime
	// (daemon restart starts at 0; the startup banner is the boundary).
	var seqCtr atomic.Int64
	var dropCtr atomic.Int64

	go func() {
		err := subClient.Subscribe(
			[]string{"window", "workspace", "binding"},
			func(event sway.Event) {
				// Drop high-volume events nothing reacts to. Keeps
				// channel pressure low and matches the intent of the
				// existing handler switch in workspace.handleWindowEvent
				// (cases: new, close, focus, floating, move).
				if !shouldDispatch(event) {
					return
				}
				event.Seq = seqCtr.Add(1)
				select {
				case eventCh <- event:
				default:
					n := dropCtr.Add(1)
					logDroppedEvent(logger, event, n)
				}
			},
		)
		if err != nil {
			logger.Error("subscribe error", "error", err)
		}
	}()
	logger.Info("subscribed to sway events", "types", "window,workspace,binding")

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("daemon ready", "pid", os.Getpid())
	for {
		select {
		case <-ctx.Done():
			logger.Info("shutting down",
				"reason", ctx.Err(),
				"events_seen", seqCtr.Load(),
				"events_dropped", dropCtr.Load())
			return nil
		case event := <-eventCh:
			// Capture BEFORE dispatch so the recorded stream is exactly the
			// sequence the hub consumed, in order. WriteEvent no-ops when
			// capture is disabled (capWriter == nil) — and we skip the tree
			// round-trip entirely in that case, keeping the hot path free.
			if capWriter != nil {
				// Resolve the container's CURRENT workspace from the live
				// tree. For window::move this is the DESTINATION — the single
				// fact replay needs that the snapshot can't carry. Best
				// effort: a failed GetTree or an already-gone node yields "".
				wsName := resolveEventWorkspace(client, event)
				if err := capWriter.WriteEvent(event, wsName); err != nil {
					logger.Warn("event capture write failed", "seq", event.Seq, "error", err)
				}
			}
			hub.HandleEvent(event)
		}
	}
}

// resolveEventWorkspace returns the name of the workspace the event's
// container currently lives on, read from the LIVE sway tree. This is only
// called when event capture is enabled, so the extra get_tree round-trip
// per event is paid only by capture sessions (diagnostic use), never in
// normal operation. For workspace events it returns the workspace name
// directly; for window events it looks the container up in the tree (the
// move case resolves the DESTINATION, which the event snapshot can't
// carry). Returns "" when nothing resolves — replay tolerates that.
func resolveEventWorkspace(client sway.Client, ev sway.Event) string {
	switch ev.Type {
	case "workspace":
		if ev.Workspace != nil {
			return ev.Workspace.Name
		}
		return ""
	case "window":
		if ev.Container == nil {
			return ""
		}
		tree, err := client.GetTree()
		if err != nil || tree == nil {
			return ""
		}
		tree.SetParents()
		if node := tree.FindByID(ev.Container.ID); node != nil {
			if ws := node.FindWorkspace(); ws != nil {
				return ws.Name
			}
		}
		return ""
	default:
		return ""
	}
}

// shouldDispatch returns true if the event has a handler in
// workspace.Hub. Filtering at the subscribe layer prevents high-volume
// no-op events (window::mark, window::title, window::urgent) from
// consuming channel slots during a burst — they would just fall through
// the handleWindowEvent switch anyway.
//
// window::mark is the worst offender: every MasterStack reposition
// emits a `mark --add move_target` + `unmark move_target` pair, each
// firing window::mark. The 2026-05-22 ws7 drop storm was largely
// window::mark echoes from the manager's own commands.
//
// Allow-list rather than deny-list: if a new event type that we don't
// recognize ships, we want to ignore it by default until a handler
// exists. Match exactly the cases handleWindowEvent / handleWorkspaceEvent
// / handleBindingEvent actually use.
func shouldDispatch(ev sway.Event) bool {
	switch ev.Type {
	case "window":
		switch ev.Change {
		case "new", "close", "focus", "floating", "move":
			return true
		default:
			// mark, title, urgent, fullscreen_mode — all unhandled.
			// fullscreen_mode is a known gap (the fuzzer surfaces it)
			// but the existing handler doesn't react, so dropping it
			// at the subscribe layer doesn't change observable behavior.
			// NOTE: MasterStack's fullscreen-skip paths (ArrangeAll,
			// recoverLostMaster, the already-tracked re-arrange) defer
			// their rebuild expecting a later op to retry — adding a real
			// fullscreen-exit rearrange requires allow-listing
			// fullscreen_mode HERE and adding a hub switch case in
			// lockstep; doing either alone is dead code.
			return false
		}
	case "workspace":
		switch ev.Change {
		case "init", "focus":
			return true
		default:
			return false
		}
	case "binding":
		return true
	default:
		return false
	}
}

// logDroppedEvent emits an ERROR with every field needed to reconstruct
// which sway state change the daemon missed. Drops are not transients —
// they are silent corruption sources, since manager tracking diverges
// from the live sway tree the moment a state-change event is missed.
// Identity (con_id, name, app_id), the assigned seq (so journal readers
// can verify a gap), and the running drop counter together make every
// drop a self-contained breadcrumb. If you see this line in production,
// treat it as a real bug — buffer pressure or filter gap — and fix the
// upstream cause, not the symptom.
func logDroppedEvent(logger *slog.Logger, ev sway.Event, n int64) {
	attrs := []any{
		"seq", ev.Seq,
		"drop_count", n,
		"type", ev.Type,
		"change", ev.Change,
	}
	if ev.Container != nil {
		attrs = append(attrs,
			"con_id", ev.Container.ID,
			"name", ev.Container.Name,
			"app_id", ev.Container.AppID,
			"type_con", ev.Container.Type,
		)
		if ws := ev.Container.FindWorkspace(); ws != nil {
			attrs = append(attrs, "workspace", ws.Name)
		}
	}
	if ev.Workspace != nil {
		attrs = append(attrs, "workspace", ev.Workspace.Name)
	}
	if ev.Binding != nil {
		attrs = append(attrs, "binding", ev.Binding.Command)
	}
	logger.Error("event queue full, dropping event (likely real bug — see comment in daemon.go)", attrs...)
}

// logStartupBanner emits a prominent, searchable multi-line block so
// `journalctl | grep 'tilekeeper starting'` always pulls up the exact
// build, config, and log level a session is running under.
func logStartupBanner(logger *slog.Logger, cfg config.Config, dcfg Config) {
	bi := dcfg.Build
	levelName := logging.LevelName(dcfg.LogLevel)
	if dcfg.LogLevelName != "" && dcfg.LogLevelErr == nil {
		levelName = strings.ToUpper(dcfg.LogLevelName)
	}

	logger.Info("═══════════════════ tilekeeper starting ═══════════════════")
	logger.Info("build info",
		"version", bi.Version,
		"commit", bi.Commit,
		"tree", bi.TreeState,
		"built", bi.BuildTime,
		"go", bi.GoVersion,
		"os", runtime.GOOS,
		"arch", runtime.GOARCH,
	)
	logger.Info("runtime",
		"pid", os.Getpid(),
		"ppid", os.Getppid(),
		"uid", os.Getuid(),
		"cwd", getCwd(),
		"exe", getExe(),
	)
	logger.Info("config",
		"path", configPathDisplay(dcfg.ConfigPath),
		"defaultLayout", cfg.General.DefaultLayout,
		"masterWidth", cfg.General.MasterWidth,
		"stackLayout", cfg.General.StackLayout,
		"stackSide", cfg.General.StackSide,
		"visibleStackLimit", cfg.General.VisibleStackLimit,
		"workspaces", len(cfg.Workspaces),
	)
	logger.Info("logging",
		"level", levelName,
		"source", logLevelSource(dcfg),
	)
	logger.Info("═══════════════════════════════════════════════════════════════")
}

func logLevelSource(dcfg Config) string {
	if os.Getenv("TK_LOG_LEVEL") != "" {
		return "env:TK_LOG_LEVEL"
	}
	if dcfg.LogLevelName != "" {
		return "config:logLevel"
	}
	return "default"
}

func configPathDisplay(p string) string {
	if p == "" {
		return "<defaults; no config file found>"
	}
	return p
}

func getCwd() string {
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "?"
}

func getExe() string {
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return "?"
}

// arrangeExisting fetches the sway tree and runs ArrangeAll for each
// managed workspace that already has windows.
func arrangeExisting(client sway.Client, hub *workspace.Hub, logger *slog.Logger) error {
	tree, err := client.GetTree()
	if err != nil {
		return err
	}
	tree.SetParents()

	// Seed wsForCon for every existing leaf so a later cross-workspace
	// move of a pre-existing window hits the tracked path in
	// handleWindowMove and removes the window from the source manager.
	hub.SeedTracking(tree)

	unwedgeObstructedWorkspaces(client, tree, logger)

	for _, ws := range tree.Workspaces() {
		mgr := hub.Manager(ws.Name)
		if mgr == nil {
			continue
		}
		leaves := ws.Leaves()
		if len(leaves) > 0 {
			ids := make([]int64, 0, len(leaves))
			for _, l := range leaves {
				ids = append(ids, l.ID)
			}
			logger.Info("arranging existing windows",
				"workspace", ws.Name, "layout", mgr.Name(), "count", len(leaves), "ids", ids)
			if err := mgr.ArrangeAll(ws); err != nil {
				logger.Error("ArrangeAll failed", "workspace", ws.Name, "error", err)
			}
		}
	}
	return nil
}

// unwedgeObstructedWorkspaces works around sway's silent-return in
// seat_set_workspace_focus when a workspace's focus-inactive points at a
// non-fullscreen sibling of the workspace's fullscreen container (sway
// bug documented in docs/sway-patches.md §1). Symptom: `swaymsg workspace
// number N` returns success but does not switch.
//
// Fix: for each workspace with a fullscreen leaf, issue
// `[con_id=<fs_id>] focus` which sets focus-inactive on that workspace
// back to the fullscreen container (passes sway's obstruction check).
// Then restore the originally-focused workspace so the operator isn't
// dropped somewhere unexpected on restart.
func unwedgeObstructedWorkspaces(client sway.Client, tree *sway.Node, logger *slog.Logger) {
	var fullscreens []struct {
		wsName string
		fsID   int64
	}
	for _, ws := range tree.Workspaces() {
		for _, leaf := range ws.Leaves() {
			if leaf != nil && leaf.FullscreenMode == 1 {
				fullscreens = append(fullscreens, struct {
					wsName string
					fsID   int64
				}{ws.Name, leaf.ID})
				break
			}
		}
	}
	if len(fullscreens) == 0 {
		return
	}

	originalWS := ""
	if wss, err := client.GetWorkspaces(); err == nil {
		for _, w := range wss {
			if w.Focused {
				originalWS = w.Name
				break
			}
		}
	}

	for _, fs := range fullscreens {
		logger.Info("unwedging fullscreen focus",
			"workspace", fs.wsName, "con_id", fs.fsID)
		if err := client.RunCommand(fmt.Sprintf("[con_id=%d] focus", fs.fsID)); err != nil {
			logger.Warn("unwedge focus failed",
				"workspace", fs.wsName, "con_id", fs.fsID, "error", err)
		}
	}

	if originalWS != "" {
		if err := client.RunCommand(fmt.Sprintf("workspace %s", originalWS)); err != nil {
			logger.Warn("restore workspace after unwedge failed",
				"workspace", originalWS, "error", err)
		}
	}
}
