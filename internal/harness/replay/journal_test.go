package replay

import (
	"strings"
	"testing"
)

// TestJournalToCapture parses a representative slog text journal and asserts
// the op begin lines become EventRecords with the right identity, change,
// and resolved workspace (workspace= for ops, to= for moves).
func TestJournalToCapture(t *testing.T) {
	journal := `2026-06-13T10:00:00.000-04:00 level=INFO msg="op begin" op=1 op_name=new seq=1 workspace=7 layout=MasterStack con_id=500 name="alacritty" app_id=Alacritty tracked_before="[]"
2026-06-13T10:00:00.100-04:00 level=INFO msg="op end" op=1 op_name=new seq=1 workspace=7 con_id=500 tracked_after="[500]"
2026-06-13T10:00:02.000-04:00 level=INFO msg="op begin" op=3 op_name=focus seq=3 workspace=7 con_id=500 name="alacritty" app_id=Alacritty tracked_before="[500 501]"
2026-06-13T10:00:03.000-04:00 level=DEBUG msg="sway cmd" component=layout.masterstack workspace=7 cmd="[con_id=500] focus"
2026-06-13T10:00:04.000-04:00 level=INFO msg="op begin" op=4 op_name=move seq=4 con_id=501 name="firefox" from=7 to=9 tracked=true
2026-06-13T10:00:05.000-04:00 level=INFO msg="op begin" op=5 op_name=binding command="nop tilekeeper master add" workspace=7
`
	f, skipped := JournalToCapture(strings.NewReader(journal))
	if skipped != 0 {
		t.Errorf("skipped: got %d want 0", skipped)
	}
	// op begin lines for new, focus, move, binding = 4 events (op end and
	// sway cmd lines are ignored).
	if len(f.Events) != 4 {
		t.Fatalf("events: got %d want 4", len(f.Events))
	}
	if f.Events[0].Change != "new" || f.Events[0].Container.ID != 500 || f.Events[0].WS != "7" {
		t.Errorf("new event wrong: %+v / container=%+v", f.Events[0], f.Events[0].Container)
	}
	if f.Events[0].Container.Name != "alacritty" || f.Events[0].Container.AppID != "Alacritty" {
		t.Errorf("new event identity not parsed: name=%q app=%q", f.Events[0].Container.Name, f.Events[0].Container.AppID)
	}
	mv := f.Events[2]
	if mv.Change != "move" || mv.Container.ID != 501 || mv.WS != "9" {
		t.Errorf("move event should resolve to dest ws 9: %+v", mv)
	}
	bind := f.Events[3]
	if bind.Type != "binding" || bind.Binding != "nop tilekeeper master add" {
		t.Errorf("binding not parsed: %+v", bind)
	}
}

// TestParseKVQuoted confirms quoted values with spaces and bare values both
// parse, and that tokens without '=' (timestamp, level prefix) are ignored.
func TestParseKVQuoted(t *testing.T) {
	kv := parseKV(`2026-06-13T10:00:05 level=INFO msg="op begin" command="nop tilekeeper master add" con_id=42`)
	if kv["command"] != "nop tilekeeper master add" {
		t.Errorf("quoted-with-spaces value: got %q", kv["command"])
	}
	if kv["con_id"] != "42" {
		t.Errorf("bare value: got %q", kv["con_id"])
	}
	if kv["msg"] != "op begin" {
		t.Errorf("msg: got %q", kv["msg"])
	}
}

// TestMineCaptureJournal mines weights from a journal-derived capture and
// checks the move is classified as a container move (its source workspace
// held >1 window) and the binding command is tallied.
func TestMineCaptureJournal(t *testing.T) {
	journal := `level=INFO msg="op begin" op=1 op_name=new seq=1 workspace=7 con_id=500 name="a" app_id=A tracked_before="[]"
level=INFO msg="op begin" op=2 op_name=new seq=2 workspace=7 con_id=501 name="b" app_id=B tracked_before="[500]"
level=INFO msg="op begin" op=3 op_name=move seq=3 con_id=500 name="a" from=7 to=9 tracked=true
level=INFO msg="op begin" op=4 op_name=binding command="nop tilekeeper stack toggle" workspace=7
`
	f, _ := JournalToCapture(strings.NewReader(journal))
	w := MineCapture(f)
	if w.Counts["new"] != 2 {
		t.Errorf("new count: got %d want 2", w.Counts["new"])
	}
	if w.ContainerMoves != 1 {
		t.Errorf("container moves: got %d want 1 (source ws had 2 windows)", w.ContainerMoves)
	}
	if w.BindingCommands["nop tilekeeper stack toggle"] != 1 {
		t.Errorf("binding tally: %+v", w.BindingCommands)
	}
}
