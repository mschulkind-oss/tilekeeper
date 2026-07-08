// Package config handles TOML configuration parsing for tilekeeper.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config is the top-level configuration. Settings live under a
// [tilekeeper] section, with optional [workspace.<name>] overrides.
type Config struct {
	General    GeneralConfig              `toml:"tilekeeper"`
	Workspaces map[string]WorkspaceConfig `toml:"workspace"`
}

// GeneralConfig holds daemon-wide settings.
type GeneralConfig struct {
	DefaultLayout     string `toml:"defaultLayout"`
	MasterWidth       int    `toml:"masterWidth"`
	StackLayout       string `toml:"stackLayout"`
	StackSide         string `toml:"stackSide"`
	VisibleStackLimit int    `toml:"visibleStackLimit"`
	Debug             bool   `toml:"debug"`
	IPCSocket         string `toml:"ipcSocket"`
	LogLevel          string `toml:"logLevel"`
}

// WorkspaceConfig holds per-workspace overrides.
type WorkspaceConfig struct {
	DefaultLayout     string `toml:"defaultLayout"`
	MasterWidth       int    `toml:"masterWidth"`
	StackLayout       string `toml:"stackLayout"`
	StackSide         string `toml:"stackSide"`
	VisibleStackLimit int    `toml:"visibleStackLimit"`
	// ProjectTabs-specific config
	SplitRatio   int    `toml:"splitRatio"`
	TerminalSide string `toml:"terminalSide"`
	DefaultMode  string `toml:"defaultMode"`
}

// DefaultConfig returns a config with sensible defaults.
//
// LogLevel is left empty so cmd/tilekeeper can apply its precedence
// (logLevel > TK_LOG_LEVEL > debug=true > info default). Filling it here
// would silently shadow `debug = true` in user configs.
func DefaultConfig() Config {
	return Config{
		General: GeneralConfig{
			DefaultLayout: "none",
			MasterWidth:   50,
			StackLayout:   "splitv",
			StackSide:     "right",
		},
		Workspaces: make(map[string]WorkspaceConfig),
	}
}

// DefaultConfigPaths returns the standard config file locations in priority order.
func DefaultConfigPaths() []string {
	var paths []string
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		paths = append(paths, filepath.Join(xdg, "tilekeeper", "config.toml"))
	}
	if home := os.Getenv("HOME"); home != "" {
		paths = append(paths, filepath.Join(home, ".config", "tilekeeper", "config.toml"))
	}
	return paths
}

// FindConfigFile returns the first config file that exists, or empty string.
func FindConfigFile() string {
	for _, p := range DefaultConfigPaths() {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// Load reads a config from the given path, applying defaults for missing values.
// If path is empty, it searches default config paths. If no file is found, returns defaults.
func Load(path string) (Config, error) {
	cfg := DefaultConfig()

	if path == "" {
		path = FindConfigFile()
	}
	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("reading config: %w", err)
	}

	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return cfg, fmt.Errorf("parsing config: %w", err)
	}

	// Apply defaults for zero values
	if cfg.General.MasterWidth == 0 {
		cfg.General.MasterWidth = 50
	}
	if cfg.General.StackLayout == "" {
		cfg.General.StackLayout = "splitv"
	}
	if cfg.General.StackSide == "" {
		cfg.General.StackSide = "right"
	}
	// Don't fill LogLevel here. main.go's "logLevel > TK_LOG_LEVEL > Debug
	// (legacy bool) > info default" precedence runs only when LogLevel is
	// empty; setting "info" here silently shadowed `debug = true`.

	return cfg, nil
}

// ExampleConfig returns a TOML string with an example configuration.
func ExampleConfig() string {
	return `# tilekeeper configuration

[tilekeeper]
defaultLayout = "none"
masterWidth = 50
stackLayout = "splitv"
stackSide = "right"
visibleStackLimit = 3

# Logging — one of: "trace", "debug", "info", "warn", "error".
# "trace" is extremely verbose (every sway IPC message). Use for debugging
# only. "debug" logs every binding/event dispatch with full context.
# Override at runtime with env: TK_LOG_LEVEL=debug
logLevel = "info"

# Per-workspace overrides
[workspace.1]
defaultLayout = "MasterStack"

[workspace.2]
defaultLayout = "MasterStack"
stackSide = "left"
masterWidth = 65
`
}
