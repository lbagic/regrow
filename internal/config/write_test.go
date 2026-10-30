package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSetRulesWritesAFileThereWasNone(t *testing.T) {
	p := filepath.Join(t.TempDir(), "regrow", "config.yaml")
	if err := SetRules(p, Rules{Off: []string{"npm-cache", "brew-cache"}}); err != nil {
		t.Fatal(err)
	}
	c, err := LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"brew-cache", "npm-cache"}; !reflect.DeepEqual(c.Rules.Off, want) || c.Rules.Beta {
		t.Errorf("rules = %+v, want off %v sorted and beta off", c.Rules, want)
	}
	if !c.Rules.IsOff("npm-cache") || c.Rules.IsOff("pip-cache") {
		t.Errorf("IsOff: npm-cache %v, pip-cache %v", c.Rules.IsOff("npm-cache"), c.Rules.IsOff("pip-cache"))
	}
}

func TestSetRulesKeepsTheRestOfTheFile(t *testing.T) {
	p := writeConfig(t, `# my regrow settings
docker:
  keep:
    - myproj_* # the project databases
  volume_export_cap: 2GiB
rules:
  off: [npm-cache] # for now
`)
	if err := SetRules(p, Rules{Off: []string{"pip-cache"}, Beta: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# my regrow settings", "myproj_* # the project databases", "volume_export_cap: 2GiB", "pip-cache", "beta: true"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the file lacks %q:\n%s", want, data)
		}
	}
	if strings.Contains(string(data), "npm-cache") {
		t.Errorf("the old list stayed:\n%s", data)
	}
	c, err := LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Docker.Keeps("myproj_db", "") || !c.Rules.Beta || !c.Rules.IsOff("pip-cache") {
		t.Errorf("config = %+v", c)
	}

	if err := SetRules(p, Rules{}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(p); strings.Contains(string(data), "rules") || !strings.Contains(string(data), "myproj_*") {
		t.Errorf("empty rules must drop only the key:\n%s", data)
	}
}

func TestSetRulesToNothingLeavesAFileThatLoads(t *testing.T) {
	p := writeConfig(t, "rules:\n  off: [npm-cache]\n")
	if err := SetRules(p, Rules{}); err != nil {
		t.Fatal(err)
	}
	if c, err := LoadFile(p); err != nil || len(c.Rules.Off) != 0 {
		t.Fatalf("config = %+v, %v; want the defaults", c, err)
	}
	// Not empty: a regrow older than the rules key fails on that with a bare EOF.
	if data, _ := os.ReadFile(p); string(data) != "{}\n" {
		t.Errorf("file = %q, want {}", data)
	}

	none := filepath.Join(t.TempDir(), "config.yaml")
	if err := SetRules(none, Rules{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(none); err == nil {
		t.Error("nothing to write must leave no file")
	}
}

func TestSetRulesKeepsAFileOfCommentsAlone(t *testing.T) {
	for _, body := range []string{"# docker:\n#   keep: [myproj_*]\n", "---\n", "--- # regrow\n"} {
		p := writeConfig(t, body)
		if err := SetRules(p, Rules{Off: []string{"npm-cache"}}); err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		data, _ := os.ReadFile(p)
		if !strings.HasPrefix(string(data), strings.TrimRight(body, "\n")) {
			t.Errorf("%q became %q: its text must stay", body, data)
		}
		if c, err := LoadFile(p); err != nil || !c.Rules.IsOff("npm-cache") {
			t.Errorf("%q: config = %+v, %v", body, c.Rules, err)
		}
	}
}

func TestSetRulesWritesThroughALink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles", "regrow.yaml")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("docker:\n  volume_export_cap: 2GiB\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := SetRules(link, Rules{Off: []string{"npm-cache"}}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("config.yaml is no longer a link: %v, %v", info, err)
	}
	if c, err := LoadFile(target); err != nil || !c.Rules.IsOff("npm-cache") || c.Docker.VolumeExportCap != "2GiB" {
		t.Errorf("the linked file = %+v, %v", c, err)
	}
}

func TestSetRulesLeavesABrokenFileAlone(t *testing.T) {
	body := "docker:\n  kep: [x]\n"
	p := writeConfig(t, body)
	if err := SetRules(p, Rules{Off: []string{"npm-cache"}}); err == nil {
		t.Fatal("a file that does not load must not be rewritten")
	}
	if data, _ := os.ReadFile(p); string(data) != body {
		t.Errorf("the file changed:\n%s", data)
	}
	if entries, _ := os.ReadDir(filepath.Dir(p)); len(entries) != 1 {
		t.Errorf("a temp file stayed beside the config: %v", entries)
	}
}

// Every rule back on drops the key, and the comments around it stay.
func TestSetRulesToNothingKeepsTheCommentsAroundTheKey(t *testing.T) {
	for _, tc := range []struct{ name, file string }{
		{"between keys", "docker:\n  volume_export_cap: 2GiB\n# rule switches\nrules:\n  off: [pip-cache]\n# tail note\n"},
		{"before a key", "# rule switches\nrules:\n  off: [pip-cache]\n# about docker\ndocker:\n  volume_export_cap: 2GiB\n"},
		{"alone", "# my regrow settings\nrules:\n  off: [pip-cache]\n"},
		{"after a blank line", "rules:\n  off: [pip-cache]\n\n# tail note\n"},
		{"on the key's line", "docker:\n  volume_export_cap: 2GiB\nrules: # my switches\n  off: [pip-cache]\n"},
		{"after a flow mapping", "docker:\n  volume_export_cap: 2GiB\nrules: {off: [pip-cache]} # my switches\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := writeConfig(t, tc.file)
			if err := SetRules(p, Rules{}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(tc.file, "\n") {
				if _, c, ok := strings.Cut(line, "#"); ok && !strings.Contains(string(data), "#"+c) {
					t.Errorf("%q is gone:\n%s", "#"+c, data)
				}
			}
			if strings.Contains(string(data), "pip-cache") {
				t.Errorf("the key stayed:\n%s", data)
			}
			if _, err := LoadFile(p); err != nil {
				t.Errorf("the file does not load: %v\n%s", err, data)
			}
		})
	}
}

// A file written {} goes back to one key a line when a key is added.
func TestSetRulesAfterAnEmptyFileWritesBlockStyle(t *testing.T) {
	p := writeConfig(t, "rules:\n  off: [npm-cache]\n")
	for _, r := range []Rules{{}, {Off: []string{"pip-cache"}}} {
		if err := SetRules(p, r); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "{") {
		t.Errorf("a flow mapping:\n%s", data)
	}
}

// A link to a file not there is refused: where the file would go
// depends on links regrow cannot follow, and the link stays a link.
func TestSetRulesRefusesALinkToNoFile(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "config.yaml")
	if err := os.Symlink(filepath.Join("dotfiles", "regrow.yaml"), link); err != nil {
		t.Fatal(err)
	}
	if err := SetRules(link, Rules{Off: []string{"npm-cache"}}); err == nil || !strings.Contains(err.Error(), "which is not there") {
		t.Errorf("SetRules = %v, want the link refused", err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced: %v, %v", info, err)
	}
}

// A second YAML document would be read by nothing, a rule it turns off
// still running: the file does not load, and nothing writes over it.
func TestAFileOfTwoDocumentsDoesNotLoad(t *testing.T) {
	text := "docker:\n  volume_export_cap: 2GiB\n---\nrules:\n  off: [pip-cache]\n"
	p := writeConfig(t, text)
	if _, err := LoadFile(p); err == nil || !strings.Contains(err.Error(), "more than one YAML document") {
		t.Errorf("LoadFile = %v, want the second document named", err)
	}
	if err := SetRules(p, Rules{Off: []string{"npm-cache"}}); err == nil {
		t.Error("SetRules wrote over a file that does not load")
	}
	if data, _ := os.ReadFile(p); string(data) != text {
		t.Errorf("the file changed:\n%s", data)
	}
	third := writeConfig(t, "docker:\n  volume_export_cap: 2GiB\n---\n---\nrules:\n  off: [pip-cache]\n")
	if _, err := LoadFile(third); err == nil {
		t.Error("a rules: in a third document loaded unseen")
	}
	for _, ok := range []string{"---\ndocker:\n  volume_export_cap: 2GiB\n", "docker:\n  volume_export_cap: 2GiB\n---\n", "---\n---\n"} {
		if _, err := LoadFile(writeConfig(t, ok)); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
}

// A file of "---" lines, comments or not, takes a switch: the markers
// past the first go, its comments stay.
func TestSetRulesOnAFileOfMarkers(t *testing.T) {
	for _, file := range []string{"---\n---\n", "# my settings\n---\n---\n", "---\n...\n"} {
		p := writeConfig(t, file)
		if err := SetRules(p, Rules{Off: []string{"npm-cache"}}); err != nil {
			t.Fatalf("%q: %v", file, err)
		}
		data, _ := os.ReadFile(p)
		if strings.HasPrefix(file, "#") && !strings.Contains(string(data), "# my settings") {
			t.Errorf("%q: the comment is gone:\n%s", file, data)
		}
		if c, err := LoadFile(p); err != nil || !c.Rules.IsOff("npm-cache") {
			t.Errorf("%q: %+v, %v\n%s", file, c.Rules, err, data)
		}
	}
}

// A link on the way to config.yaml that names nothing is no config to
// read, not an empty one: the rules it turned off would run.
func TestLoadFileRefusesALinkToNothing(t *testing.T) {
	dir := t.TempDir()
	mustLink := func(target, link string) {
		t.Helper()
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	mustLink(filepath.Join(dir, "moved.yaml"), filepath.Join(dir, "config.yaml"))
	if _, err := LoadFile(filepath.Join(dir, "config.yaml")); err == nil || !strings.Contains(err.Error(), "which is not there") {
		t.Errorf("a link to no file: %v", err)
	}
	mustLink(filepath.Join(dir, "unmounted"), filepath.Join(dir, "regrow"))
	if _, err := LoadFile(filepath.Join(dir, "regrow", "config.yaml")); err == nil || !strings.Contains(err.Error(), "which is not there") {
		t.Errorf("a linked folder that is gone: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "dotfiles"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustLink(filepath.Join(dir, "dotfiles"), filepath.Join(dir, "linked"))
	if c, err := LoadFile(filepath.Join(dir, "linked", "config.yaml")); err != nil || len(c.Rules.Off) != 0 {
		t.Errorf("a linked folder with no file yet is the defaults: %+v, %v", c.Rules, err)
	}
	if _, err := LoadFile(filepath.Join(dir, "config.yaml")); err == nil || !strings.Contains(err.Error(), "moved.yaml") {
		t.Errorf("the error names the link's missing target: %v", err)
	}
}

// The error names the path that is missing, as an absolute path, past
// any link on the way that is there.
func TestALinkToNothingNamesWhatIsMissing(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mustLink := func(target, link string) {
		t.Helper()
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	mustLink("a.yaml", filepath.Join(dir, "chain.yaml"))
	mustLink("missing.yaml", filepath.Join(dir, "a.yaml"))
	mustLink(filepath.Join("dotfiles", "regrow.yaml"), filepath.Join(dir, "relative.yaml"))
	mustLink(filepath.Join(dir, "d2"), filepath.Join(dir, "d1"))
	mustLink(filepath.Join(dir, "gone"), filepath.Join(dir, "d2"))
	if err := os.MkdirAll(filepath.Join(dir, "deep", "real"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustLink(filepath.Join(dir, "deep", "real"), filepath.Join(dir, "linked"))
	mustLink(filepath.Join("..", "up.yaml"), filepath.Join(dir, "deep", "real", "config.yaml"))
	for _, c := range []struct{ path, missing string }{
		{filepath.Join(dir, "chain.yaml"), filepath.Join(dir, "missing.yaml")},
		{filepath.Join(dir, "relative.yaml"), filepath.Join(dir, "dotfiles", "regrow.yaml")},
		{filepath.Join(dir, "d1", "config.yaml"), filepath.Join(dir, "gone")},
		{filepath.Join(dir, "linked", "config.yaml"), filepath.Join(dir, "deep", "up.yaml")},
	} {
		if _, err := LoadFile(c.path); err == nil || !strings.Contains(err.Error(), "leads to "+c.missing+", which is not there") {
			t.Errorf("%s: %v, want %s named", c.path, err, c.missing)
		}
	}
}

// SetRules refuses a folder link that names nothing, saying so, and a
// loop of links, which it leaves a link.
func TestSetRulesRefusesABrokenLink(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "unmounted"), filepath.Join(dir, "regrow")); err != nil {
		t.Fatal(err)
	}
	if err := SetRules(filepath.Join(dir, "regrow", "config.yaml"), Rules{Off: []string{"npm-cache"}}); err == nil || !strings.Contains(err.Error(), "which is not there") {
		t.Errorf("a linked folder that is gone: %v", err)
	}
	loop := filepath.Join(dir, "loop.yaml")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	if err := SetRules(loop, Rules{Off: []string{"npm-cache"}}); err == nil {
		t.Error("a loop of links took a write")
	}
	if info, err := os.Lstat(loop); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the loop was replaced: %v, %v", info, err)
	}
}
