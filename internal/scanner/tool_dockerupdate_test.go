package scanner

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

func dockerPlist(version string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>CFBundleIdentifier</key>
	<string>com.docker.docker</string>
	<key>CFBundleShortVersionString</key>
	<string>` + version + `</string>
</dict>
</plist>
`
}

func TestDockerUpdateDownload(t *testing.T) {
	type fixture struct {
		staged    string // the staged app's version; "" writes no Info.plist
		installed string // "" installs no Docker.app
		age       time.Duration
		noApp     bool // in_progress holds no Docker.app
	}
	cases := []struct {
		name    string
		fx      fixture
		procs   processes
		mapped  []string
		label   string // "" when kept
		why     string
		wantErr string
	}{
		{name: "an unfinished download", fx: fixture{installed: "4.93.0", age: 6 * 24 * time.Hour},
			label: "Docker Desktop update, never finished downloading"},
		{name: "an unfinished download, Docker gone", fx: fixture{age: 6 * 24 * time.Hour},
			label: "Docker Desktop update, never finished downloading"},
		{name: "a finished update not newer", fx: fixture{staged: "4.93.0", installed: "4.93.0", age: 6 * 24 * time.Hour},
			label: "Docker Desktop 4.93.0 update, not newer than the installed 4.93.0"},
		{name: "a finished update waiting", fx: fixture{staged: "4.94.0", installed: "4.93.0", age: 6 * 24 * time.Hour},
			why: "newer than the installed 4.93.0"},
		{name: "a finished update, nothing installed", fx: fixture{staged: "4.94.0", age: 6 * 24 * time.Hour},
			why: "no installed Docker.app"},
		{name: "a download going on", fx: fixture{installed: "4.93.0", age: time.Hour},
			why: "changed in the last day"},
		{name: "a process working in it", fx: fixture{installed: "4.93.0", age: 6 * 24 * time.Hour},
			procs: processes{cwds: []string{"<dir>/Docker.app"}}, why: "works in it"},
		{name: "a process running from it", fx: fixture{installed: "4.93.0", age: 6 * 24 * time.Hour},
			mapped: []string{"<dir>/Docker.app/Contents/MacOS/install"}, why: "a running process uses"},
		{name: "Docker Desktop running", fx: fixture{installed: "4.93.0", age: 6 * 24 * time.Hour},
			mapped: []string{"<app>/Contents/MacOS/com.docker.backend"}, why: "Docker Desktop is running"},
		{name: "Docker Desktop running, nothing in the folder newer", fx: fixture{staged: "4.93.0", installed: "4.93.0", age: 6 * 24 * time.Hour},
			mapped: []string{"<app>/Contents/MacOS/Docker Desktop.app/Contents/MacOS/Docker Desktop"}, why: "Docker Desktop is running"},
		{name: "Docker Desktop running from another folder", fx: fixture{installed: "4.93.0", age: 6 * 24 * time.Hour},
			mapped: []string{"/Applications/Dev/Docker.app/Contents/MacOS/com.docker.backend"}, why: "Docker Desktop is running"},
		{name: "no Docker.app in it", fx: fixture{noApp: true, age: 6 * 24 * time.Hour},
			why: "holds no Docker.app"},
		{name: "a staged version that does not parse", fx: fixture{staged: "next", installed: "4.93.0", age: 6 * 24 * time.Hour},
			wantErr: "no version"},
	}
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			d := newDockerUpdate(home)
			d.w = testWalker()
			d.apps = []string{filepath.Join(home, "Applications/Docker.app")}
			d.now = func() time.Time { return now }
			fix := func(list []string) []string {
				var out []string
				for _, s := range list {
					out = append(out, strings.NewReplacer("<dir>", d.dir, "<app>", d.apps[0]).Replace(s))
				}
				return out
			}
			procs := processes{cwds: fix(c.procs.cwds), args: fix(c.procs.args)}
			d.procs = func(context.Context) (processes, error) { return procs, nil }
			d.mapped = func(context.Context) ([]string, error) { return fix(c.mapped), nil }
			if c.fx.noApp {
				writeFile(t, filepath.Join(d.dir, "payload"), 4096)
			} else {
				writeFile(t, filepath.Join(d.dir, "Docker.app/Contents/MacOS/install"), 4096)
				if c.fx.staged != "" {
					writeText(t, filepath.Join(d.dir, "Docker.app/Contents/Info.plist"), dockerPlist(c.fx.staged))
				}
			}
			if c.fx.installed != "" {
				writeText(t, filepath.Join(home, "Applications/Docker.app/Contents/Info.plist"), dockerPlist(c.fx.installed))
			}
			at := now.Add(-c.fx.age)
			if err := filepath.WalkDir(d.dir, func(p string, _ fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				return os.Chtimes(p, at, at)
			}); err != nil {
				t.Fatal(err)
			}

			item, why, err := d.judge(context.Background())
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.label != "" {
				if item == nil || item.Label != c.label || item.Path != d.dir || item.Bytes <= 0 {
					t.Errorf("item %+v (kept for %q), want %q with the folder's size", item, why, c.label)
				}
				return
			}
			if item != nil || !strings.Contains(why, c.why) {
				t.Errorf("item %+v, kept for %q; want kept for %q", item, why, c.why)
			}
		})
	}

	t.Run("no folder", func(t *testing.T) {
		d := newDockerUpdate(t.TempDir())
		d.w = testWalker()
		if item, why, err := d.judge(context.Background()); item != nil || why != "" || err != nil {
			t.Errorf("item %+v, why %q, err %v; want nothing", item, why, err)
		}
	})

	t.Run("processes that cannot be listed", func(t *testing.T) {
		home := t.TempDir()
		d := newDockerUpdate(home)
		d.w = testWalker()
		d.procs = func(context.Context) (processes, error) { return processes{}, errors.New("ps failed") }
		writeFile(t, filepath.Join(d.dir, "Docker.app/Contents/MacOS/install"), 4096)
		if item, _, err := d.judge(context.Background()); err == nil {
			t.Errorf("item %+v, no error; want ps's failure", item)
		}
	})
}

func TestDockerUpdateRule(t *testing.T) {
	r := catalogRule(t, "docker-update-download")
	if r.Risk != engine.RiskCaution || r.PreAction != engine.PreActionDockerUpdateRecheck || len(r.NativeCommand) > 0 || !r.Beta {
		t.Errorf("risk %q, pre_action %q, command %q, beta %v; want a beta caution Trash rule with the recheck", r.Risk, r.PreAction, r.NativeCommand, r.Beta)
	}
	if _, ok := DefaultQueries(engine.Host{})[r.ToolQuery]; !ok {
		t.Errorf("query %q is not registered", r.ToolQuery)
	}
}
