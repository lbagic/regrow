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
