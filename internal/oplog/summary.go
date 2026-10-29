package oplog

import (
	"time"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/trash"
)

// Summary is one run as `regrow history` counts it: an action once its
// done line is in the journal, a prune at what it deleted, any other
// action at its planned size; a failed or cut-short action counts
// nothing. Its bytes are split by where the space went, as the run's
// summary splits them.
type Summary struct {
	ID     string    `json:"id"`
	Start  time.Time `json:"start"`
	Done   int       `json:"done"`
	Failed int       `json:"failed"`
	Undone int       `json:"undone"`
	// Restorable counts the moves `regrow undo` can still put back.
	Restorable int `json:"restorable"`
	// Prune: every action of the run was a prune.
	Prune bool `json:"prune"`
	// FreesNow is what commands and prunes freed as they ran.
	FreesNow int64 `json:"frees_now"`
	// AfterTrash went to the Trash, Staged to regrow staging (Finder
	// being unavailable), Exported is docker volumes removed after
	// their export.
	AfterTrash int64 `json:"after_trash"`
	Staged     int64 `json:"staged"`
	Exported   int64 `json:"exported"`
	// Rules is the bytes by rule id.
	Rules map[string]int64 `json:"rules"`
}

// Bytes is the run's size as `regrow history` prints it.
func (s Summary) Bytes() int64 { return s.FreesNow + s.AfterTrash + s.Staged + s.Exported }

// Summary counts the run. Restorable is right only for a run from Runs
// over the whole journal, as OnDisk is.
func (r Run) Summary() Summary {
	s := Summary{ID: r.ID, Start: r.Start, Rules: map[string]int64{}}
	planned := map[int]Entry{}
	prunes := 0
	for _, e := range r.Entries {
		switch e.Event {
		case EventStart:
			planned[e.Seq] = e
			if e.Kind == string(engine.ActionPrune) {
				prunes++
			}
			continue
		case EventFail:
			s.Failed++
			continue
		case EventUndo:
			if e.Error == "" {
				s.Undone++
			}
			continue
		case EventDone:
			s.Done++
		default:
			continue
		}
		start := planned[e.Seq]
		var bytes int64
		switch {
		case e.Pruned != nil:
			bytes = e.Pruned.Bytes
		case start.Kind != string(engine.ActionPrune):
			bytes = start.Bytes
		}
		var method trash.Method
		if e.Receipt != nil {
			method = e.Receipt.Method
		}
		switch {
		case start.Kind == string(engine.ActionTrash) && method == trash.MethodFinder:
			s.AfterTrash += bytes
		case start.Kind == string(engine.ActionTrash) && method == trash.MethodStaging:
			s.Staged += bytes
		case method == trash.MethodExport:
			s.Exported += bytes
		default:
			s.FreesNow += bytes
		}
		if bytes > 0 {
			s.Rules[start.RuleID] += bytes
		}
	}
	s.Prune = prunes > 0 && prunes == len(planned)
	present, _ := r.OnDisk()
	s.Restorable = len(present)
	return s
}
