package replay

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/mschulkind-oss/tilekeeper/internal/capture"
	"github.com/mschulkind-oss/tilekeeper/internal/sway"
)

// JournalToCapture parses a text journal (slog key=value lines, as exported
// by `journalctl --user-unit tilekeeper.service`) into a best-effort
// capture.File. It is intentionally LOSSY: the journal records only
// con_id/name/app_id and the resolved workspace per op — never the full
// container snapshot (layout/floating/fullscreen/rect) or the tree shape. So
// the reconstructed events carry minimal containers, and structural
// invariants that depend on geometry (master-width-honored) will be
// unreliable. Use a daemon CAPTURE (TK_EVENT_CAPTURE) for faithful replay;
// this path exists so an OLD incident with only a journal can still be
// approximately re-driven and mined.
//
// It reads `op begin` lines, which carry: op_name, seq, workspace (or
// from/to for moves), con_id, name, app_id, and binding command. Each
// becomes one capture.EventRecord. Lines it can't parse are skipped.
func JournalToCapture(r io.Reader) (*capture.File, int) {
	f := &capture.File{}
	skipped := 0
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<16), 8<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, `msg="op begin"`) {
			continue
		}
		kv := parseKV(line)
		rec := opBeginToRecord(kv)
		if rec == nil {
			skipped++
			continue
		}
		f.Events = append(f.Events, rec)
	}
	f.SkippedLines = skipped
	return f, skipped
}

// JournalFileToCapture is JournalToCapture over a file path.
func JournalFileToCapture(path string) (*capture.File, int, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer fh.Close()
	f, skipped := JournalToCapture(fh)
	return f, skipped, nil
}

// opBeginToRecord turns a parsed `op begin` line into an EventRecord, or nil
// if the op_name is not a window/binding op replay can drive.
func opBeginToRecord(kv map[string]string) *capture.EventRecord {
	opName := kv["op_name"]
	if opName == "" {
		return nil
	}
	seq, _ := strconv.ParseInt(kv["seq"], 10, 64)
	switch opName {
	case "new", "close", "focus", "floating":
		conID, err := strconv.ParseInt(kv["con_id"], 10, 64)
		if err != nil {
			return nil
		}
		return &capture.EventRecord{
			Seq:       seq,
			Type:      "window",
			Change:    opName,
			WS:        kv["workspace"],
			Container: swayNode(conID, kv["name"], kv["app_id"], floatingFromOp(opName)),
		}
	case "move":
		conID, err := strconv.ParseInt(kv["con_id"], 10, 64)
		if err != nil {
			return nil
		}
		// Moves log from/to instead of workspace; "to" is the destination.
		dest := kv["to"]
		return &capture.EventRecord{
			Seq:       seq,
			Type:      "window",
			Change:    "move",
			WS:        dest,
			Container: swayNode(conID, kv["name"], kv["app_id"], ""),
		}
	case "binding":
		return &capture.EventRecord{
			Seq:     seq,
			Type:    "binding",
			Change:  "run",
			Binding: kv["command"],
		}
	default:
		return nil
	}
}

// floatingFromOp returns the floating string a floating-op implies. The
// journal doesn't record the post-toggle state, so we leave it empty
// (tiled) — the lossy-journal caveat. A real floating transition needs a
// capture.
func floatingFromOp(op string) string { return "" }

// swayNode builds a minimal leaf snapshot from the identity fields the
// journal carries. type=con; geometry/layout/fullscreen are unknown
// (zero) — the lossy-journal limitation.
func swayNode(id int64, name, appID, floating string) *sway.Node {
	return &sway.Node{
		ID:       id,
		Type:     "con",
		Name:     name,
		AppID:    appID,
		Floating: floating,
	}
}

// parseKV parses a slog text line ("k=v k=\"quoted v\" ...") into a map.
// It handles double-quoted values (which may contain spaces) and bare
// values. Keys before the first '=' on a token are taken literally; tokens
// without '=' (the leading timestamp/level, the msg prefix) are ignored.
func parseKV(line string) map[string]string {
	out := map[string]string{}
	i := 0
	n := len(line)
	for i < n {
		// Skip leading spaces.
		for i < n && line[i] == ' ' {
			i++
		}
		if i >= n {
			break
		}
		// Read key up to '=' or space.
		keyStart := i
		for i < n && line[i] != '=' && line[i] != ' ' {
			i++
		}
		if i >= n || line[i] == ' ' {
			// Token had no '=' — skip it.
			continue
		}
		key := line[keyStart:i]
		i++ // consume '='
		if i < n && line[i] == '"' {
			// Quoted value: read to the closing quote (no escape handling —
			// slog rarely embeds escaped quotes in these fields).
			i++
			valStart := i
			for i < n && line[i] != '"' {
				i++
			}
			out[key] = line[valStart:i]
			if i < n {
				i++ // consume closing quote
			}
		} else {
			valStart := i
			for i < n && line[i] != ' ' {
				i++
			}
			out[key] = line[valStart:i]
		}
	}
	return out
}
