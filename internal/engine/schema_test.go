package engine

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPathEntryUnmarshalScalarAndMap(t *testing.T) {
	var got struct {
		Paths []PathEntry `yaml:"paths"`
	}
	src := `
paths:
  - ~/Library/Caches/example
  - path: /Library/Application Support/com.apple.idleassetsd/Customer
    os_min: "13"
    os_max: "15"
`
	if err := yaml.Unmarshal([]byte(src), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Paths) != 2 {
		t.Fatalf("want 2 entries, got %d", len(got.Paths))
	}
	if got.Paths[0].Path != "~/Library/Caches/example" || got.Paths[0].OSMin != "" {
		t.Errorf("scalar entry parsed wrong: %+v", got.Paths[0])
	}
	if got.Paths[1].OSMin != "13" || got.Paths[1].OSMax != "15" {
		t.Errorf("map entry parsed wrong: %+v", got.Paths[1])
	}
}

func TestPathEntryVersionMatch(t *testing.T) {
	tests := []struct {
		entry   PathEntry
		version string
		want    bool
	}{
		{PathEntry{Path: "p"}, "15.5", true},
		{PathEntry{Path: "p", OSMin: "26"}, "26.0", true},
		{PathEntry{Path: "p", OSMin: "26"}, "15.5", false},
		{PathEntry{Path: "p", OSMax: "15"}, "15.2", false}, // 15.2 > 15
		{PathEntry{Path: "p", OSMax: "15.4"}, "15.2", true},
		{PathEntry{Path: "p", OSMin: "14", OSMax: "15.9"}, "15.5", true},
		// Unknown host version: constrained entries do not apply.
		{PathEntry{Path: "p", OSMin: "26"}, "", false},
		{PathEntry{Path: "p"}, "", true},
	}
	for _, tt := range tests {
		if got := tt.entry.matches(tt.version); got != tt.want {
			t.Errorf("entry %+v vs version %q: got %v want %v", tt.entry, tt.version, got, tt.want)
		}
	}
}

func validRule() Rule {
	return Rule{
		ID:       "example-rule",
		Title:    "Example",
		Category: "dev-caches",
		Risk:     RiskSafe,
		Paths:    map[string][]PathEntry{"darwin": {{Path: "~/Library/Caches/example"}}},
	}
}

func TestValidate(t *testing.T) {
	if err := validRule().Validate(); err != nil {
		t.Fatalf("valid rule rejected: %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(*Rule)
		wantErr string
	}{
		{"bad id", func(r *Rule) { r.ID = "Bad_ID" }, "kebab-case"},
		{"missing title", func(r *Rule) { r.Title = "" }, "title"},
		{"missing category", func(r *Rule) { r.Category = "" }, "category"},
		{"bad risk", func(r *Rule) { r.Risk = "expert" }, "risk"},
		{"no source", func(r *Rule) { r.Paths = nil }, "at least one of"},
		{"unknown os", func(r *Rule) { r.Paths = map[string][]PathEntry{"windows": {{Path: "C:"}}} }, "unknown paths os"},
		{"empty path", func(r *Rule) { r.Paths = map[string][]PathEntry{"darwin": {{Path: ""}}} }, "empty path"},
		{"discover without roots", func(r *Rule) { r.Discover = &Discover{Name: "target"} }, "roots"},
		{"discover without matcher", func(r *Rule) { r.Discover = &Discover{Roots: []string{"~"}} }, "name or markers"},
		{
			"surface-only with native command",
			func(r *Rule) { r.Risk = RiskSurfaceOnly; r.NativeCommand = "rm -rf" },
			"surface-only",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validRule()
			tt.mutate(&r)
			err := r.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}
