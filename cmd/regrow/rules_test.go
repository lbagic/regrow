package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lbagic/regrow/internal/config"
	"github.com/lbagic/regrow/internal/engine"
)

var switchCatalog = []engine.Rule{
	{ID: "npm-cache", Title: "npm cache"},
	{ID: "go-build-cache", Title: "Go build cache"},
	{ID: "fresh-rule", Title: "A beta rule", Beta: true},
}

func ids(rules []engine.Rule) string {
	out := make([]string, len(rules))
	for i, r := range rules {
		out[i] = r.ID
	}
	return strings.Join(out, " ")
}

func TestCommandCatalogLeavesOutOffAndBetaRules(t *testing.T) {
	path := "/home/dev/.config/regrow/config.yaml"
	tests := []struct {
		name     string
		betaFlag bool
		rules    config.Rules
		want     string
	}{
		{"by default the beta rules are out", false, config.Rules{}, "npm-cache go-build-cache"},
		{"--beta-rules takes them in", true, config.Rules{}, "npm-cache go-build-cache fresh-rule"},
		{"so does rules: beta", false, config.Rules{Beta: true}, "npm-cache go-build-cache fresh-rule"},
		{"an off rule is out", false, config.Rules{Off: []string{"npm-cache"}}, "go-build-cache"},
		{"an off beta rule stays out with the beta rules in", true, config.Rules{Off: []string{"fresh-rule"}}, "npm-cache go-build-cache"},
		{"an id no rule has changes nothing", false, config.Rules{Off: []string{"gone-rule"}}, "npm-cache go-build-cache"},
	}
	for _, tt := range tests {
		got, err := commandCatalog("scan", nil, tt.betaFlag, switchCatalog, tt.rules, path)
		if err != nil || ids(got) != tt.want {
			t.Errorf("%s: %q, %v; want %q", tt.name, ids(got), err, tt.want)
		}
	}
}

func TestCommandCatalogRefusesAnOffRuleByName(t *testing.T) {
	path := "/home/dev/.config/regrow/config.yaml"
	off := config.Rules{Off: []string{"npm-cache", "go-build-cache"}}
	for _, tt := range []struct {
		cmd string
		ids []string
	}{
		{"clean", []string{"npm-cache"}},
		{"plan", []string{"npm-cache/abc"}},
		{"prune", []string{"go-build"}},
	} {
		_, err := commandCatalog(tt.cmd, tt.ids, false, switchCatalog, off, path)
		if err == nil || !strings.Contains(err.Error(), "turned off in "+path) {
			t.Errorf("%s %v: %v, want a refusal naming the config", tt.cmd, tt.ids, err)
		}
	}
	if _, err := commandCatalog("clean", []string{"go-build-cache/x"}, false, switchCatalog, config.Rules{Off: []string{"npm-cache"}}, path); err != nil {
		t.Errorf("a rule that is on: %v", err)
	}
}

func TestRuleSwitchWritesTheConfigAndReadsItBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "regrow", "config.yaml")
	s := newRuleSwitch(switchCatalog, false, path, config.Rules{})
	if got := ids(s.Catalog()); got != "npm-cache go-build-cache" {
		t.Fatalf("catalog = %q", got)
	}
	yes, no := true, false
	st, err := s.Set("npm-cache", &yes, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Rules[0].Off || st.Rules[0].Runs || !st.Rules[1].Runs || st.Rules[2].Runs || st.Config != path {
		t.Errorf("state after npm-cache off = %+v", st)
	}
	if got := ids(s.Catalog()); got != "go-build-cache" {
		t.Errorf("catalog after npm-cache off = %q", got)
	}
	if st, err = s.Set("", nil, &yes); err != nil || !st.Beta || !st.Rules[2].Runs {
		t.Fatalf("beta on: %+v, %v", st, err)
	}
	if st, err = s.Set("npm-cache", &no, nil); err != nil || st.Rules[0].Off {
		t.Fatalf("npm-cache on: %+v, %v", st, err)
	}
	if got := ids(s.Catalog()); got != "npm-cache go-build-cache fresh-rule" {
		t.Errorf("catalog = %q", got)
	}
	if c, err := config.LoadFile(path); err != nil || !c.Rules.Beta || len(c.Rules.Off) != 0 {
		t.Errorf("file = %+v, %v; want beta and nothing off", c.Rules, err)
	}
	if _, err := s.Set("no-such-rule", &yes, nil); err == nil {
		t.Error("a rule the catalog lacks must be refused")
	}

	// An edit by hand counts; one that breaks the file leaves the last rules.
	if err := os.WriteFile(path, []byte("rules:\n  off: [go-build-cache]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ids(s.Catalog()); got != "npm-cache" {
		t.Errorf("catalog after an edit by hand = %q", got)
	}
	if err := os.WriteFile(path, []byte("rules:\n  of: [npm-cache]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ids(s.Catalog()); got != "npm-cache" {
		t.Errorf("catalog with a broken file = %q, want the last that loaded", got)
	}
	if st := s.State(); st.Error == "" {
		t.Error("the state must say the file does not load")
	}
	if _, err := s.Set("npm-cache", &yes, nil); err == nil {
		t.Error("a change to a file that does not load must fail")
	}
}
