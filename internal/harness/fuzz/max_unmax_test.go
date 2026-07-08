package fuzz

import (
	"log/slog"
	"testing"

	"github.com/mschulkind-oss/tilekeeper/internal/config"
	"github.com/mschulkind-oss/tilekeeper/internal/harness/sim"
	"github.com/mschulkind-oss/tilekeeper/internal/sway"
	"github.com/mschulkind-oss/tilekeeper/internal/workspace"
)

// TestMaxUnmaxAccumulatesWrappers exercises the MasterStack maximize
// binding repeatedly to see if wrappers persist. Each maximize flips the
// master's parent to layout=tabbed; unmaximize calls arrangeWindows which
// can leave the old wrapper in place because flattenWorkspace (move to
// workspace <self>) is a no-op in real sway.
func TestMaxUnmaxAccumulatesWrappers(t *testing.T) {
	s := sim.New()
	logger := slog.New(slog.NewTextHandler(&nopW{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	hub := workspace.NewHub(s, config.Config{
		General:    config.GeneralConfig{DefaultLayout: "none", MasterWidth: 75},
		Workspaces: map[string]config.WorkspaceConfig{"7": {DefaultLayout: "MasterStack"}},
	}, logger)
	hub.Initialize()

	state := newFuzzState([]string{"7"})
	hub.HandleEvent(state.initWorkspace(s, "7"))

	// Add 4 windows.
	for range 4 {
		hub.HandleEvent(state.genNew(s, state.workspaces["7"], 100))
	}

	for iter := range 10 {
		// Toggle max: binding "nop tilekeeper maximize" → handleLayoutCommand "maximize".
		hub.HandleEvent(sway.Event{
			Type:    "binding",
			Change:  "run",
			Binding: &sway.Binding{Command: "nop tilekeeper maximize"},
		})
		// Toggle again to unmax.
		hub.HandleEvent(sway.Event{
			Type:    "binding",
			Change:  "run",
			Binding: &sway.Binding{Command: "nop tilekeeper maximize"},
		})

		tree, _ := s.GetTree()
		depth, path := longestSingletonChain(tree)
		t.Logf("iter=%d singleton-chain depth=%d path=%s", iter, depth, path)
	}
}
