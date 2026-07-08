package sway

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mschulkind-oss/tilekeeper/internal/logging"
)

// Sway IPC message types.
const (
	msgRunCommand     uint32 = 0
	msgGetWorkspaces  uint32 = 1
	msgSubscribe      uint32 = 2
	msgGetTree        uint32 = 4
	msgEventMask      uint32 = 0x80000000
	msgEventWorkspace uint32 = msgEventMask // workspace event = type 0
	msgEventWindow    uint32 = msgEventMask | 3
	msgEventBinding   uint32 = msgEventMask | 5
)

// ipcMagic is the protocol header prefix.
var ipcMagic = []byte("i3-ipc")

// Conn is a real sway IPC client connected via SWAYSOCK.
type Conn struct {
	mu       sync.Mutex
	sockPath string
	conn     net.Conn
	logger   *slog.Logger // may be nil; accessed via log()
}

// SocketPath returns the unix socket path this connection was dialed on.
func (c *Conn) SocketPath() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sockPath
}

// SetLogger attaches a logger to this connection. Safe to call after
// Connect(); nil silences all IPC-level logging.
func (c *Conn) SetLogger(l *slog.Logger) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logger = l
}

// log returns the configured logger or a silent no-op logger.
func (c *Conn) log() *slog.Logger {
	if c.logger == nil {
		return slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	}
	return c.logger
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// Connect creates a new Conn to a running sway instance. It prefers
// $SWAYSOCK when that socket is live, and falls back to scanning
// /run/user/$UID/sway-ipc.*.sock for a socket whose PID-suffixed name
// matches a running sway process. This keeps the daemon connectable
// after sway restarts under systemd — systemd user services inherit a
// frozen $SWAYSOCK from the user manager's environment, so a stale env
// var is the common failure mode.
func Connect() (*Conn, error) {
	if sock := os.Getenv("SWAYSOCK"); sock != "" {
		if c, err := ConnectTo(sock); err == nil {
			return c, nil
		}
	}
	sock, err := DiscoverSocket()
	if err != nil {
		return nil, err
	}
	return ConnectTo(sock)
}

// DiscoverSocket returns the path of a live sway IPC socket under
// /run/user/$UID, preferring one whose PID-suffixed name corresponds
// to a running sway process. The socket filename format produced by
// sway is sway-ipc.<uid>.<pid>.sock.
func DiscoverSocket() (string, error) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	matches, err := filepath.Glob(filepath.Join(dir, "sway-ipc.*.sock"))
	if err != nil {
		return "", fmt.Errorf("scanning %s for sway sockets: %w", dir, err)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no sway-ipc sockets under %s — is sway running?", dir)
	}
	// Prefer sockets whose embedded PID is a live sway process. If
	// several qualify, take the most recently modified.
	type candidate struct {
		path string
		mod  time.Time
	}
	var live, stale []candidate
	for _, p := range matches {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		c := candidate{path: p, mod: info.ModTime()}
		if pidFromSockName(p) > 0 && isSwayProcess(pidFromSockName(p)) {
			live = append(live, c)
		} else {
			stale = append(stale, c)
		}
	}
	pool := live
	if len(pool) == 0 {
		pool = stale
	}
	if len(pool) == 0 {
		return "", fmt.Errorf("no usable sway-ipc sockets under %s", dir)
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].mod.After(pool[j].mod) })
	return pool[0].path, nil
}

// pidFromSockName extracts the PID from sway-ipc.<uid>.<pid>.sock. Returns 0 if
// the filename does not match that pattern.
func pidFromSockName(path string) int {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, ".sock")
	parts := strings.Split(base, ".")
	if len(parts) < 3 || parts[0] != "sway-ipc" {
		return 0
	}
	pid, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// isSwayProcess returns true if /proc/<pid>/comm exists and reports "sway".
func isSwayProcess(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == "sway"
}

// ConnectTo creates a new Conn to the given unix socket path.
func ConnectTo(sockPath string) (*Conn, error) {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("connecting to sway socket %s: %w", sockPath, err)
	}
	return &Conn{sockPath: sockPath, conn: conn}, nil
}

// Close shuts down the connection.
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

func (c *Conn) GetTree() (*Node, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	resp, err := c.roundTrip(msgGetTree, nil)
	if err != nil {
		return nil, fmt.Errorf("get_tree: %w", err)
	}

	var root Node
	if err := json.Unmarshal(resp, &root); err != nil {
		return nil, fmt.Errorf("parsing tree: %w", err)
	}
	root.SetParents()
	return &root, nil
}

func (c *Conn) RunCommand(cmd string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	start := time.Now()
	logging.Trace(c.logger, "ipc send run_command", "cmd", cmd)

	resp, err := c.roundTrip(msgRunCommand, []byte(cmd))
	if err != nil {
		c.log().Error("run_command transport error", "cmd", cmd, "error", err)
		return fmt.Errorf("run_command: %w", err)
	}

	var results []commandResult
	if err := json.Unmarshal(resp, &results); err != nil {
		c.log().Error("run_command parse error", "cmd", cmd, "error", err, "raw", string(resp))
		return fmt.Errorf("parsing command result: %w", err)
	}

	elapsed := time.Since(start)
	for _, r := range results {
		if !r.Success {
			c.log().Warn("sway rejected command", "cmd", cmd, "error", r.Error, "elapsed", elapsed)
			return fmt.Errorf("sway command failed: %s", r.Error)
		}
	}
	c.log().Debug("run_command", "cmd", cmd, "elapsed", elapsed)
	return nil
}

func (c *Conn) Subscribe(eventTypes []string, handler EventHandler) error {
	// Subscribe needs its own connection because it blocks reading events.
	subConn, err := net.Dial("unix", c.sockPath)
	if err != nil {
		return fmt.Errorf("subscribe connection: %w", err)
	}

	payload, err := json.Marshal(eventTypes)
	if err != nil {
		return fmt.Errorf("marshaling event types: %w", err)
	}

	if err := writeMessage(subConn, msgSubscribe, payload); err != nil {
		subConn.Close()
		return fmt.Errorf("sending subscribe: %w", err)
	}

	resp, _, err := readMessage(subConn)
	if err != nil {
		subConn.Close()
		return fmt.Errorf("reading subscribe response: %w", err)
	}

	var result commandResult
	if err := json.Unmarshal(resp, &result); err != nil {
		subConn.Close()
		return fmt.Errorf("parsing subscribe response: %w", err)
	}
	if !result.Success {
		subConn.Close()
		return fmt.Errorf("subscribe failed: %s", result.Error)
	}

	c.log().Info("subscribed", "types", eventTypes)

	// Read events in a goroutine.
	go func() {
		defer subConn.Close()
		for {
			payload, msgType, err := readMessage(subConn)
			if err != nil {
				c.log().Error("event stream closed", "error", err)
				return // connection closed
			}
			event := parseEvent(msgType, payload)
			if event != nil {
				logging.Trace(c.logger, "ipc event",
					"type", event.Type, "change", event.Change,
					"con_id", eventConID(event),
					"workspace", eventWSName(event))
				handler(*event)
			} else {
				logging.Trace(c.logger, "ipc event (unhandled type)", "msgType", fmt.Sprintf("0x%x", msgType), "bytes", len(payload))
			}
		}
	}()

	return nil
}

// eventConID returns a best-effort container id for trace logs.
func eventConID(ev *Event) int64 {
	if ev.Container != nil {
		return ev.Container.ID
	}
	return 0
}

// eventWSName returns a best-effort workspace name for trace logs.
func eventWSName(ev *Event) string {
	if ev.Workspace != nil {
		return ev.Workspace.Name
	}
	if ev.Container != nil {
		if ws := ev.Container.FindWorkspace(); ws != nil {
			return ws.Name
		}
	}
	return ""
}

func (c *Conn) GetWorkspaces() ([]Workspace, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	resp, err := c.roundTrip(msgGetWorkspaces, nil)
	if err != nil {
		return nil, fmt.Errorf("get_workspaces: %w", err)
	}

	var workspaces []Workspace
	if err := json.Unmarshal(resp, &workspaces); err != nil {
		return nil, fmt.Errorf("parsing workspaces: %w", err)
	}
	return workspaces, nil
}

// roundTrip sends a message and reads the response. Caller must hold mu.
func (c *Conn) roundTrip(msgType uint32, payload []byte) ([]byte, error) {
	if err := writeMessage(c.conn, msgType, payload); err != nil {
		return nil, err
	}
	resp, _, err := readMessage(c.conn)
	return resp, err
}

// writeMessage writes a framed IPC message to the connection.
func writeMessage(conn net.Conn, msgType uint32, payload []byte) error {
	var buf bytes.Buffer
	buf.Write(ipcMagic)
	if err := binary.Write(&buf, binary.LittleEndian, uint32(len(payload))); err != nil {
		return err
	}
	if err := binary.Write(&buf, binary.LittleEndian, msgType); err != nil {
		return err
	}
	buf.Write(payload)
	_, err := conn.Write(buf.Bytes())
	return err
}

// readMessage reads a framed IPC message from the connection.
func readMessage(conn net.Conn) (payload []byte, msgType uint32, err error) {
	// Read header: 6 bytes magic + 4 bytes length + 4 bytes type = 14 bytes
	header := make([]byte, 14)
	if _, err := readFull(conn, header); err != nil {
		return nil, 0, fmt.Errorf("reading header: %w", err)
	}

	if !bytes.Equal(header[:6], ipcMagic) {
		return nil, 0, fmt.Errorf("invalid IPC magic: %x", header[:6])
	}

	length := binary.LittleEndian.Uint32(header[6:10])
	msgType = binary.LittleEndian.Uint32(header[10:14])

	if length > 0 {
		payload = make([]byte, length)
		if _, err := readFull(conn, payload); err != nil {
			return nil, 0, fmt.Errorf("reading payload: %w", err)
		}
	}

	return payload, msgType, nil
}

// readFull reads exactly len(buf) bytes from the connection.
func readFull(conn net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := conn.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// parseEvent converts a raw IPC event message into an Event.
func parseEvent(msgType uint32, payload []byte) *Event {
	switch msgType {
	case msgEventWindow:
		return parseWindowEvent(payload)
	case msgEventWorkspace:
		return parseWorkspaceEvent(payload)
	case msgEventBinding:
		return parseBindingEvent(payload)
	default:
		return nil
	}
}

func parseWindowEvent(payload []byte) *Event {
	var raw struct {
		Change    string `json:"change"`
		Container Node   `json:"container"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil
	}
	return &Event{
		Type:      "window",
		Change:    raw.Change,
		Container: &raw.Container,
	}
}

func parseWorkspaceEvent(payload []byte) *Event {
	var raw struct {
		Change  string `json:"change"`
		Current Node   `json:"current"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil
	}
	return &Event{
		Type:      "workspace",
		Change:    raw.Change,
		Workspace: &raw.Current,
	}
}

func parseBindingEvent(payload []byte) *Event {
	var raw struct {
		Change  string `json:"change"`
		Binding struct {
			Command string `json:"command"`
		} `json:"binding"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil
	}
	return &Event{
		Type:   "binding",
		Change: raw.Change,
		Binding: &Binding{
			Command: raw.Binding.Command,
		},
	}
}

type commandResult struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// Verify interface compliance at compile time.
var _ Client = (*Conn)(nil)
