// Package oplog is the append-only jsonl journal behind `regrow undo`
// and `regrow history`. Every action is journaled before it runs
// (ARCHITECTURE.md invariant 6); trash receipts recorded on completion
// are the undo contract.
package oplog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/lbagic/regrow/internal/trash"
)

// Event is the lifecycle stage a journal line records.
const (
	EventStart = "start" // written BEFORE the action runs (invariant 6)
	EventDone  = "done"  // action succeeded; trash actions carry the receipt
	EventFail  = "fail"  // action failed; Error says why
	EventUndo  = "undo"  // a done trash action was restored (or restore failed)
)

// Entry is one journal line. A run emits start→done/fail pairs per
// action, keyed by (Run, Seq); a later undo emits undo lines that
// reference the same keys.
type Entry struct {
	Time    time.Time      `json:"time"`
	Run     string         `json:"run"`
	Seq     int            `json:"seq"`
	Event   string         `json:"event"`
	RuleID  string         `json:"rule_id,omitempty"`
	Kind    string         `json:"kind,omitempty"`
	Command []string       `json:"command,omitempty"`
	Path    string         `json:"path,omitempty"`
	Bytes   int64          `json:"bytes,omitempty"`
	Receipt *trash.Receipt `json:"receipt,omitempty"`
	Error   string         `json:"error,omitempty"`
}

// DefaultPath is ~/.local/state/regrow/oplog.jsonl, honouring
// XDG_STATE_HOME.
func DefaultPath() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "regrow", "oplog.jsonl"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "regrow", "oplog.jsonl"), nil
}

// Log is an append-only jsonl journal. Every Append is synced before
// it returns: the invariant is "journaled before it runs", and an
// entry lost in a page cache on crash breaks undo.
type Log struct {
	f *os.File
}

// Open opens (creating parents) the journal for appending.
func Open(path string) (*Log, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &Log{f: f}, nil
}

func (l *Log) Append(e Entry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := l.f.Write(append(line, '\n')); err != nil {
		return err
	}
	return l.f.Sync()
}

func (l *Log) Close() error { return l.f.Close() }

// Read parses the journal. A missing file is an empty history. A
// corrupt line fails loudly: the journal is the undo contract, and
// silently skipping lines could undo the wrong thing.
func Read(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("%s:%d: corrupt oplog line: %w", path, n, err)
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// Run is one execution's journal lines, grouped.
type Run struct {
	ID      string
	Start   time.Time
	Entries []Entry
}

// Runs groups entries by run id, oldest first. Undo entries group
// under the run they undo (they share its id).
func Runs(entries []Entry) []Run {
	byID := map[string]*Run{}
	var order []string
	for _, e := range entries {
		r, ok := byID[e.Run]
		if !ok {
			r = &Run{ID: e.Run, Start: e.Time}
			byID[e.Run] = r
			order = append(order, e.Run)
		}
		if e.Time.Before(r.Start) {
			r.Start = e.Time
		}
		r.Entries = append(r.Entries, e)
	}
	out := make([]Run, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

// Undoable returns the run's trash receipts that can still be
// restored — done actions minus those already undone — in reverse
// execution order (last moved, first restored).
func (r Run) Undoable() []Entry {
	undone := map[int]bool{}
	for _, e := range r.Entries {
		if e.Event == EventUndo && e.Error == "" {
			undone[e.Seq] = true
		}
	}
	var out []Entry
	for _, e := range r.Entries {
		if e.Event == EventDone && e.Receipt != nil && !undone[e.Seq] {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq > out[j].Seq })
	return out
}
