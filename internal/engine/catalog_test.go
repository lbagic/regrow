package engine

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// One owner per byte: the containment forest can only nest what it
// sees as nested, and it compares paths byte for byte. So no two rules
// may name the same path, even in another case or through a macOS
// system symlink, and two rules that share a prefix must spell it the
// same way. Two rules discovering the same directory name would both
// claim every hit.
func ownerConflicts(catalog []Rule) []string {
	type spelling struct{ rule, path string }
	var conflicts []string
	for _, osName := range []string{"darwin", "linux"} {
		owner := map[string]spelling{}
		var all []spelling
		for _, r := range catalog {
			for _, e := range r.Paths[osName] {
				p := filepath.Clean(e.Path)
				all = append(all, spelling{r.ID, p})
				key := ownerKey(osName, p)
				if prev, ok := owner[key]; ok && prev.rule != r.ID {
					conflicts = append(conflicts, fmt.Sprintf("%s: %s (%s) and %s (%s) name one path", osName, prev.rule, prev.path, r.ID, p))
					continue
				}
				owner[key] = spelling{r.ID, p}
			}
		}
		for _, outer := range all {
			for _, inner := range all {
				if outer.rule == inner.rule {
					continue
				}
				nested := strings.HasPrefix(ownerKey(osName, inner.path), ownerKey(osName, outer.path)+"/")
				if nested && !strings.HasPrefix(inner.path, outer.path+"/") {
					conflicts = append(conflicts, fmt.Sprintf("%s: %s (%s) is inside %s (%s) but spells the prefix differently", osName, inner.rule, inner.path, outer.rule, outer.path))
				}
			}
		}
	}
	names := map[string]string{}
	for _, r := range catalog {
		if r.Discover == nil || r.Discover.Name == "" {
			continue
		}
		if prev, ok := names[r.Discover.Name]; ok {
			conflicts = append(conflicts, fmt.Sprintf("%s and %s both discover %q", prev, r.ID, r.Discover.Name))
			continue
		}
		names[r.Discover.Name] = r.ID
	}
	slices.Sort(conflicts)
	return conflicts
}

// ownerKey is how the filesystem compares a path: APFS is
// case-insensitive by default, and /tmp, /var and /etc are symlinks
// into /private on macOS.
func ownerKey(osName, path string) string {
	if osName == "darwin" {
		for _, link := range []string{"/tmp", "/var", "/etc"} {
			if path == link || strings.HasPrefix(path, link+"/") {
				path = "/private" + path
				break
			}
		}
	}
	return strings.ToLower(path)
}

func TestCatalogOneOwnerPerByte(t *testing.T) {
	catalog, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range ownerConflicts(catalog) {
		t.Error(c)
	}
}

func TestOwnerConflicts(t *testing.T) {
	pathRule := func(id string, paths ...string) Rule {
		entries := make([]PathEntry, len(paths))
		for i, p := range paths {
			entries[i] = PathEntry{Path: p}
		}
		return Rule{ID: id, Paths: map[string][]PathEntry{"darwin": entries}}
	}
	discoverRule := func(id, name string) Rule {
		return Rule{ID: id, Discover: &Discover{Roots: []string{"~"}, Name: name}}
	}
	cases := []struct {
		name    string
		catalog []Rule
		want    int
	}{
		{"same literal path", []Rule{pathRule("a", "~/Library/Caches/x"), pathRule("b", "~/Library/Caches/x")}, 1},
		{"trailing slash", []Rule{pathRule("a", "~/.npm"), pathRule("b", "~/.npm/")}, 1},
		{"other case", []Rule{pathRule("a", "~/Library/Caches/Yarn"), pathRule("b", "~/Library/Caches/yarn")}, 1},
		{"through /tmp", []Rule{pathRule("a", "/tmp/x"), pathRule("b", "/private/tmp/x")}, 1},
		{"prefix in other case", []Rule{pathRule("a", "~/Library/Caches"), pathRule("b", "~/library/caches/go-build")}, 1},
		{"same discover name", []Rule{discoverRule("a", "dist"), discoverRule("b", "dist")}, 1},
		{"nested, same spelling", []Rule{pathRule("a", "~/Library/Caches"), pathRule("b", "~/Library/Caches/go-build")}, 0},
		{"one rule, two OS", []Rule{{ID: "a", Paths: map[string][]PathEntry{"darwin": {{Path: "~/.npm"}}, "linux": {{Path: "~/.npm"}}}}}, 0},
		{"distinct names", []Rule{discoverRule("a", "target"), discoverRule("b", "node_modules")}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ownerConflicts(tc.catalog); len(got) != tc.want {
				t.Errorf("got %d conflicts, want %d: %q", len(got), tc.want, got)
			}
		})
	}
}

// The aerial row's fix is the owner's instructions for a download that
// eats tens of GB: the lines that stop it must not drop out of the
// catalog unnoticed. regrow clears the picks and then deletes the
// videos, as root and only while nothing is picked.
func TestAerialCauseFixText(t *testing.T) {
	catalog, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	var rule *Rule
	var fix []string
	for i, r := range catalog {
		if r.ID != "aerial-wallpapers" {
			continue
		}
		rule = &catalog[i]
		for _, c := range r.Causes {
			if c.Check == "aerial-downloads" {
				fix = c.Fix
			}
		}
	}
	if rule == nil || fix == nil {
		t.Fatal("aerial-wallpapers declares no aerial-downloads cause")
	}
	if rule.Risk != RiskCaution || !rule.Sudo || rule.Hold != HoldAerialPicks || rule.PreAction != PreActionHoldRecheck || rule.Measure != MeasureAerialVideos || rule.NativeCommand.PerItem() {
		t.Errorf("risk %q, sudo %v, hold %q, pre_action %q, measure %q, command %q; want a caution whole-rule command as root, held while picked, rechecked and measured",
			rule.Risk, rule.Sudo, rule.Hold, rule.PreAction, rule.Measure, rule.NativeCommand)
	}
	// On macOS 26 regrow only shows the videos, so the story gives the
	// command, quoted after a colon so a face sets it on a line of its own.
	const removeNew = `sudo find ~/Library/Containers/com.apple.wallpaper.agent/Data/Library/Caches -type f -name '*.mov' -delete`
	if !strings.Contains(rule.Regen.Story, "To remove them yourself there: `"+removeNew+"`") {
		t.Errorf("the row's story does not give %q:\n%s", removeNew, rule.Regen.Story)
	}
	line := func(parts ...string) bool {
		return slices.ContainsFunc(fix, func(l string) bool {
			for _, p := range parts {
				if !strings.Contains(l, p) {
					return false
				}
			}
			return true
		})
	}
	// idleassetsd downloads for the aerials picked in its database, and
	// a still wallpaper leaves them picked: clearing the picks, then
	// deleting the videos, is what keeps them gone on macOS 15.7.
	for i, want := range [][]string{
		{"still wallpaper", ": regrow aerials off"},
		{"Then delete the videos", ": regrow clean aerial-wallpapers"},
		{"pick the same aerials again", ": regrow aerials on"},
		{"Picking an aerial", "System Settings ▸ Wallpaper", "again"},
	} {
		if !line(want...) {
			t.Errorf("no fix line with %q in:\n%s", want, strings.Join(fix, "\n"))
		} else if i < 2 && !strings.Contains(fix[i], want[0]) {
			t.Errorf("fix line %d is not the one with %q: clear, then delete, in that order:\n%s", i+1, want[0], strings.Join(fix, "\n"))
		}
	}
	// A command ends its line, so what follows the colon pastes.
	for _, cmd := range []string{"regrow aerials off", "regrow clean aerial-wallpapers", "regrow aerials on"} {
		if i := slices.IndexFunc(fix, func(l string) bool { return strings.Contains(l, ": "+cmd) }); i < 0 || !strings.HasSuffix(fix[i], ": "+cmd) {
			t.Errorf("the fix line of %q does not end with it, so a pasted line runs more:\n%s", cmd, strings.Join(fix, "\n"))
		}
	}
	// launchd starts the downloader on demand while it is disabled.
	for _, l := range fix {
		if strings.Contains(l, "launchctl disable") && !strings.Contains(l, "does not stop") {
			t.Errorf("a fix line offers launchctl disable, which does not stop the downloads: %s", l)
		}
	}
}

// A surface-only row exists to tell the owner what to do: the rows for
// space regrow cannot remove must keep naming the setting or command
// that does, and every rule of the kind ships behind --beta-rules.
func TestSurfaceRowsNameTheirFix(t *testing.T) {
	catalog, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Rule{}
	for _, r := range catalog {
		byID[r.ID] = r
	}
	for id, wants := range map[string][]string{
		"macos-update-download": {"Software Update", "softwareupdate --list", "Download new updates when available", "sudo defaults write /Library/Preferences/com.apple.SoftwareUpdate AutomaticDownload -bool false"},
		"chrome-ai-model":       {"Settings ▸ AI Innovations", "On-device AI", "GenAILocalFoundationalModelSettings"},
	} {
		r, ok := byID[id]
		if !ok {
			t.Errorf("%s is missing from the catalog", id)
			continue
		}
		if r.Risk != RiskSurfaceOnly || !r.Beta {
			t.Errorf("%s: risk %q, beta %v; want a beta surface-only rule", id, r.Risk, r.Beta)
		}
		for _, w := range wants {
			if !strings.Contains(r.Note, w) {
				t.Errorf("%s: note %q does not name %q", id, r.Note, w)
			}
		}
	}
}
