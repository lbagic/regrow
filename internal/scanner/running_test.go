package scanner

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lbagic/regrow/internal/engine"
)

// appBundle plants an app bundle whose Info.plist names id, and returns
// the bundle and its executable.
func appBundle(t *testing.T, dir, name, id string) (bundle, exe string) {
	t.Helper()
	bundle = filepath.Join(dir, name)
	writeText(t, filepath.Join(bundle, "Contents", "Info.plist"), launchdPlist("", nil, map[string]string{"CFBundleIdentifier": id}))
	exe = filepath.Join(bundle, "Contents", "MacOS", strings.TrimSuffix(name, ".app"))
	writeText(t, exe, "")
	return bundle, exe
}

func TestRunningKeeps(t *testing.T) {
	apps := t.TempDir()
	_, slack := appBundle(t, apps, "Slack.app", "com.tinyspeck.slackmacgap")
	slackBundle := filepath.Join(apps, "Slack.app")
	helper := filepath.Join(slackBundle, "Contents", "Frameworks", "Slack Helper.app", "Contents", "MacOS", "Slack Helper")
	// Steam runs from a bundle not named .app.
	_, steam := appBundle(t, apps, "Steam.AppBundle/Steam", "com.valvesoftware.steam")
	noPlist := filepath.Join(apps, "Tool.app", "Contents", "MacOS", "tool")
	writeText(t, noPlist, "")
	cache := filepath.Join(t.TempDir(), "Caches", "bun")
	gopls := filepath.Join(t.TempDir(), "go", "bin", "gopls")

	cases := []struct {
		name   string
		u      engine.UnlessRunning
		mapped []string
		procs  processes
		path   string
		want   string // part of why; "" keeps nothing
	}{
		{"the app runs", engine.UnlessRunning{Apps: []string{"com.tinyspeck.slackmacgap"}}, []string{slack}, processes{}, cache, "com.tinyspeck.slackmacgap"},
		{"only a helper of the app runs", engine.UnlessRunning{Apps: []string{"com.tinyspeck.slackmacgap"}}, []string{helper}, processes{}, cache, "com.tinyspeck.slackmacgap"},
		{"another app runs", engine.UnlessRunning{Apps: []string{"com.hnc.Discord"}}, []string{slack}, processes{}, cache, ""},
		{"an app from a bundle not named .app", engine.UnlessRunning{Apps: []string{"com.valvesoftware.steam"}}, []string{steam}, processes{}, cache, "com.valvesoftware.steam"},
		{"a bundle with no Info.plist", engine.UnlessRunning{Apps: []string{"com.hnc.Discord"}}, []string{noPlist}, processes{}, cache, ""},
		{"the program runs", engine.UnlessRunning{Commands: []string{"gopls"}}, []string{gopls}, processes{}, cache, "`gopls` is running"},
		{"a program of that name in another case", engine.UnlessRunning{Commands: []string{"gopls"}}, []string{strings.ToUpper(gopls)}, processes{}, cache, "is running"},
		{"another program runs", engine.UnlessRunning{Commands: []string{"bun"}}, []string{gopls}, processes{}, cache, ""},
		{"a process works inside", engine.UnlessRunning{Inside: true}, nil, processes{cwds: []string{filepath.Join(cache, "x")}}, cache, "works in it"},
		{"a command line names it", engine.UnlessRunning{Inside: true}, nil, processes{args: []string{"bun install --cache-dir " + cache}}, cache, "names it"},
		{"a process maps a file from it", engine.UnlessRunning{Inside: true}, []string{filepath.Join(cache, "lib.dylib")}, processes{}, cache, "maps a file from it"},
		{"inside, but elsewhere", engine.UnlessRunning{Inside: true}, []string{gopls}, processes{cwds: []string{filepath.Dir(cache) + "/bun-other"}}, cache, ""},
		{"inside is not asked", engine.UnlessRunning{Commands: []string{"bun"}}, nil, processes{cwds: []string{cache}}, cache, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &runningSet{mapped: tc.mapped, procs: tc.procs, w: testWalker()}
			why, err := s.keeps(context.Background(), tc.u, tc.path)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" && why != "" || tc.want != "" && !strings.Contains(why, tc.want) {
				t.Errorf("keeps = %q, want %q", why, tc.want)
			}
		})
	}
}

// A running app whose Info.plist cannot be read may be the one the
// rule names.
func TestRunningKeepsUnreadableBundle(t *testing.T) {
	bundle, exe := appBundle(t, t.TempDir(), "Slack.app", "com.tinyspeck.slackmacgap")
	chmod(t, filepath.Join(bundle, "Contents"), 0)
	s := &runningSet{mapped: []string{exe}, w: testWalker()}
	if why, err := s.keeps(context.Background(), engine.UnlessRunning{Apps: []string{"com.hnc.Discord"}}, "/x"); err == nil {
		t.Errorf("keeps = %q with no error, want the unreadable Info.plist as an error", why)
	}
}

func guardedRule(dir string) engine.Rule {
	return engine.Rule{
		ID: "guarded", Title: "Guarded", Category: "app-caches", Risk: engine.RiskSafe, Beta: true,
		Paths:         map[string][]engine.PathEntry{"darwin": {{Path: filepath.Join(dir, "a")}, {Path: filepath.Join(dir, "b")}}},
		UnlessRunning: &engine.UnlessRunning{Commands: []string{"bun"}, Inside: true},
		PreAction:     engine.PreActionRunningRecheck,
	}
}

// The scan reads the processes once for every guarded rule, drops the
// items kept now, and offers nothing when they cannot be read.
func TestScanGuardsRunningRules(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a", "x"), 4096)
	writeFile(t, filepath.Join(dir, "b", "x"), 4096)
	r := guardedRule(dir)
	other := r
	other.ID, other.UnlessRunning = "guarded-too", &engine.UnlessRunning{Commands: []string{"gopls"}}
	plain := r
	plain.ID, plain.UnlessRunning, plain.PreAction = "plain", nil, ""

	reads := 0
	s := New(engine.Host{OS: "darwin", Version: "15.7.7", Home: dir})
	s.Queries = nil
	set := &runningSet{w: testWalker(), procs: processes{cwds: []string{filepath.Join(dir, "a")}}}
	s.running = func(context.Context) (*runningSet, error) { reads++; return set, nil }
	findings := s.Scan(context.Background(), []engine.Rule{r, other, plain})
	if reads != 1 {
		t.Errorf("processes read %d times, want once per scan", reads)
	}
	if f := findings[0]; f.Err != "" || len(f.Items) != 1 || f.Items[0].Path != filepath.Join(dir, "b") {
		t.Errorf("guarded: %+v, want only b, a being worked in", f)
	}
	if f := findings[1]; f.Err != "" || len(f.Items) != 2 {
		t.Errorf("guarded by gopls alone: %+v, want both", f)
	}

	set.mapped = []string{"/opt/homebrew/bin/bun"}
	findings = s.Scan(context.Background(), []engine.Rule{r, plain})
	if f := findings[0]; f.Err != "" || len(f.Items) != 0 {
		t.Errorf("with bun running: %+v, want nothing offered", f)
	}
	if f := findings[1]; len(f.Items) != 2 {
		t.Errorf("an unguarded rule: %+v, want both items whatever runs", f)
	}

	s.running = func(context.Context) (*runningSet, error) {
		return nil, errors.New("lsof could not list running processes")
	}
	findings = s.Scan(context.Background(), []engine.Rule{r, plain})
	if f := findings[0]; !strings.Contains(f.Err, "lsof could not list") || len(f.Items) != 0 {
		t.Errorf("processes unknown: %+v, want an error and nothing offered", f)
	}
	if f := findings[1]; f.Err != "" || len(f.Items) != 2 {
		t.Errorf("an unguarded rule with processes unknown: %+v, want both and no error", f)
	}

	// A guarded rule with nothing found reads no processes.
	reads = 0
	s.running = func(context.Context) (*runningSet, error) { reads++; return set, nil }
	empty := guardedRule(filepath.Join(dir, "none"))
	if findings := s.Scan(context.Background(), []engine.Rule{empty}); findings[0].Err != "" || reads != 0 {
		t.Errorf("nothing found: %+v, %d reads; want no error and no read", findings[0], reads)
	}
}

func TestRecheckRunning(t *testing.T) {
	dir := t.TempDir()
	r := guardedRule(dir)
	item := filepath.Join(dir, "a")
	free := func(context.Context) (*runningSet, error) { return &runningSet{w: testWalker()}, nil }
	if err := recheckRunning(context.Background(), r, item, free); err != nil {
		t.Errorf("nothing running: %v, want the move to go ahead", err)
	}
	busy := func(context.Context) (*runningSet, error) {
		return &runningSet{w: testWalker(), procs: processes{args: []string{"bun x " + item + "/pkg"}}}, nil
	}
	if err := recheckRunning(context.Background(), r, item, busy); err == nil || !strings.Contains(err.Error(), "names it") {
		t.Errorf("a process naming it: %v, want a refusal", err)
	}
	unknown := func(context.Context) (*runningSet, error) { return nil, errors.New("ps failed") }
	if err := recheckRunning(context.Background(), r, item, unknown); err == nil || !strings.Contains(err.Error(), "ps failed") {
		t.Errorf("processes unknown: %v, want a refusal", err)
	}
	r.UnlessRunning = nil
	if err := recheckRunning(context.Background(), r, item, free); err == nil {
		t.Error("a rule with no guard: want a refusal")
	}
}
