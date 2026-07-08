package ipc

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testHandler is a simple IPC handler for testing.
type testHandler struct {
	lastReq   Request
	response  Response
	callCount int
}

func (h *testHandler) HandleIPC(req Request) Response {
	h.lastReq = req
	h.callCount++
	return h.response
}

func TestNewServerAndServe(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")
	handler := &testHandler{response: Response{OK: true}}

	srv, err := NewServer(sock, handler, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	// Serve in background
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve() }()

	// Wait for listener to be ready
	time.Sleep(10 * time.Millisecond)

	// Connect and send a request
	resp, err := SendRequest(sock, Request{Command: "swap-master", Workspace: "4"})
	if err != nil {
		t.Fatalf("SendRequest: %v", err)
	}
	if !resp.OK {
		t.Error("expected OK response")
	}
	if handler.lastReq.Command != "swap-master" {
		t.Errorf("command = %q, want swap-master", handler.lastReq.Command)
	}
	if handler.lastReq.Workspace != "4" {
		t.Errorf("workspace = %q, want 4", handler.lastReq.Workspace)
	}
}

func TestMultipleRequests(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")
	handler := &testHandler{response: Response{OK: true}}

	srv, err := NewServer(sock, handler, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()
	go srv.Serve()
	time.Sleep(10 * time.Millisecond)

	// Multiple requests on same connection
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	for _, cmd := range []string{"swap-master", "rotate cw", "master grow"} {
		req, _ := json.Marshal(Request{Command: cmd})
		req = append(req, '\n')
		conn.Write(req)

		buf := make([]byte, 4096)
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		var resp Response
		json.Unmarshal(buf[:n], &resp)
		if !resp.OK {
			t.Errorf("command %q: not OK", cmd)
		}
	}

	if handler.callCount != 3 {
		t.Errorf("callCount = %d, want 3", handler.callCount)
	}
}

func TestErrorResponse(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")
	handler := &testHandler{response: Response{OK: false, Error: "no such workspace"}}

	srv, err := NewServer(sock, handler, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()
	go srv.Serve()
	time.Sleep(10 * time.Millisecond)

	resp, err := SendRequest(sock, Request{Command: "swap-master", Workspace: "99"})
	if err != nil {
		t.Fatalf("SendRequest: %v", err)
	}
	if resp.OK {
		t.Error("expected error response")
	}
	if resp.Error != "no such workspace" {
		t.Errorf("error = %q, want 'no such workspace'", resp.Error)
	}
}

func TestInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")
	handler := &testHandler{response: Response{OK: true}}

	srv, err := NewServer(sock, handler, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()
	go srv.Serve()
	time.Sleep(10 * time.Millisecond)

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	// Send invalid JSON
	conn.Write([]byte("not json\n"))
	buf := make([]byte, 4096)
	n, _ := conn.Read(buf)
	var resp Response
	json.Unmarshal(buf[:n], &resp)
	if resp.OK {
		t.Error("expected error for invalid JSON")
	}
	if resp.Error != "invalid JSON" {
		t.Errorf("error = %q", resp.Error)
	}

	// Handler should not have been called
	if handler.callCount != 0 {
		t.Errorf("handler was called for invalid JSON")
	}
}

func TestSendRequestToNonExistentSocket(t *testing.T) {
	_, err := SendRequest("/nonexistent/path.sock", Request{Command: "test"})
	if err == nil {
		t.Error("expected error connecting to non-existent socket")
	}
}

func TestServerClose(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")
	handler := &testHandler{response: Response{OK: true}}

	srv, err := NewServer(sock, handler, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve() }()
	time.Sleep(10 * time.Millisecond)

	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Serve returned error after close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not return after close")
	}
}

func TestStaleSocketCleanup(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")

	// Create a stale file
	os.WriteFile(sock, []byte("stale"), 0o600)

	handler := &testHandler{response: Response{OK: true}}
	srv, err := NewServer(sock, handler, nil)
	if err != nil {
		t.Fatalf("NewServer should clean stale socket: %v", err)
	}
	srv.Close()
}

func TestDefaultSocketPath(t *testing.T) {
	path := DefaultSocketPath()
	if path == "" {
		t.Error("DefaultSocketPath returned empty string")
	}
}

func TestServerAddr(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")
	handler := &testHandler{response: Response{OK: true}}

	srv, err := NewServer(sock, handler, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	addr := srv.Addr()
	if addr == nil {
		t.Error("Addr returned nil")
	}
}
