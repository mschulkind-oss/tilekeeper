package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.General.DefaultLayout != "none" {
		t.Errorf("expected default layout 'none', got %q", cfg.General.DefaultLayout)
	}
	if cfg.General.MasterWidth != 50 {
		t.Errorf("expected master width 50, got %d", cfg.General.MasterWidth)
	}
	if cfg.General.StackSide != "right" {
		t.Errorf("expected stack side 'right', got %q", cfg.General.StackSide)
	}
}

func TestLoadMinimalFixture(t *testing.T) {
	cfg, err := Load("testdata/minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.General.DefaultLayout != "none" {
		t.Errorf("defaultLayout: got %q, want 'none'", cfg.General.DefaultLayout)
	}
	// Defaults should fill in
	if cfg.General.MasterWidth != 50 {
		t.Errorf("masterWidth default: got %d, want 50", cfg.General.MasterWidth)
	}
}

func TestLoadMasterStackFixture(t *testing.T) {
	cfg, err := Load("testdata/masterstack.toml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.General.DefaultLayout != "MasterStack" {
		t.Errorf("defaultLayout: got %q, want 'MasterStack'", cfg.General.DefaultLayout)
	}
	if cfg.General.MasterWidth != 60 {
		t.Errorf("masterWidth: got %d, want 60", cfg.General.MasterWidth)
	}
	if cfg.General.VisibleStackLimit != 3 {
		t.Errorf("visibleStackLimit: got %d, want 3", cfg.General.VisibleStackLimit)
	}

	// Workspace overrides
	if len(cfg.Workspaces) != 3 {
		t.Fatalf("expected 3 workspace overrides, got %d", len(cfg.Workspaces))
	}
	ws1 := cfg.Workspaces["1"]
	if ws1.MasterWidth != 55 {
		t.Errorf("workspace 1 masterWidth: got %d, want 55", ws1.MasterWidth)
	}
	wsCoding := cfg.Workspaces["coding"]
	if wsCoding.StackLayout != "tabbed" {
		t.Errorf("workspace coding stackLayout: got %q, want 'tabbed'", wsCoding.StackLayout)
	}
}

func TestLoadUserConfigFixture(t *testing.T) {
	cfg, err := Load("testdata/user_config.toml")
	if err != nil {
		t.Fatal(err)
	}

	// General settings
	if cfg.General.DefaultLayout != "none" {
		t.Errorf("defaultLayout: got %q, want 'none'", cfg.General.DefaultLayout)
	}
	if cfg.General.MasterWidth != 75 {
		t.Errorf("masterWidth: got %d, want 75", cfg.General.MasterWidth)
	}
	if !cfg.General.Debug {
		t.Error("debug: got false, want true")
	}
	if cfg.General.VisibleStackLimit != 3 {
		t.Errorf("visibleStackLimit: got %d, want 3", cfg.General.VisibleStackLimit)
	}

	// Workspace overrides
	if len(cfg.Workspaces) != 5 {
		t.Fatalf("expected 5 workspace overrides, got %d", len(cfg.Workspaces))
	}

	ws4 := cfg.Workspaces["4"]
	if ws4.DefaultLayout != "MasterStack" {
		t.Errorf("workspace 4 layout: got %q, want 'MasterStack'", ws4.DefaultLayout)
	}
	if ws4.StackSide != "left" {
		t.Errorf("workspace 4 stackSide: got %q, want 'left'", ws4.StackSide)
	}

	ws8 := cfg.Workspaces["8"]
	if ws8.DefaultLayout != "tabbed" {
		t.Errorf("workspace 8 layout: got %q, want 'tabbed'", ws8.DefaultLayout)
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	// Minimal config — defaults should fill in
	content := `[tilekeeper]
defaultLayout = "MasterStack"
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.General.MasterWidth != 50 {
		t.Errorf("default masterWidth: got %d, want 50", cfg.General.MasterWidth)
	}
	if cfg.General.StackLayout != "splitv" {
		t.Errorf("default stackLayout: got %q, want 'splitv'", cfg.General.StackLayout)
	}
	if cfg.General.StackSide != "right" {
		t.Errorf("default stackSide: got %q, want 'right'", cfg.General.StackSide)
	}
	if cfg.General.LogLevel != "" {
		// Load intentionally leaves LogLevel empty so cmd/tilekeeper
		// can apply its precedence (logLevel > TK_LOG_LEVEL > debug=true
		// > info default). Filling it here would silently shadow
		// `debug = true` in user configs.
		t.Errorf("LogLevel: got %q, want empty (precedence handled by main)", cfg.General.LogLevel)
	}
}

func TestLoadDoesNotShadowDebugTrue(t *testing.T) {
	// Regression: Load used to fill empty LogLevel with "info", which
	// silently shadowed `debug = true` because main.go's debug-fallback
	// only fires when LogLevel is empty.
	content := `[tilekeeper]
debug = true
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.General.LogLevel != "" {
		t.Errorf("LogLevel must remain empty so debug=true precedence runs, got %q", cfg.General.LogLevel)
	}
	if !cfg.General.Debug {
		t.Errorf("Debug: got %v, want true", cfg.General.Debug)
	}
}

func TestExampleConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(ExampleConfig()), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err != nil {
		t.Fatalf("ExampleConfig failed to parse: %v", err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load("/nonexistent/path/config.toml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoadEmptyPathNoConfigFile(t *testing.T) {
	// With no XDG_CONFIG_HOME or HOME pointing to valid config, Load("") returns defaults
	t.Setenv("XDG_CONFIG_HOME", "/nonexistent")
	t.Setenv("HOME", "/nonexistent")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load empty path should not error: %v", err)
	}
	if cfg.General.DefaultLayout != "none" {
		t.Errorf("expected default layout 'none', got %q", cfg.General.DefaultLayout)
	}
	if cfg.General.MasterWidth != 50 {
		t.Errorf("expected default masterWidth 50, got %d", cfg.General.MasterWidth)
	}
}

func TestLoadEmptyPathFindsConfigFile(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "tilekeeper")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `[tilekeeper]
defaultLayout = "MasterStack"
masterWidth = 80
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load with discovered config should not error: %v", err)
	}
	if cfg.General.DefaultLayout != "MasterStack" {
		t.Errorf("expected layout 'MasterStack', got %q", cfg.General.DefaultLayout)
	}
	if cfg.General.MasterWidth != 80 {
		t.Errorf("expected masterWidth 80, got %d", cfg.General.MasterWidth)
	}
}

func TestDefaultConfigPaths(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	t.Setenv("HOME", "/tmp/home")

	paths := DefaultConfigPaths()
	if len(paths) != 2 {
		t.Fatalf("expected 2 paths, got %d", len(paths))
	}
	if paths[0] != "/tmp/xdg/tilekeeper/config.toml" {
		t.Errorf("path[0] = %q, want /tmp/xdg/tilekeeper/config.toml", paths[0])
	}
	if paths[1] != "/tmp/home/.config/tilekeeper/config.toml" {
		t.Errorf("path[1] = %q, want /tmp/home/.config/tilekeeper/config.toml", paths[1])
	}
}

func TestDefaultConfigPathsNoEnv(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")

	paths := DefaultConfigPaths()
	if len(paths) != 0 {
		t.Errorf("expected 0 paths with no env, got %d", len(paths))
	}
}

func TestFindConfigFileNone(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/nonexistent")
	t.Setenv("HOME", "/nonexistent")

	if path := FindConfigFile(); path != "" {
		t.Errorf("expected empty path, got %q", path)
	}
}

func TestFindConfigFileExists(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "tilekeeper")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfgDir, "config.toml")
	if err := os.WriteFile(path, []byte("[tilekeeper]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", dir)

	found := FindConfigFile()
	if found != path {
		t.Errorf("FindConfigFile = %q, want %q", found, path)
	}
}

func TestLoadInvalidTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("not valid toml [[["), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for invalid TOML")
	}
}
