package engine

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Risk classes are architectural, not cosmetic (ARCHITECTURE.md
// invariant 5): safe rules may be auto-selected, caution rules are
// review-and-select, surface-only rules are never deletable through us.
type Risk string

const (
	RiskSafe        Risk = "safe"
	RiskCaution     Risk = "caution"
	RiskSurfaceOnly Risk = "surface-only"
)

func (r Risk) valid() bool {
	switch r {
	case RiskSafe, RiskCaution, RiskSurfaceOnly:
		return true
	}
	return false
}

// Actionable reports whether the class may ever produce plan actions.
// Surface-only is report-only by architecture (invariant 5); the
// planner, the schema, and the UI all ask this one method instead of
// re-deriving it.
func (r Risk) Actionable() bool {
	return r != RiskSurfaceOnly
}

// PathEntry is one known target path, optionally constrained to an
// inclusive host OS version range (paths move between OS releases —
// see docs/research/02 §14). In YAML an entry is either a bare string
// or a map: {path, os_min, os_max}.
type PathEntry struct {
	Path  string `json:"path"`
	OSMin string `json:"os_min,omitempty"`
	OSMax string `json:"os_max,omitempty"`
}

func (p *PathEntry) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		p.Path = node.Value
		return nil
	}
	var raw struct {
		Path  string `yaml:"path"`
		OSMin string `yaml:"os_min"`
		OSMax string `yaml:"os_max"`
	}
	if err := node.Decode(&raw); err != nil {
		return err
	}
	p.Path, p.OSMin, p.OSMax = raw.Path, raw.OSMin, raw.OSMax
	return nil
}

// matches reports whether the entry applies to the given host version.
func (p PathEntry) matches(version string) bool {
	if p.OSMin != "" && compareVersion(version, p.OSMin) < 0 {
		return false
	}
	if p.OSMax != "" && compareVersion(version, p.OSMax) > 0 {
		return false
	}
	return true
}

// Discover describes marker-file discovery for targets that live in
// arbitrary project directories (rust target/ via CACHEDIR.TAG, .venv
// via pyvenv.cfg, node_modules by name).
type Discover struct {
	// Roots to walk; ~ expands to the host home. Missing roots are
	// skipped silently so rules can list conventional locations.
	Roots []string `yaml:"roots" json:"roots"`
	// Name, if set, requires the directory base name to match.
	Name string `yaml:"name" json:"name,omitempty"`
	// Markers are file names that must all exist directly inside a
	// candidate directory for it to count as a hit.
	Markers []string `yaml:"markers" json:"markers,omitempty"`
	// MaxDepth bounds the walk below each root (0 = scanner default).
	MaxDepth int `yaml:"max_depth" json:"max_depth,omitempty"`
	// Exclude lists directory base names never descended into, in
	// addition to the scanner's built-in excludes.
	Exclude []string `yaml:"exclude" json:"exclude,omitempty"`
}

// Argv is a command line. In YAML it is either a plain string (split
// on whitespace) or an explicit argv list for arguments that contain
// spaces — osascript scripts, find patterns.
type Argv []string

// The placeholder convention is part of the rule interface and owned
// here: {path} and {arg} in native_command substitute per item; any
// other {token} is a load-time error (a typo like {id} must never
// silently downgrade a per-item command to run-once-with-a-literal).
// Bare braces ({}, find's marker) are not placeholders.
var placeholderRe = regexp.MustCompile(`\{[a-z_]+\}`)

// Placeholders returns the placeholder tokens the command uses, in
// order of first appearance, deduplicated.
func (a Argv) Placeholders() []string {
	var out []string
	seen := map[string]bool{}
	for _, tok := range a {
		for _, ph := range placeholderRe.FindAllString(tok, -1) {
			if !seen[ph] {
				seen[ph] = true
				out = append(out, ph)
			}
		}
	}
	return out
}

// PerItem reports whether the command must run once per item.
func (a Argv) PerItem() bool { return len(a.Placeholders()) > 0 }

// ExpandItem substitutes the item's values into the command. It
// refuses to expand a placeholder into an empty string — a blank
// {path} or {arg} would silently change what the command targets
// (`simctl delete ""`), so the caller must skip the item instead.
func (a Argv) ExpandItem(it Item) ([]string, error) {
	values := map[string]string{"{path}": it.Path, "{arg}": it.Arg}
	out := make([]string, len(a))
	for i, tok := range a {
		for ph, v := range values {
			if !strings.Contains(tok, ph) {
				continue
			}
			if v == "" {
				return nil, fmt.Errorf("item %q supplies no value for %s", it.Label, ph)
			}
			tok = strings.ReplaceAll(tok, ph, v)
		}
		out[i] = tok
	}
	return out, nil
}

func (a *Argv) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		*a = Argv(strings.Fields(node.Value))
		return nil
	}
	var raw []string
	if err := node.Decode(&raw); err != nil {
		return err
	}
	*a = Argv(raw)
	return nil
}

// FixtureItem is one fake tool-query result the golden harness feeds
// the rule (a docker df row, a simctl device).
type FixtureItem struct {
	Label string `yaml:"label"`
	Arg   string `yaml:"arg"`
	Bytes int64  `yaml:"bytes"`
}

// FixtureHost overrides the fake host a rule's golden test runs
// against (default darwin/15.5) — for version-gated paths and, later,
// linux rules.
type FixtureHost struct {
	OS      string `yaml:"os"`
	Version string `yaml:"version"`
}

// Fixture is the rule's own test data: the golden harness plants Files
// in a fixture home and feeds Items to the rule's tool query as its
// fake result. File paths use rule path syntax (~ = fixture home,
// absolute = under the fixture root). Test-only: the scanner never
// reads it and it is excluded from JSON output. Every embedded rule
// must carry one (CLAUDE.md: every rule gets a golden test) — enforced
// by the harness rather than Validate, so --rules-dir users are not
// forced to write fixtures.
type Fixture struct {
	Files []string      `yaml:"files"`
	Items []FixtureItem `yaml:"items"`
	Host  *FixtureHost  `yaml:"host"`
}

// Regen is the regeneration story shown next to every finding: what
// brings the data back and what that costs (PRODUCT.md pillar 1).
type Regen struct {
	Story string `yaml:"story" json:"story"`
	Cost  string `yaml:"cost" json:"cost"`
}

// Rule is one declarative cleaning target (PRODUCT.md §6). Exactly
// the data a community PR edits; code handles only the weird cases
// via named tool queries.
type Rule struct {
	ID       string `yaml:"id" json:"id"`
	Title    string `yaml:"title" json:"title"`
	Category string `yaml:"category" json:"category"`
	Risk     Risk   `yaml:"risk" json:"risk"`
	// Paths maps GOOS (darwin, linux) to version-aware known paths.
	Paths map[string][]PathEntry `yaml:"paths" json:"paths,omitempty"`
	// Discover enables marker-file discovery.
	Discover *Discover `yaml:"discover" json:"discover,omitempty"`
	// ToolQuery names a built-in scanner query (docker-df, simctl,
	// hf-scan-cache, ...) for targets only a tool can enumerate.
	ToolQuery string `yaml:"tool_query" json:"tool_query,omitempty"`
	// NativeCommand is the steward command preferred over raw
	// deletion. Placeholders {path} and {arg} are substituted per
	// item; without a placeholder the command runs once per rule.
	NativeCommand Argv  `yaml:"native_command" json:"native_command,omitempty"`
	Regen         Regen `yaml:"regen" json:"regen"`
	// Note is a caveat shown alongside the regen story: footguns,
	// prerequisites ("switch to a static wallpaper first"), warnings.
	Note string `yaml:"note" json:"note,omitempty"`
	Sudo bool   `yaml:"sudo" json:"sudo,omitempty"`
	// Beta gates a rule behind --beta-rules: new rules ship beta first
	// and graduate after a release of real-machine burn-in
	// (PRODUCT.md: staged rollout of new rules).
	Beta bool `yaml:"beta" json:"beta,omitempty"`
	// Fixture is the rule's golden-test data; never serialized to JSON.
	Fixture *Fixture `yaml:"fixture" json:"-"`
}

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Validate checks structural invariants a rule must satisfy before it
// enters the catalog.
func (r Rule) Validate() error {
	var errs []string
	if !idRe.MatchString(r.ID) {
		errs = append(errs, fmt.Sprintf("id %q must be kebab-case", r.ID))
	}
	if r.Title == "" {
		errs = append(errs, "title is required")
	}
	if r.Category == "" {
		errs = append(errs, "category is required")
	}
	if !r.Risk.valid() {
		errs = append(errs, fmt.Sprintf("risk %q must be one of safe, caution, surface-only", r.Risk))
	}
	if len(r.Paths) == 0 && r.Discover == nil && r.ToolQuery == "" {
		errs = append(errs, "rule needs at least one of paths, discover, tool_query")
	}
	for osName, entries := range r.Paths {
		if osName != "darwin" && osName != "linux" {
			errs = append(errs, fmt.Sprintf("unknown paths os %q", osName))
		}
		for _, e := range entries {
			if e.Path == "" {
				errs = append(errs, fmt.Sprintf("empty path under os %q", osName))
			}
		}
	}
	if r.Discover != nil {
		if len(r.Discover.Roots) == 0 {
			errs = append(errs, "discover.roots is required")
		}
		if r.Discover.Name == "" && len(r.Discover.Markers) == 0 {
			errs = append(errs, "discover needs a name or markers")
		}
	}
	for _, tok := range r.NativeCommand {
		if tok == "" {
			errs = append(errs, "native_command must not contain empty arguments")
		}
	}
	for _, ph := range r.NativeCommand.Placeholders() {
		switch ph {
		case "{path}", "{arg}":
		default:
			errs = append(errs, fmt.Sprintf("native_command uses unknown placeholder %s (only {path} and {arg} exist)", ph))
		}
		if ph == "{arg}" && r.ToolQuery == "" {
			errs = append(errs, "native_command uses {arg} but only tool_query items supply args")
		}
	}
	if !r.Risk.Actionable() && len(r.NativeCommand) > 0 {
		errs = append(errs, "surface-only rules must not carry a native_command")
	}
	if len(errs) > 0 {
		return fmt.Errorf("rule %s: %s", r.ID, strings.Join(errs, "; "))
	}
	return nil
}
