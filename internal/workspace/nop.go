package workspace

import "strings"

// NopCommand represents a parsed nop binding command.
//
// Sway fires binding events for `nop` commands, which we use for
// zero-overhead command dispatch. Format:
//
//	nop tilekeeper <command> [workspace <name>]
//
// where <command> is a layout action, e.g. "swap-master", "rotate cw",
// "focus left", "master grow", "stack side-toggle", "layout MasterStack".
type NopCommand struct {
	Command   string // e.g., "swap-master", "focus left", "layout MasterStack"
	Workspace string // optional target workspace
}

// ParseNopCommand extracts a layout command from a sway nop binding.
// Returns nil if the command is not a recognized `nop tilekeeper` command.
func ParseNopCommand(raw string) *NopCommand {
	rest, ok := strings.CutPrefix(strings.TrimSpace(raw), "nop tilekeeper ")
	if !ok {
		return nil
	}
	return parseNativeNop(rest)
}

func parseNativeNop(rest string) *NopCommand {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return nil
	}

	cmd := &NopCommand{}
	if idx := strings.Index(rest, " workspace "); idx >= 0 {
		cmd.Command = strings.TrimSpace(rest[:idx])
		cmd.Workspace = strings.TrimSpace(rest[idx+len(" workspace "):])
	} else {
		cmd.Command = rest
	}
	return cmd
}
