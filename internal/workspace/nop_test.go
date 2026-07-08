package workspace

import "testing"

func TestParseNopCommand(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantNil bool
		wantCmd string
		wantWS  string
	}{
		// Native format: nop tilekeeper <command>
		{
			name:    "native/swap-master",
			input:   "nop tilekeeper swap-master",
			wantCmd: "swap-master",
		},
		{
			name:    "native/rotate cw",
			input:   "nop tilekeeper rotate cw",
			wantCmd: "rotate cw",
		},
		{
			name:    "native/with workspace",
			input:   "nop tilekeeper master grow workspace 4",
			wantCmd: "master grow",
			wantWS:  "4",
		},
		{
			name:    "native/rotate with workspace",
			input:   "nop tilekeeper rotate ccw workspace 7",
			wantCmd: "rotate ccw",
			wantWS:  "7",
		},
		{
			name:    "native/focus master",
			input:   "nop tilekeeper focus master",
			wantCmd: "focus master",
		},
		{
			name:    "native/with extra spaces",
			input:   "  nop tilekeeper   swap-master  ",
			wantCmd: "swap-master",
		},
		{
			name:    "native/layout switch",
			input:   "nop tilekeeper layout MasterStack",
			wantCmd: "layout MasterStack",
		},
		{
			name:    "native/flat command with workspace",
			input:   "nop tilekeeper stack side-toggle workspace 7",
			wantCmd: "stack side-toggle",
			wantWS:  "7",
		},

		// Rejected inputs
		{
			name:    "not nop",
			input:   "exec --no-startup-id firefox",
			wantNil: true,
		},
		{
			name:    "nop but not layout",
			input:   "nop something else",
			wantNil: true,
		},
		{
			name:    "empty after layout prefix",
			input:   "nop tilekeeper ",
			wantNil: true,
		},
		{
			name:    "just nop",
			input:   "nop",
			wantNil: true,
		},
		{
			name:    "empty string",
			input:   "",
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseNopCommand(tt.input)
			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil result")
			}
			if got.Command != tt.wantCmd {
				t.Errorf("Command = %q, want %q", got.Command, tt.wantCmd)
			}
			if got.Workspace != tt.wantWS {
				t.Errorf("Workspace = %q, want %q", got.Workspace, tt.wantWS)
			}
		})
	}
}
