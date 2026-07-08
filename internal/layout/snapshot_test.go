package layout

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSnapshotJSONRoundtrip(t *testing.T) {
	original := &LayoutSnapshot{
		SpecName:  "master-stack",
		Workspace: "1",
		Slots: map[string][]WindowInfo{
			"master": {
				{AppID: "Alacritty", Title: "vim main.go", Focused: true, InstanceID: "editor-1"},
			},
			"stack": {
				{AppID: "firefox", Title: "GitHub", Marks: []string{"browser"}},
				{AppID: "Alacritty", Title: "tests", InstanceID: "term-2"},
			},
		},
		CapturedAt: time.Date(2026, 4, 8, 2, 0, 0, 0, time.UTC),
	}

	data, err := json.MarshalIndent(original, "", "  ")
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var restored LayoutSnapshot
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if restored.SpecName != original.SpecName {
		t.Errorf("spec name: got %q, want %q", restored.SpecName, original.SpecName)
	}
	if restored.Workspace != original.Workspace {
		t.Errorf("workspace: got %q, want %q", restored.Workspace, original.Workspace)
	}
	if len(restored.Slots) != 2 {
		t.Fatalf("slot count: got %d, want 2", len(restored.Slots))
	}

	master := restored.Slots["master"]
	if len(master) != 1 {
		t.Fatalf("master window count: got %d, want 1", len(master))
	}
	if master[0].AppID != "Alacritty" {
		t.Errorf("master app_id: got %q, want %q", master[0].AppID, "Alacritty")
	}
	if master[0].InstanceID != "editor-1" {
		t.Errorf("master instance_id: got %q, want %q", master[0].InstanceID, "editor-1")
	}
	if !master[0].Focused {
		t.Error("master should be focused")
	}

	stack := restored.Slots["stack"]
	if len(stack) != 2 {
		t.Fatalf("stack window count: got %d, want 2", len(stack))
	}
	if stack[0].Marks[0] != "browser" {
		t.Errorf("stack[0] mark: got %q, want %q", stack[0].Marks[0], "browser")
	}
}

func TestCaptureSnapshot(t *testing.T) {
	spec := NewMasterStack(50)
	slotWindows := map[string][]WindowInfo{
		"master": {{AppID: "Alacritty", Focused: true}},
		"stack":  {{AppID: "firefox"}, {AppID: "slack"}},
	}

	snap := CaptureSnapshot(spec, "2", slotWindows)

	if snap.SpecName != "master-stack" {
		t.Errorf("spec name: got %q, want %q", snap.SpecName, "master-stack")
	}
	if snap.Workspace != "2" {
		t.Errorf("workspace: got %q, want %q", snap.Workspace, "2")
	}
	if len(snap.Slots["master"]) != 1 {
		t.Errorf("master slots: got %d, want 1", len(snap.Slots["master"]))
	}
	if len(snap.Slots["stack"]) != 2 {
		t.Errorf("stack slots: got %d, want 2", len(snap.Slots["stack"]))
	}
	if snap.CapturedAt.IsZero() {
		t.Error("captured_at should not be zero")
	}
}

func TestWindowMatcherSerialization(t *testing.T) {
	matcher := WindowMatcher{
		InstanceID: "editor-1",
		AppID:      "Alacritty",
		TitleRegex: ".*main\\.go",
		Marks:      []string{"editor"},
		Ordinal:    1,
	}

	data, err := json.Marshal(matcher)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var restored WindowMatcher
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if restored.InstanceID != matcher.InstanceID {
		t.Errorf("instance_id: got %q, want %q", restored.InstanceID, matcher.InstanceID)
	}
	if restored.AppID != matcher.AppID {
		t.Errorf("app_id: got %q, want %q", restored.AppID, matcher.AppID)
	}
	if restored.TitleRegex != matcher.TitleRegex {
		t.Errorf("title_regex: got %q, want %q", restored.TitleRegex, matcher.TitleRegex)
	}
	if restored.Ordinal != matcher.Ordinal {
		t.Errorf("ordinal: got %d, want %d", restored.Ordinal, matcher.Ordinal)
	}
}

func TestSlotAssignmentSerialization(t *testing.T) {
	assignments := []SlotAssignment{
		{
			SlotID:  "master",
			Matcher: WindowMatcher{AppID: "Alacritty", InstanceID: "editor-1"},
		},
		{
			SlotID:  "stack",
			Matcher: WindowMatcher{AppID: "firefox"},
		},
	}

	data, err := json.MarshalIndent(assignments, "", "  ")
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var restored []SlotAssignment
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if len(restored) != 2 {
		t.Fatalf("assignment count: got %d, want 2", len(restored))
	}
	if restored[0].SlotID != "master" {
		t.Errorf("slot 0: got %q, want %q", restored[0].SlotID, "master")
	}
	if restored[1].Matcher.AppID != "firefox" {
		t.Errorf("slot 1 app_id: got %q, want %q", restored[1].Matcher.AppID, "firefox")
	}
}
