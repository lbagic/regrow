package engine

// Totals splits bytes into buckets by when, if ever, they become free
// space. Containment is computed from the forest, never stored.
type Totals struct {
	FreesNow     int64 // steward commands: freed as they run
	AfterTrash   int64 // moved to the Trash: freed once it is emptied
	ShownOnly    int64 // surface-only: regrow never deletes it
	MacOSManaged int64 // phantom space macOS reclaims on its own
	Partial      int   // items whose bytes are lower bounds
}
