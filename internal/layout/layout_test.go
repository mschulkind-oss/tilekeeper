package layout

import (
	"testing"

	"github.com/mschulkind-oss/tilekeeper/internal/sway"
)

func TestIsExcluded(t *testing.T) {
	tests := []struct {
		name   string
		window *sway.Node
		want   bool
	}{
		{"nil window", nil, true},
		{"normal con", &sway.Node{Type: "con"}, false},
		{"workspace type", &sway.Node{Type: "workspace"}, true},
		{"floating_con type", &sway.Node{Type: "floating_con"}, true},
		{"floating auto_on", &sway.Node{Type: "con", Floating: "auto_on"}, true},
		{"floating user_on", &sway.Node{Type: "con", Floating: "user_on"}, true},
		{"fullscreen", &sway.Node{Type: "con", FullscreenMode: 1}, true},
		{"tabbed parent", &sway.Node{
			Type:   "con",
			Parent: &sway.Node{Layout: "tabbed"},
		}, true},
		{"stacked parent", &sway.Node{
			Type:   "con",
			Parent: &sway.Node{Layout: "stacked"},
		}, true},
		{"normal parent", &sway.Node{
			Type:   "con",
			Parent: &sway.Node{Layout: "splith"},
		}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsExcluded(tt.window)
			if got != tt.want {
				t.Errorf("IsExcluded(%v) = %v, want %v", tt.window, got, tt.want)
			}
		})
	}
}

func TestTilingLeaves(t *testing.T) {
	t.Run("nil workspace", func(t *testing.T) {
		leaves := TilingLeaves(nil)
		if leaves != nil {
			t.Errorf("expected nil, got %v", leaves)
		}
	})

	t.Run("workspace with windows", func(t *testing.T) {
		sway.ResetIDCounter()
		ws := sway.CreateWorkspace("1", 3)
		leaves := TilingLeaves(ws)
		if len(leaves) != 3 {
			t.Errorf("got %d leaves, want 3", len(leaves))
		}
	})
}

func TestFloatingLeaves(t *testing.T) {
	t.Run("nil workspace", func(t *testing.T) {
		leaves := FloatingLeaves(nil)
		if leaves != nil {
			t.Errorf("expected nil, got %v", leaves)
		}
	})

	t.Run("workspace with floating", func(t *testing.T) {
		sway.ResetIDCounter()
		ws := sway.CreateWorkspace("1", 2, 1)
		leaves := FloatingLeaves(ws)
		if len(leaves) != 1 {
			t.Errorf("got %d floating leaves, want 1", len(leaves))
		}
	})

	t.Run("workspace no floating", func(t *testing.T) {
		sway.ResetIDCounter()
		ws := sway.CreateWorkspace("1", 2)
		leaves := FloatingLeaves(ws)
		if len(leaves) != 0 {
			t.Errorf("got %d floating leaves, want 0", len(leaves))
		}
	})
}
