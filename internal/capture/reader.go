package capture

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// File is a parsed capture file: the optional meta record plus the ordered
// list of event records.
type File struct {
	Meta   *MetaRecord
	Events []*EventRecord
	// SkippedLines counts JSONL lines that failed to parse or carried an
	// unknown kind. Replay surfaces this so a partially-corrupt capture is
	// processed as far as possible rather than rejected wholesale.
	SkippedLines int
}

// ReadFile parses a JSONL capture from path. It tolerates malformed or
// unknown-kind lines (counted in SkippedLines) so a truncated capture
// still yields whatever events parsed cleanly before the corruption.
func ReadFile(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("capture: read %s: %w", path, err)
	}
	defer f.Close()
	return Read(f)
}

// Read parses a JSONL capture from r. See ReadFile for tolerance behavior.
func Read(r io.Reader) (*File, error) {
	out := &File{}
	sc := bufio.NewScanner(r)
	// Container snapshots with a startup get_tree can be large; raise the
	// per-line cap well above bufio's 64KiB default.
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			out.SkippedLines++
			continue
		}
		switch rec.Kind {
		case KindMeta:
			if rec.Meta != nil {
				out.Meta = rec.Meta
			}
		case KindEvent:
			if rec.Event != nil {
				out.Events = append(out.Events, rec.Event)
			} else {
				out.SkippedLines++
			}
		default:
			out.SkippedLines++
		}
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("capture: scan: %w", err)
	}
	return out, nil
}
