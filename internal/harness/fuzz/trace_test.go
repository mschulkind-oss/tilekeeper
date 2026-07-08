package fuzz

import (
	"fmt"
	"log/slog"
	"math/rand/v2"
	"testing"

	"github.com/mschulkind-oss/tilekeeper/internal/config"
	"github.com/mschulkind-oss/tilekeeper/internal/harness/sim"
	"github.com/mschulkind-oss/tilekeeper/internal/sway"
	"github.com/mschulkind-oss/tilekeeper/internal/workspace"
)

type recCli struct {
	*sim.SimSwayClient
	t    *testing.T
	on   bool
	step int
}

func (c *recCli) RunCommand(cmd string) error {
	err := c.SimSwayClient.RunCommand(cmd)
	if c.on {
		c.t.Logf("  step=%d cmd=%q err=%v", c.step, cmd, err)
	}
	return err
}

var _ sway.Client = (*recCli)(nil)

type nopW struct{}

func (nopW) Write(p []byte) (int, error) { return len(p), nil }

func traceSeed(t *testing.T, seed uint64, atStep int, preContext int) {
	cfg := DefaultConfig()
	cfg.Seed = seed
	cfg.Steps = atStep
	rng := rand.New(rand.NewPCG(cfg.Seed, cfg.Seed^0x9E3779B97F4A7C15))
	s := sim.New()
	rec := &recCli{SimSwayClient: s, t: t}
	logger := slog.New(slog.NewTextHandler(&nopW{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))

	wsCfg := map[string]config.WorkspaceConfig{}
	for _, name := range cfg.Workspaces {
		wsCfg[name] = config.WorkspaceConfig{DefaultLayout: cfg.DefaultLayout}
	}
	hub := workspace.NewHub(rec, config.Config{
		General:    config.GeneralConfig{DefaultLayout: "none", MasterWidth: 75},
		Workspaces: wsCfg,
	}, logger)
	hub.Initialize()

	state := newFuzzState(cfg.Workspaces)
	res := &Result{Config: cfg}

	for _, ws := range cfg.Workspaces {
		ev := state.initWorkspace(s, ws)
		runStep(hub, s, ev, 0, "workspace:init", true, res)
	}

	for i := 1; i <= cfg.Steps; i++ {
		rec.step = i
		rec.on = i >= atStep-preContext && i <= atStep
		events := state.generateEvents(rng, s, cfg.MaxWindows)
		if len(events) == 0 {
			continue
		}
		ev := events[0]
		if rec.on {
			tree, _ := s.GetTree()
			foc := tree.FindFocused()
			var fid int64
			if foc != nil {
				fid = foc.ID
			}
			// Dump leaf count on the configured workspace to see tree size.
			leafCount := 0
			var leafInfo []string
			for _, ws := range tree.Workspaces() {
				if ws.Name == cfg.Workspaces[0] {
					for _, l := range ws.Leaves() {
						if l.Type == "con" && !l.IsFloating() {
							leafCount++
							parentLayout := "<nil>"
							var parentID int64
							if l.Parent != nil {
								parentLayout = l.Parent.Layout
								parentID = l.Parent.ID
							}
							leafInfo = append(leafInfo, fmt.Sprintf("id=%d parent=%d/%s", l.ID, parentID, parentLayout))
						}
					}
				}
			}
			evCon := int64(0)
			if ev.Container != nil {
				evCon = ev.Container.ID
			}
			// Figure out which ws the event container currently lives on in the tree.
			curWS := ""
			if evCon != 0 {
				if node := tree.FindByID(evCon); node != nil {
					if w := node.FindWorkspace(); w != nil {
						curWS = w.Name
					}
				}
			}
			// Per-workspace leaf and focus snapshot
			var wsSnap []string
			for _, ws := range tree.Workspaces() {
				focusedHere := 0
				for _, l := range ws.Leaves() {
					if l.Focused {
						focusedHere++
					}
				}
				wsSnap = append(wsSnap, fmt.Sprintf("%s:leaves=%d/foc=%d", ws.Name, len(ws.Leaves()), focusedHere))
			}
			t.Logf("step=%d event=%s focused=%d leaves=%d wsForCon=%q curWS=%q wsSnap=%v",
				i, describe(ev), fid, leafCount, hub.WorkspaceForContainer(evCon), curWS, wsSnap)
			if i == atStep {
				for _, info := range leafInfo {
					t.Logf("  leaf %s", info)
				}
			}
		}
		runStep(hub, s, ev, i, describe(ev), true, res)
		if ev.Type == "window" && ev.Change == "close" && ev.Container != nil {
			if live := state.windows[ev.Container.ID]; live != nil {
				s.CloseLeaf(live)
			}
			delete(state.windows, ev.Container.ID)
		}
		// Dispatch any burst tail (dialognew's window::floating) so the
		// stream matches the canonical fuzz loop's per-step semantics.
		for _, tail := range events[1:] {
			if rec.on {
				t.Logf("step=%d event=%s (burst tail)", i, describe(tail))
			}
			runStep(hub, s, tail, i, describe(tail), true, res)
		}
	}
	_ = fmt.Sprintf
}

func TestTraceSeed10At99(t *testing.T)   { traceSeed(t, 10, 99, 30) }
func TestTraceSeed5At109(t *testing.T)   { traceSeed(t, 5, 109, 40) }
func TestTraceSeed1At115(t *testing.T)   { traceSeed(t, 1, 115, 10) }
func TestTraceSeed1At273(t *testing.T)   { traceSeed(t, 1, 273, 250) }
func TestTraceSeed1At321(t *testing.T)   { traceSeed(t, 1, 321, 20) }
func TestTraceSeed1At191(t *testing.T)   { traceSeed(t, 1, 191, 30) }
func TestTraceSeed1At13(t *testing.T)    { traceSeed(t, 1, 13, 12) }
func TestTraceSeed2At136(t *testing.T)   { traceSeed(t, 2, 137, 6) }
func TestTraceSeed63At538(t *testing.T)  { traceSeed(t, 63, 538, 8) }
func TestTraceSeed63At539(t *testing.T)  { traceSeed(t, 63, 539, 1) }
func TestTraceSeed36At7(t *testing.T)    { traceSeed(t, 36, 7, 7) }
func TestTraceSeed3At40(t *testing.T)    { traceSeed(t, 3, 40, 40) }
func TestTraceSeed1At75(t *testing.T)    { traceSeed(t, 1, 75, 75) }
func TestTraceSeed139At146(t *testing.T) { traceSeed(t, 139, 146, 12) }
func TestTraceSeed1At12(t *testing.T)    { traceSeed(t, 1, 12, 12) }

// TestTraceSeed273FollowID1679 traces the missed id 1679 from seed 273.
func TestTraceSeed273FollowID1679(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Seed = 273
	cfg.Steps = 3720
	const target int64 = 1679
	rng := rand.New(rand.NewPCG(cfg.Seed, cfg.Seed^0x9E3779B97F4A7C15))
	s := sim.New()
	logger := slog.New(slog.NewTextHandler(&nopW{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))

	wsCfg := map[string]config.WorkspaceConfig{}
	for _, name := range cfg.Workspaces {
		wsCfg[name] = config.WorkspaceConfig{DefaultLayout: cfg.DefaultLayout}
	}
	hub := workspace.NewHub(s, config.Config{
		General:    config.GeneralConfig{DefaultLayout: "none", MasterWidth: 75},
		Workspaces: wsCfg,
	}, logger)
	hub.Initialize()
	state := newFuzzState(cfg.Workspaces)

	for _, ws := range cfg.Workspaces {
		ev := state.initWorkspace(s, ws)
		hub.HandleEvent(ev)
	}
	for i := 1; i <= cfg.Steps; i++ {
		events := state.generateEvents(rng, s, cfg.MaxWindows)
		if len(events) == 0 {
			continue
		}
		ev := events[0]
		touches := false
		if ev.Container != nil && ev.Container.ID == target {
			touches = true
		}
		if ev.Type == "binding" {
			touches = true // dump bindings always to see layout-set
		}
		if touches {
			tree, _ := s.GetTree()
			node := tree.FindByID(target)
			loc := "<gone>"
			if node != nil {
				if w := node.FindWorkspace(); w != nil {
					loc = w.Name
				}
			}
			t.Logf("step=%d ev=%s tracked7=%v tracked8=%v wsForCon[%d]=%q targetLoc=%s",
				i, describe(ev),
				idsOrNil(hub.Manager("7")), idsOrNil(hub.Manager("8")),
				target, hub.WorkspaceForContainer(target), loc)
		}
		hub.HandleEvent(ev)
		if ev.Type == "window" && ev.Change == "close" && ev.Container != nil {
			if live := state.windows[ev.Container.ID]; live != nil {
				s.CloseLeaf(live)
			}
			delete(state.windows, ev.Container.ID)
		}
		for _, tail := range events[1:] {
			hub.HandleEvent(tail)
		}
	}
}

func idsOrNil(m interface {
	WindowIDs() []int64
}) []int64 {
	if m == nil {
		return nil
	}
	return m.WindowIDs()
}

// TestPanicStackSeed5At109 skips runStep's recover so the Go runtime
// prints a real stack trace for triage.
func TestPanicStackSeed5At109(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Seed = 5
	cfg.Steps = 109
	rng := rand.New(rand.NewPCG(cfg.Seed, cfg.Seed^0x9E3779B97F4A7C15))
	s := sim.New()
	logger := slog.New(slog.NewTextHandler(&nopW{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))

	wsCfg := map[string]config.WorkspaceConfig{}
	for _, name := range cfg.Workspaces {
		wsCfg[name] = config.WorkspaceConfig{DefaultLayout: cfg.DefaultLayout}
	}
	hub := workspace.NewHub(s, config.Config{
		General:    config.GeneralConfig{DefaultLayout: "none", MasterWidth: 75},
		Workspaces: wsCfg,
	}, logger)
	hub.Initialize()

	state := newFuzzState(cfg.Workspaces)

	for _, ws := range cfg.Workspaces {
		ev := state.initWorkspace(s, ws)
		hub.HandleEvent(ev)
	}

	for i := 1; i <= cfg.Steps; i++ {
		events := state.generateEvents(rng, s, cfg.MaxWindows)
		if len(events) == 0 {
			continue
		}
		ev := events[0]
		if i == cfg.Steps {
			t.Logf("about to dispatch step=%d event=%s", i, describe(ev))
		}
		hub.HandleEvent(ev)
		if ev.Type == "window" && ev.Change == "close" && ev.Container != nil {
			if live := state.windows[ev.Container.ID]; live != nil {
				s.CloseLeaf(live)
			}
			delete(state.windows, ev.Container.ID)
		}
		for _, tail := range events[1:] {
			hub.HandleEvent(tail)
		}
	}
}
