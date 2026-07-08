// Package ipc provides a Unix socket IPC server for tilekeeper.
//
// The server accepts JSON-encoded commands over a Unix domain socket
// and dispatches them to the workspace Hub. This allows the CLI client,
// session manager, and other tools to communicate with the daemon.
//
// Protocol: newline-delimited JSON over a Unix socket.
//
// Request:
//
//	{"command": "swap-master", "workspace": "4"}
//
// Response:
//
//	{"ok": true}
//	{"ok": false, "error": "no manager for workspace 4"}
package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
)

// Request is a JSON command sent to the IPC server.
type Request struct {
	Command   string `json:"command"`
	Workspace string `json:"workspace,omitempty"`
}

// Response is a JSON reply from the IPC server.
type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Data  any    `json:"data,omitempty"`
}

// Handler processes IPC requests.
type Handler interface {
	HandleIPC(req Request) Response
}

// Server listens on a Unix socket and dispatches requests to a Handler.
type Server struct {
	listener net.Listener
	handler  Handler
	logger   *slog.Logger
	done     chan struct{}
	wg       sync.WaitGroup
}

// NewServer creates an IPC server bound to the given socket path.
func NewServer(socketPath string, handler Handler, logger *slog.Logger) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}

	// Ensure parent directory exists
	dir := filepath.Dir(socketPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create socket directory: %w", err)
	}

	// Remove stale socket
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale socket: %w", err)
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", socketPath, err)
	}

	return &Server{
		listener: listener,
		handler:  handler,
		logger:   logger,
		done:     make(chan struct{}),
	}, nil
}

// Serve starts accepting connections. Blocks until Close is called.
func (s *Server) Serve() error {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.done:
				return nil // normal shutdown
			default:
				return fmt.Errorf("accept: %w", err)
			}
		}

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(conn)
		}()
	}
}

// Close shuts down the server and waits for connections to finish.
func (s *Server) Close() error {
	close(s.done)
	err := s.listener.Close()
	s.wg.Wait()
	return err
}

// Addr returns the listener's address (for testing).
func (s *Server) Addr() net.Addr {
	return s.listener.Addr()
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			s.logger.Warn("ipc: invalid JSON", "raw", string(line), "error", err)
			s.writeResponse(conn, Response{OK: false, Error: "invalid JSON"})
			continue
		}

		s.logger.Debug("ipc request",
			"command", req.Command, "workspace", req.Workspace)
		resp := s.handler.HandleIPC(req)
		if resp.OK {
			s.logger.Debug("ipc response",
				"command", req.Command, "ok", true)
		} else {
			s.logger.Warn("ipc response",
				"command", req.Command, "workspace", req.Workspace,
				"ok", false, "error", resp.Error)
		}
		s.writeResponse(conn, resp)
	}
	if err := scanner.Err(); err != nil {
		s.logger.Debug("ipc scanner closed", "error", err)
	}
}

func (s *Server) writeResponse(conn net.Conn, resp Response) {
	data, err := json.Marshal(resp)
	if err != nil {
		s.logger.Error("marshal response", "error", err)
		return
	}
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		s.logger.Debug("write response", "error", err)
	}
}

// DefaultSocketPath returns the default IPC socket path.
func DefaultSocketPath() string {
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return filepath.Join(xdg, "tilekeeper.sock")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("tilekeeper-%d.sock", os.Getuid()))
}

// SendRequest connects to an IPC socket, sends a request, and returns the response.
func SendRequest(socketPath string, req Request) (Response, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return Response{}, fmt.Errorf("connect to %s: %w", socketPath, err)
	}
	defer conn.Close()

	data, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("marshal request: %w", err)
	}
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		return Response{}, fmt.Errorf("write request: %w", err)
	}

	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		return Response{}, fmt.Errorf("no response from server")
	}

	var resp Response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		return Response{}, fmt.Errorf("unmarshal response: %w", err)
	}
	return resp, nil
}
