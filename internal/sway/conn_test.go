package sway

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteMessage(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		writeMessage(client, msgRunCommand, []byte("test command"))
	}()

	header := make([]byte, 14)
	readFull(server, header)

	if !bytes.Equal(header[:6], ipcMagic) {
		t.Errorf("magic = %x, want %x", header[:6], ipcMagic)
	}

	length := binary.LittleEndian.Uint32(header[6:10])
	if length != 12 {
		t.Errorf("length = %d, want 12", length)
	}

	msgType := binary.LittleEndian.Uint32(header[10:14])
	if msgType != msgRunCommand {
		t.Errorf("msgType = %d, want %d", msgType, msgRunCommand)
	}

	payload := make([]byte, length)
	readFull(server, payload)
	if string(payload) != "test command" {
		t.Errorf("payload = %q, want %q", string(payload), "test command")
	}
}

func TestReadMessage(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	want := []byte(`{"success":true}`)
	go func() {
		writeMessage(server, msgRunCommand, want)
	}()

	payload, msgType, err := readMessage(client)
	if err != nil {
		t.Fatalf("readMessage error: %v", err)
	}
	if msgType != msgRunCommand {
		t.Errorf("msgType = %d, want %d", msgType, msgRunCommand)
	}
	if string(payload) != string(want) {
		t.Errorf("payload = %q, want %q", string(payload), string(want))
	}
}

func TestReadMessageInvalidMagic(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		server.Write([]byte("badmagXXXXYYYY"))
	}()

	_, _, err := readMessage(client)
	if err == nil {
		t.Fatal("expected error for invalid magic")
	}
}

func TestParseWindowEvent(t *testing.T) {
	payload := `{"change":"new","container":{"id":42,"name":"test","type":"con"}}`
	event := parseWindowEvent([]byte(payload))

	if event == nil {
		t.Fatal("expected non-nil event")
	}
	if event.Type != "window" {
		t.Errorf("type = %q, want %q", event.Type, "window")
	}
	if event.Change != "new" {
		t.Errorf("change = %q, want %q", event.Change, "new")
	}
	if event.Container.ID != 42 {
		t.Errorf("container.ID = %d, want 42", event.Container.ID)
	}
}

func TestParseBindingEvent(t *testing.T) {
	payload := `{"change":"run","binding":{"command":"nop tilekeeper swap-master"}}`
	event := parseBindingEvent([]byte(payload))

	if event == nil {
		t.Fatal("expected non-nil event")
	}
	if event.Type != "binding" {
		t.Errorf("type = %q, want %q", event.Type, "binding")
	}
	if event.Binding.Command != "nop tilekeeper swap-master" {
		t.Errorf("binding.command = %q, want %q", event.Binding.Command, "nop tilekeeper swap-master")
	}
}

func TestParseWorkspaceEvent(t *testing.T) {
	payload := `{"change":"init","current":{"id":1,"name":"3","type":"workspace"}}`
	event := parseWorkspaceEvent([]byte(payload))

	if event == nil {
		t.Fatal("expected non-nil event")
	}
	if event.Type != "workspace" {
		t.Errorf("type = %q, want %q", event.Type, "workspace")
	}
	if event.Workspace.Name != "3" {
		t.Errorf("workspace.name = %q, want %q", event.Workspace.Name, "3")
	}
}

func TestParseEventUnknownType(t *testing.T) {
	event := parseEvent(0x80000099, []byte("{}"))
	if event != nil {
		t.Error("expected nil for unknown event type")
	}
}

func TestParseWindowEventInvalidJSON(t *testing.T) {
	event := parseWindowEvent([]byte("not json"))
	if event != nil {
		t.Error("expected nil for invalid JSON")
	}
}

func TestConnectNoSwaysock(t *testing.T) {
	// Point both SWAYSOCK and XDG_RUNTIME_DIR at empty/bogus locations so
	// neither the env-var path nor the discovery fallback can succeed.
	t.Setenv("SWAYSOCK", "")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	_, err := Connect()
	if err == nil {
		t.Fatal("expected error when SWAYSOCK unset and no sockets discoverable")
	}
}

func TestDiscoverSocketEmpty(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	if _, err := DiscoverSocket(); err == nil {
		t.Fatal("expected error when runtime dir has no sway sockets")
	}
}

func TestDiscoverSocketPrefersLivePID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)

	// A socket tagged with our own PID: /proc/self/comm is the test
	// binary name, so isSwayProcess() returns false and this counts as
	// stale. A socket tagged with a clearly-dead PID should also be
	// stale. Absent a live sway, DiscoverSocket falls back to the stale
	// pool and returns the newest by mtime.
	older := filepath.Join(dir, "sway-ipc.1000.1.sock")
	newer := filepath.Join(dir, fmt.Sprintf("sway-ipc.1000.%d.sock", os.Getpid()))
	for _, p := range []string{older, newer} {
		f, err := os.Create(p)
		if err != nil {
			t.Fatalf("create %s: %v", p, err)
		}
		f.Close()
	}
	// Force mtime ordering so "newer" wins.
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(older, past, past); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	got, err := DiscoverSocket()
	if err != nil {
		t.Fatalf("DiscoverSocket: %v", err)
	}
	if got != newer {
		t.Fatalf("expected newest socket %s, got %s", newer, got)
	}
}

func TestConnectToBadPath(t *testing.T) {
	_, err := ConnectTo("/nonexistent/path/sway-ipc.sock")
	if err == nil {
		t.Fatal("expected error for bad socket path")
	}
}

// TestRoundTripWithFakeServer tests the full send/receive cycle using a
// fake sway server on a unix socket.
func TestRoundTripWithFakeServer(t *testing.T) {
	sockDir := t.TempDir()
	sockPath := filepath.Join(sockDir, "sway.sock")

	// Start a fake server that echoes back a success response
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	treeResponse := Node{
		ID:   1,
		Type: "root",
		Nodes: []*Node{
			{ID: 2, Type: "output", Name: "eDP-1"},
		},
	}
	treeJSON, _ := json.Marshal(treeResponse)

	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				for {
					_, msgType, err := readMessage(c)
					if err != nil {
						return
					}
					switch msgType {
					case msgGetTree:
						writeMessage(c, msgGetTree, treeJSON)
					case msgRunCommand:
						writeMessage(c, msgRunCommand, []byte(`[{"success":true}]`))
					case msgGetWorkspaces:
						writeMessage(c, msgGetWorkspaces, []byte(`[{"num":1,"name":"1","focused":true}]`))
					}
				}
			}(c)
		}
	}()

	// Now connect as client
	t.Setenv("SWAYSOCK", sockPath)
	conn, err := ConnectTo(sockPath)
	if err != nil {
		t.Fatalf("ConnectTo: %v", err)
	}
	defer conn.Close()

	// Test GetTree
	tree, err := conn.GetTree()
	if err != nil {
		t.Fatalf("GetTree: %v", err)
	}
	if tree.ID != 1 {
		t.Errorf("tree.ID = %d, want 1", tree.ID)
	}
	if len(tree.Nodes) != 1 {
		t.Fatalf("tree.Nodes len = %d, want 1", len(tree.Nodes))
	}

	// Test RunCommand
	if err := conn.RunCommand("test cmd"); err != nil {
		t.Errorf("RunCommand: %v", err)
	}

	// Test GetWorkspaces
	ws, err := conn.GetWorkspaces()
	if err != nil {
		t.Fatalf("GetWorkspaces: %v", err)
	}
	if len(ws) != 1 || ws[0].Name != "1" {
		t.Errorf("workspaces = %+v, want [{Name:1}]", ws)
	}
}

func TestRunCommandFailure(t *testing.T) {
	sockDir := t.TempDir()
	sockPath := filepath.Join(sockDir, "sway.sock")

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	go func() {
		c, err := listener.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		readMessage(c)
		writeMessage(c, msgRunCommand, []byte(`[{"success":false,"error":"test error"}]`))
	}()

	conn, err := ConnectTo(sockPath)
	if err != nil {
		t.Fatalf("ConnectTo: %v", err)
	}
	defer conn.Close()

	err = conn.RunCommand("bad cmd")
	if err == nil {
		t.Fatal("expected error for failed command")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("test error")) {
		t.Errorf("error = %q, want to contain 'test error'", err)
	}
}

func TestSubscribeWithFakeServer(t *testing.T) {
	sockDir := t.TempDir()
	sockPath := filepath.Join(sockDir, "sway.sock")

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	windowEvent := `{"change":"new","container":{"id":42,"name":"test","type":"con"}}`

	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _, err := readMessage(c)
				if err != nil {
					return
				}
				// Send subscribe success
				writeMessage(c, msgSubscribe, []byte(`{"success":true}`))
				// Send a window event
				writeMessage(c, msgEventWindow, []byte(windowEvent))
			}(c)
		}
	}()

	conn, err := ConnectTo(sockPath)
	if err != nil {
		t.Fatalf("ConnectTo: %v", err)
	}
	defer conn.Close()

	received := make(chan Event, 1)
	err = conn.Subscribe([]string{"window"}, func(ev Event) {
		received <- ev
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	ev := <-received
	if ev.Type != "window" {
		t.Errorf("event.Type = %q, want %q", ev.Type, "window")
	}
	if ev.Container.ID != 42 {
		t.Errorf("container.ID = %d, want 42", ev.Container.ID)
	}
}

func TestWriteMessageEmptyPayload(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		writeMessage(client, msgGetTree, nil)
	}()

	payload, msgType, err := readMessage(server)
	if err != nil {
		t.Fatalf("readMessage: %v", err)
	}
	if msgType != msgGetTree {
		t.Errorf("msgType = %d, want %d", msgType, msgGetTree)
	}
	if len(payload) != 0 {
		t.Errorf("payload len = %d, want 0", len(payload))
	}
}

func TestConnectEnvVar(t *testing.T) {
	sockDir := t.TempDir()
	sockPath := filepath.Join(sockDir, "sway.sock")

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	t.Setenv("SWAYSOCK", sockPath)
	conn, err := Connect()
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	conn.Close()
}

func TestCloseNilConn(t *testing.T) {
	c := &Conn{}
	err := c.Close()
	if err != nil {
		t.Errorf("Close nil conn: %v", err)
	}
}

func TestMain(m *testing.M) {
	// Reset ID counter before test suite
	ResetIDCounter()
	os.Exit(m.Run())
}
