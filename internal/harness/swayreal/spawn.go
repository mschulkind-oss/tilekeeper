package swayreal

import (
	"errors"
	"fmt"
	"time"

	"github.com/mschulkind-oss/tilekeeper/internal/sway"
)

// SpawnWindows maps n new tiled windows on the currently focused
// workspace by launching the Wayland client n times via `swaymsg exec`,
// polling get_tree after each launch until the expected leaf count is
// reached. Client startup latency is handled by polling with a per-window
// timeout. The leaves arrive in spawn order, so callers can correlate the
// returned IDs with their spawn index.
//
// Returns the con ids of the newly created leaves (only those that
// appeared during this call), in tree order.
func (s *Sway) SpawnWindows(n int) ([]int64, error) {
	if s.clientBin == "" {
		return nil, errors.New("swayreal: no Wayland client available to spawn windows")
	}
	before, err := s.leafIDs()
	if err != nil {
		return nil, err
	}
	want := len(before) + n
	for i := 0; i < n; i++ {
		// `exec` runs the command through sh -c; the client maps an
		// xdg-toplevel that sway tiles into the focused workspace.
		if err := s.RunCommand("exec " + s.clientBin); err != nil {
			return nil, fmt.Errorf("swayreal: exec client %q: %w", s.clientBin, err)
		}
		target := len(before) + i + 1
		if err := s.waitForLeafCount(target, 10*time.Second); err != nil {
			return nil, fmt.Errorf("swayreal: waiting for window %d/%d: %w", i+1, n, err)
		}
	}
	after, err := s.leafIDs()
	if err != nil {
		return nil, err
	}
	if len(after) != want {
		return nil, fmt.Errorf("swayreal: expected %d leaves after spawning %d, got %d", want, n, len(after))
	}
	// Compute the set difference (after − before), preserving tree order.
	beforeSet := map[int64]bool{}
	for _, id := range before {
		beforeSet[id] = true
	}
	var fresh []int64
	for _, id := range after {
		if !beforeSet[id] {
			fresh = append(fresh, id)
		}
	}
	return fresh, nil
}

// waitForLeafCount polls until the tree shows at least `want` tiled+floating
// leaf windows, or the timeout elapses.
func (s *Sway) waitForLeafCount(want int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last int
	for {
		ids, err := s.leafIDs()
		if err == nil {
			last = len(ids)
			if last >= want {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("leaf count reached %d, wanted %d, within %s", last, want, timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// leafIDs returns the con ids of every application-window leaf in the tree
// (tiled and floating), in depth-first tree order. A leaf is a "con" or
// "floating_con" with no child nodes that maps to an actual surface
// (Name set). Empty placeholder workspaces produce no leaves.
func (s *Sway) leafIDs() ([]int64, error) {
	tree, err := s.GetTree()
	if err != nil {
		return nil, err
	}
	var ids []int64
	var walk func(n *sway.Node)
	walk = func(n *sway.Node) {
		isLeaf := (n.Type == "con" || n.Type == "floating_con") &&
			len(n.Nodes) == 0 && len(n.FloatingNodes) == 0
		if isLeaf && n.Name != "" {
			ids = append(ids, n.ID)
		}
		for _, c := range n.Nodes {
			walk(c)
		}
		for _, c := range n.FloatingNodes {
			walk(c)
		}
	}
	walk(tree)
	return ids, nil
}

// FocusWorkspace switches sway to the named workspace.
func (s *Sway) FocusWorkspace(name string) error {
	return s.RunCommand("workspace " + name)
}

// LeafCount returns the number of application-window leaves currently in
// the tree (tiled + floating).
func (s *Sway) LeafCount() (int, error) {
	ids, err := s.leafIDs()
	if err != nil {
		return 0, err
	}
	return len(ids), nil
}

// KillAllLeaves sends `kill` to every current window leaf. Asynchronous:
// the client process must still exit and sway must emit window::close, so
// pair with WaitForLeafCount(0, …) to confirm the tree drained.
func (s *Sway) KillAllLeaves() error {
	ids, err := s.leafIDs()
	if err != nil {
		return err
	}
	for _, id := range ids {
		_ = s.RunCommand(fmt.Sprintf("[con_id=%d] kill", id))
	}
	return nil
}

// WaitForLeafCount blocks until the tree shows exactly `want` window leaves
// (used after teardown to confirm the workspace drained), or the timeout
// elapses. Unlike the internal poller it requires an exact match, so a
// scenario that killed its windows can confirm a clean slate before the
// next spawn.
func (s *Sway) WaitForLeafCount(want int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last int
	for {
		ids, err := s.leafIDs()
		if err == nil {
			last = len(ids)
			if last == want {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("leaf count is %d, wanted exactly %d, within %s", last, want, timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
