// Package capture implements an optional, zero-overhead-when-disabled
// JSONL event-capture sink for the tilekeeper daemon.
//
// The production daemon's text journal (slog key=value lines) records only
// con_id/name/app_id for each event — it omits the full container snapshot
// (layout/floating/fullscreen/rect/focused) and never logs the get_tree
// shape. That makes faithful replay from the journal alone lossy: a real
// incident can be *located* in the journal but not deterministically
// *reconstructed* from it.
//
// This package closes that gap. When TK_EVENT_CAPTURE=<path> is set, the
// daemon opens a Writer and appends one JSON line per raw sway.Event the
// hub processes — capturing the container snapshot fields replay needs to
// rebuild the sim tree exactly. The capture file is the reliable replay
// source; the text journal is a best-effort fallback.
//
// Format: newline-delimited JSON (JSONL). The first line MAY be a "meta"
// record carrying the capture format version and an optional get_tree
// snapshot taken at daemon startup, so replay can seed initial state.
// Every subsequent line is an "event" record. Unknown record kinds are
// skipped by the reader, so the format can grow forward-compatibly.
package capture

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/mschulkind-oss/tilekeeper/internal/sway"
)

// FormatVersion is bumped when the on-disk JSONL schema changes in a way
// the reader must branch on. Readers tolerate older minor formats; a
// mismatch is logged, not fatal.
const FormatVersion = 1

// Kind discriminates record types on a JSONL line.
type Kind string

const (
	// KindMeta is the optional leading record: format version, daemon
	// build, and an optional startup tree snapshot.
	KindMeta Kind = "meta"
	// KindEvent is one captured sway.Event.
	KindEvent Kind = "event"
)

// Record is one line of the capture file. Exactly one of Meta/Event is
// populated depending on Kind. JSON omits the empty one.
type Record struct {
	Kind  Kind         `json:"kind"`
	Meta  *MetaRecord  `json:"meta,omitempty"`
	Event *EventRecord `json:"event,omitempty"`
}

// MetaRecord carries capture-wide context. Tree is optional — present only
// if the daemon captured a get_tree snapshot at startup.
type MetaRecord struct {
	FormatVersion int        `json:"format_version"`
	Version       string     `json:"version,omitempty"`
	Commit        string     `json:"commit,omitempty"`
	StartedAt     string     `json:"started_at,omitempty"`
	Workspaces    []string   `json:"workspaces,omitempty"`
	DefaultLayout string     `json:"default_layout,omitempty"`
	MasterWidth   int        `json:"master_width,omitempty"`
	Tree          *sway.Node `json:"tree,omitempty"`
}

// EventRecord is a captured sway.Event plus the daemon-assigned seq and a
// wall-clock timestamp. The Container/Workspace snapshots are exactly what
// the hub saw (sway.Node.Snapshot): id/name/app_id/type/layout/floating/
// fullscreen/rect/focused/percent/marks — everything replay needs to
// rebuild the leaf, with no children and no parent (matching IPC payloads).
type EventRecord struct {
	Seq       int64      `json:"seq"`
	TS        string     `json:"ts,omitempty"`
	Type      string     `json:"type"`
	Change    string     `json:"change"`
	Container *sway.Node `json:"container,omitempty"`
	Workspace *sway.Node `json:"workspace,omitempty"`
	Binding   string     `json:"binding,omitempty"`

	// WS is the name of the workspace the event container lives on at
	// emission time, resolved by the daemon from the LIVE tree (the event
	// snapshot itself carries no parent, so it cannot answer this). It is
	// the single field that makes window::move faithfully replayable: the
	// daemon reads a move's DESTINATION workspace from the live tree, so
	// replay must place the container there before dispatch. Empty when the
	// container could not be resolved (e.g. a close whose node is already
	// gone) — replay falls back to its own tracking.
	WS string `json:"ws,omitempty"`

	// Drop marks an event whose tree side-effect happened in sway but which
	// the daemon NEVER dispatched to the hub — the bounded event channel
	// overflowed (the documented "no drop-recovery by design" failure mode).
	// The daemon's live capture never sets this (capture happens on the
	// dispatched path), but synthesized incident captures use it to model a
	// drop: replay applies the tree mutation and SKIPS the hub dispatch, so
	// manager tracking diverges from the leaves — exactly the corruption a
	// drop causes — and tracked-matches-leaves fires. It is the replay
	// analogue of fuzz.Config.DropRate.
	Drop bool `json:"drop,omitempty"`
}

// ToEvent reconstructs a sway.Event from a captured EventRecord. The
// returned event mirrors what the daemon's subscribe callback produced:
// snapshot container (Parent=nil, no children), the seq, and binding.
func (r *EventRecord) ToEvent() sway.Event {
	ev := sway.Event{
		Type:      r.Type,
		Change:    r.Change,
		Seq:       r.Seq,
		Container: r.Container,
		Workspace: r.Workspace,
	}
	if r.Binding != "" {
		ev.Binding = &sway.Binding{Command: r.Binding}
	}
	return ev
}

// Writer appends capture records to a file as JSONL. It is safe for
// concurrent use, though the daemon's single-threaded event loop calls it
// from one goroutine. Construct with Open; nil Writers are no-ops so call
// sites need no nil checks.
type Writer struct {
	mu   sync.Mutex
	f    *os.File
	bw   *bufio.Writer
	enc  *json.Encoder
	errN int // count of write errors (for the close summary)
}

// Open creates/truncates the file at path and returns a Writer. The caller
// owns Close. An error here is non-fatal to the daemon — capture is a
// diagnostic, so the caller should log and continue with a nil Writer
// (which no-ops).
func Open(path string) (*Writer, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("capture: open %s: %w", path, err)
	}
	bw := bufio.NewWriter(f)
	w := &Writer{f: f, bw: bw, enc: json.NewEncoder(bw)}
	return w, nil
}

// WriteMeta writes the leading meta record. Call once, before any events.
// A nil Writer is a no-op.
func (w *Writer) WriteMeta(m MetaRecord) error {
	if w == nil {
		return nil
	}
	if m.FormatVersion == 0 {
		m.FormatVersion = FormatVersion
	}
	if m.StartedAt == "" {
		m.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	return w.write(Record{Kind: KindMeta, Meta: &m})
}

// WriteEvent appends one captured event. wsName is the workspace the event
// container resolves to in the live tree (see EventRecord.WS) — pass "" if
// unknown. A nil Writer is a no-op, so the daemon hot path can call this
// unconditionally when capture is enabled and skip the call entirely when
// the env var is unset (the Writer is nil).
//
// The Container/Workspace are stored as-is; the daemon passes Snapshot()
// copies (no children, Parent=nil), matching real IPC payloads, so the
// JSON is small and acyclic.
func (w *Writer) WriteEvent(ev sway.Event, wsName string) error {
	if w == nil {
		return nil
	}
	rec := Record{Kind: KindEvent, Event: &EventRecord{
		Seq:       ev.Seq,
		TS:        time.Now().UTC().Format(time.RFC3339Nano),
		Type:      ev.Type,
		Change:    ev.Change,
		Container: ev.Container,
		Workspace: ev.Workspace,
		WS:        wsName,
	}}
	if ev.Binding != nil {
		rec.Event.Binding = ev.Binding.Command
	}
	return w.write(rec)
}

func (w *Writer) write(rec Record) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.enc.Encode(rec); err != nil {
		w.errN++
		return err
	}
	return nil
}

// Close flushes and closes the underlying file. A nil Writer is a no-op.
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.bw != nil {
		if err := w.bw.Flush(); err != nil {
			_ = w.f.Close()
			return err
		}
	}
	return w.f.Close()
}
