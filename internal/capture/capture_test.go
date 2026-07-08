package capture

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mschulkind-oss/tilekeeper/internal/sway"
)

// TestRoundTrip writes a meta record + a few events through the Writer and
// reads them back, asserting the JSONL survives the round trip with the
// container snapshot fields replay depends on intact.
func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cap.jsonl")

	w, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.WriteMeta(MetaRecord{
		Workspaces:    []string{"7", "9"},
		DefaultLayout: "MasterStack",
		MasterWidth:   75,
	}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	evs := []struct {
		ev sway.Event
		ws string
	}{
		{sway.Event{Type: "workspace", Change: "init", Seq: 1, Workspace: &sway.Node{Type: "workspace", Name: "9"}}, "9"},
		{sway.Event{Type: "window", Change: "new", Seq: 2, Container: &sway.Node{ID: 100, Type: "con", Name: "a", AppID: "term", Rect: sway.Rect{Width: 1280, Height: 720}}}, "9"},
		{sway.Event{Type: "window", Change: "move", Seq: 3, Container: &sway.Node{ID: 100, Type: "con", Name: "a"}}, "7"},
		{sway.Event{Type: "binding", Change: "run", Seq: 4, Binding: &sway.Binding{Command: "nop tilekeeper focus down"}}, ""},
	}
	for _, e := range evs {
		if err := w.WriteEvent(e.ev, e.ws); err != nil {
			t.Fatalf("WriteEvent: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f, err := ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if f.Meta == nil {
		t.Fatal("meta record missing")
	}
	if f.Meta.FormatVersion != FormatVersion {
		t.Errorf("format version: got %d want %d", f.Meta.FormatVersion, FormatVersion)
	}
	if len(f.Events) != 4 {
		t.Fatalf("event count: got %d want 4", len(f.Events))
	}
	if f.Events[1].Container == nil || f.Events[1].Container.ID != 100 || f.Events[1].Container.AppID != "term" {
		t.Errorf("new event container not preserved: %+v", f.Events[1].Container)
	}
	if f.Events[2].WS != "7" {
		t.Errorf("move destination WS not preserved: got %q want 7", f.Events[2].WS)
	}
	// Reconstruct the binding event.
	bindEv := f.Events[3].ToEvent()
	if bindEv.Binding == nil || bindEv.Binding.Command != "nop tilekeeper focus down" {
		t.Errorf("binding not reconstructed: %+v", bindEv.Binding)
	}
}

// TestNilWriterNoOp confirms a nil Writer's methods are safe no-ops, so the
// daemon hot path needs no nil checks when capture is disabled.
func TestNilWriterNoOp(t *testing.T) {
	var w *Writer
	if err := w.WriteMeta(MetaRecord{}); err != nil {
		t.Errorf("nil WriteMeta: %v", err)
	}
	if err := w.WriteEvent(sway.Event{Type: "window", Change: "new"}, "7"); err != nil {
		t.Errorf("nil WriteEvent: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("nil Close: %v", err)
	}
}

// TestReadToler(sic) confirms malformed and unknown-kind lines are skipped,
// not fatal — a truncated capture still yields its parsed prefix.
func TestReadToleratesGarbage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "garbage.jsonl")
	content := `{"kind":"meta","meta":{"format_version":1}}
not json at all
{"kind":"event","event":{"seq":1,"type":"window","change":"new","container":{"id":5,"type":"con"}}}
{"kind":"unknownkind"}
{"kind":"event"}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(f.Events) != 1 {
		t.Errorf("events: got %d want 1", len(f.Events))
	}
	// garbage line + unknownkind + empty-event line = 3 skipped.
	if f.SkippedLines != 3 {
		t.Errorf("skipped: got %d want 3", f.SkippedLines)
	}
}
