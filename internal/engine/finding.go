package engine

import "time"

// Item is one concrete thing a rule found: a directory on disk, or a
// tool-reported entry (a docker image, an ollama model).
type Item struct {
	// Path is the filesystem location; empty for tool items that only
	// a steward command can address.
	Path string `json:"path,omitempty"`
	// Label is the human name shown in the UI (model name, sim
	// runtime, ...). Defaults to Path when empty.
	Label string `json:"label,omitempty"`
	// Arg substitutes {arg} in the rule's native command (a model id
	// for `ollama rm {arg}`, a runtime id for simctl).
	Arg string `json:"arg,omitempty"`
	// Bytes is measured disk usage (physical blocks, du-style), or a
	// tool-reported size.
	Bytes int64 `json:"bytes"`
	// LastUsed is a best-effort recency signal; zero when unknown.
	LastUsed time.Time `json:"last_used,omitzero"`
}

// Finding is one rule with everything the scan measured for it.
type Finding struct {
	Rule  Rule   `json:"rule"`
	Items []Item `json:"items,omitempty"`
	// Err records a scan failure (tool missing, permission denied at
	// the root) so the UI can show why a rule reported nothing.
	Err string `json:"error,omitempty"`
}

// TotalBytes sums the finding's items.
func (f Finding) TotalBytes() int64 {
	var n int64
	for _, it := range f.Items {
		n += it.Bytes
	}
	return n
}
