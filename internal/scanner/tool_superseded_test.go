package scanner

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

var buildNow = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

// plantBuild writes a build folder whose every entry was last changed
// age before buildNow.
func plantBuild(t *testing.T, dir string, age time.Duration) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "bin", "tool"), 4096)
	at := buildNow.Add(-age)
	err := filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, at, at)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func link(t *testing.T, target, at string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, at); err != nil {
		t.Fatal(err)
	}
}

func newTestBuilds(store buildStore) *supersededBuilds {
	return &supersededBuilds{
		w: testWalker(), store: store, quiet: buildQuiet,
		procs:  func(context.Context) (processes, error) { return processes{}, nil },
		mapped: func(context.Context) ([]string, error) { return nil, nil },
		now:    func() time.Time { return buildNow },
	}
}

func labels(items []engine.Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Label)
	}
	return out
}

func TestSupersededCodexReleases(t *testing.T) {
	const (
		old    = "0.160.1-aarch64-apple-darwin"
		older  = "0.161.0-aarch64-apple-darwin"
		live   = "0.162.1-aarch64-apple-darwin"
		newer  = "0.163.0-aarch64-apple-darwin"
		odd    = "nightly-aarch64-apple-darwin"
		recent = "0.159.0-aarch64-apple-darwin"
	)
	setup := func(t *testing.T) (string, buildStore) {
		home := t.TempDir()
		store := codexReleases(home)
		for _, v := range []string{old, older, live} {
			plantBuild(t, filepath.Join(store.dir, v), 72*time.Hour)
		}
		writeFile(t, filepath.Join(store.dir, ".DS_Store"), 10)
		link(t, filepath.Join(store.dir, live), store.pointers[0])
		return home, store
	}

	t.Run("every build but the live one", func(t *testing.T) {
		_, store := setup(t)
		items, kept, err := newTestBuilds(store).scan(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"Codex app server " + old, "Codex app server " + older}
		if got := labels(items); !slices.Equal(got, want) || len(kept) != 0 {
			t.Errorf("items %q, kept %v; want %q", got, kept, want)
		}
		for _, it := range items {
			if it.Bytes <= 0 || filepath.Dir(it.Path) != store.dir {
				t.Errorf("item %+v: want its size and its folder in %s", it, store.dir)
			}
		}
	})

	t.Run("what keeps a build", func(t *testing.T) {
		_, store := setup(t)
		plantBuild(t, filepath.Join(store.dir, newer), 72*time.Hour)
		plantBuild(t, filepath.Join(store.dir, odd), 72*time.Hour)
		plantBuild(t, filepath.Join(store.dir, recent), time.Hour)
		q := newTestBuilds(store)
		q.procs = func(context.Context) (processes, error) {
			return processes{args: []string{filepath.Join(store.dir, older, "bin", "tool") + " app-server"}}, nil
		}
		q.mapped = func(context.Context) ([]string, error) {
			return []string{filepath.Join(store.dir, old, "bin", "tool")}, nil
		}
		items, kept, err := q.scan(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 0 {
			t.Errorf("items %q, want none", labels(items))
		}
		whys := map[string]string{}
		for _, k := range kept {
			whys[filepath.Base(k.path)] = k.why
		}
		for name, want := range map[string]string{
			old:    "a running process uses",
			older:  "a running process works in it or names it",
			newer:  "newer than the build in use",
			odd:    "newer than the build in use",
			recent: "changed in the last day",
		} {
			if !strings.Contains(whys[name], want) {
				t.Errorf("%s kept for %q, want %q", name, whys[name], want)
			}
		}
	})

	for _, tc := range []struct {
		name  string
		point func(t *testing.T, store buildStore)
		why   string
	}{
		{"no link", func(*testing.T, buildStore) {}, "no link names the build in use"},
		{"a link that leads nowhere", func(t *testing.T, store buildStore) {
			link(t, filepath.Join(store.dir, "0.170.0-aarch64-apple-darwin"), store.pointers[0])
		}, "leads nowhere"},
		{"a link outside the folder", func(t *testing.T, store buildStore) {
			other := filepath.Join(t.TempDir(), "elsewhere")
			plantBuild(t, other, time.Hour)
			link(t, other, store.pointers[0])
		}, "leads outside"},
		{"a link to the folder itself", func(t *testing.T, store buildStore) {
			link(t, store.dir, store.pointers[0])
		}, "leads to no build"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			store := codexReleases(home)
			for _, v := range []string{old, live} {
				plantBuild(t, filepath.Join(store.dir, v), 72*time.Hour)
			}
			tc.point(t, store)
			items, kept, err := newTestBuilds(store).scan(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != 0 || len(kept) != 1 || !strings.Contains(kept[0].why, tc.why) {
				t.Errorf("items %q, kept %v; want nothing offered because %q", labels(items), kept, tc.why)
			}
		})
	}

	t.Run("processes that cannot be listed", func(t *testing.T) {
		_, store := setup(t)
		q := newTestBuilds(store)
		q.mapped = func(context.Context) ([]string, error) { return nil, errors.New("lsof failed") }
		if items, _, err := q.scan(context.Background()); err == nil {
			t.Errorf("items %q, no error; want lsof's failure", labels(items))
		}
	})

	t.Run("no folder", func(t *testing.T) {
		store := codexReleases(t.TempDir())
		if items, kept, err := newTestBuilds(store).scan(context.Background()); err != nil || len(items)+len(kept) != 0 {
			t.Errorf("items %v, kept %v, err %v; want nothing", items, kept, err)
		}
	})

	t.Run("the recheck", func(t *testing.T) {
		_, store := setup(t)
		q := newTestBuilds(store)
		if err := q.recheck(context.Background(), filepath.Join(store.dir, old)); err != nil {
			t.Errorf("recheck of an offered build: %v", err)
		}
		if err := q.recheck(context.Background(), filepath.Join(store.dir, live)); err == nil {
			t.Error("recheck of the live build passed")
		}
		q.procs = func(context.Context) (processes, error) {
			return processes{cwds: []string{filepath.Join(store.dir, old)}}, nil
		}
		if err := q.recheck(context.Background(), filepath.Join(store.dir, old)); err == nil || !strings.Contains(err.Error(), "kept now") {
			t.Errorf("recheck of a build a process works in: %v, want kept now", err)
		}
		if err := os.Remove(store.pointers[0]); err != nil {
			t.Fatal(err)
		}
		if err := q.recheck(context.Background(), filepath.Join(store.dir, old)); err == nil {
			t.Error("recheck with no link passed")
		}
	})
}

// cursor-agent's links point at the binary inside the build, and either
// may be missing.
func TestSupersededCursorAgentVersions(t *testing.T) {
	home := t.TempDir()
	store := cursorAgentVersions(home)
	const old, live = "2026.07.23-e383d2b", "2026.08.11-e8db854"
	for _, v := range []string{old, live} {
		plantBuild(t, filepath.Join(store.dir, v), 72*time.Hour)
	}
	link(t, filepath.Join(store.dir, live, "bin", "tool"), store.pointers[0])
	items, kept, err := newTestBuilds(store).scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := labels(items); !slices.Equal(got, []string{"cursor-agent " + old}) || len(kept) != 0 {
		t.Errorf("items %q, kept %v; want the old build alone", got, kept)
	}

	// The second link pointing at the old build makes it live too.
	link(t, filepath.Join(store.dir, old, "bin", "tool"), store.pointers[1])
	if items, _, err := newTestBuilds(store).scan(context.Background()); err != nil || len(items) != 0 {
		t.Errorf("items %q, err %v; want nothing with both builds linked", labels(items), err)
	}
}

func TestSupersededRules(t *testing.T) {
	for id, query := range map[string]string{"codex-releases": "codex-releases", "cursor-agent-versions": "cursor-agent-versions"} {
		r := catalogRule(t, id)
		if r.ToolQuery != query || r.PreAction != engine.PreActionSupersededBuildRecheck || len(r.NativeCommand) > 0 || !r.Beta || r.Risk != engine.RiskSafe {
			t.Errorf("%s: query %q, pre_action %q, command %q, beta %v, risk %q; want a beta safe Trash rule with the recheck", id, r.ToolQuery, r.PreAction, r.NativeCommand, r.Beta, r.Risk)
		}
		if _, ok := DefaultQueries(engine.Host{})[query]; !ok {
			t.Errorf("%s: query %q is not registered", id, query)
		}
	}
}
