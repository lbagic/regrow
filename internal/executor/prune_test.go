package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/oplog"
	"github.com/lbagic/regrow/internal/trash"
)

const goCacheReadme = "This directory holds cached build artifacts from the Go build system.\n"

// pruneFixture is a three-entry Go build cache in a temp dir.
func pruneFixture(t *testing.T) (cache string, entrySize int64) {
	t.Helper()
	cache = filepath.Join(t.TempDir(), "go-build")
	files := map[string]string{
		"README":  goCacheReadme,
		"00/a1-a": strings.Repeat("x", 4096),
		"00/b2-d": strings.Repeat("x", 4096),
		"ff/c3-a": strings.Repeat("x", 4096),
	}
	for rel, body := range files {
		if err := writeFile(filepath.Join(cache, rel), body); err != nil {
			t.Fatal(err)
		}
	}
	usage, err := engine.MeasureCache(context.Background(), cache)
	if err != nil || usage.Files != 3 || usage.Bytes <= 0 {
		t.Fatalf("fixture cache measures %+v, err %v", usage, err)
	}
	return cache, usage.Bytes / 3
}

func pruneAction(cache string) engine.Plan {
	return engine.Plan{Actions: []engine.Action{{
		RuleID: engine.RuleGoBuildCache, Kind: engine.ActionPrune, Path: cache,
		Command: []string{"fixture-prune", cache},
		// Deliberately wrong: the journal and the result must carry
		// what was measured, never this estimate.
		Bytes: 999_999,
	}}}
}

func TestExecutePruneJournalsWhatItDeleted(t *testing.T) {
	cache, size := pruneFixture(t)
	log := &memLog{}
	e := &Executor{Log: log, Now: fixedNow, RunNative: func(_ context.Context, argv []string) error {
		if argv[0] != "fixture-prune" {
			t.Fatalf("unexpected command %q", argv)
		}
		return errors.Join(os.Remove(filepath.Join(cache, "00/a1-a")), os.Remove(filepath.Join(cache, "ff/c3-a")))
	}}

	res, err := e.Execute(context.Background(), pruneAction(cache))
	if err != nil {
		t.Fatal(err)
	}
	if len(log.entries) != 2 {
		t.Fatalf("want a start and a done line, got %+v", log.entries)
	}
	start, done := log.entries[0], log.entries[1]
	if start.Event != oplog.EventStart || start.Kind != "prune" || start.Path != cache || start.Bytes != 999_999 || start.Pruned != nil {
		t.Errorf("start line = %+v", start)
	}
	want := oplog.Pruned{Files: 2, Bytes: 2 * size}
	if done.Event != oplog.EventDone || done.Pruned == nil || *done.Pruned != want {
		t.Errorf("done line = %+v (pruned %+v), want pruned %+v", done, done.Pruned, want)
	}
	if res.Done != 1 || res.Failed != 0 || res.Bytes != want.Bytes || res.Pruned != want {
		t.Errorf("result = %+v, want the measured %+v", res, want)
	}
}

func TestExecutePruneJournalsAPartialDeleteOnFailure(t *testing.T) {
	cache, size := pruneFixture(t)
	log := &memLog{}
	e := &Executor{Log: log, Now: fixedNow, RunNative: func(context.Context, []string) error {
		if err := os.Remove(filepath.Join(cache, "00/b2-d")); err != nil {
			t.Fatal(err)
		}
		return errors.New("exit status 1")
	}}

	res, err := e.Execute(context.Background(), pruneAction(cache))
	if err != nil {
		t.Fatal(err)
	}
	fail := log.entries[len(log.entries)-1]
	want := oplog.Pruned{Files: 1, Bytes: size}
	if fail.Event != oplog.EventFail || fail.Error != "exit status 1" || fail.Pruned == nil || *fail.Pruned != want {
		t.Errorf("fail line = %+v (pruned %+v), want pruned %+v", fail, fail.Pruned, want)
	}
	if res.Done != 0 || res.Failed != 1 || res.Bytes != 0 || res.Pruned != want || res.FailedPruned != want {
		t.Errorf("result = %+v, want one failure that still reports %+v", res, want)
	}
}

func TestExecutePruneRefusesATargetThatIsNotAGoCache(t *testing.T) {
	cache, _ := pruneFixture(t)
	if err := os.WriteFile(filepath.Join(cache, "README"), []byte("notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := &memLog{}
	e := &Executor{Log: log, Now: fixedNow, RunNative: func(context.Context, []string) error {
		t.Fatal("the command must not run against an unverified directory")
		return nil
	}}

	res, err := e.Execute(context.Background(), pruneAction(cache))
	if err != nil {
		t.Fatal(err)
	}
	fail := log.entries[len(log.entries)-1]
	if fail.Event != oplog.EventFail || !strings.Contains(fail.Error, "not a Go build cache") || fail.Pruned != nil {
		t.Errorf("fail line = %+v", fail)
	}
	if res.Failed != 1 || res.Pruned != (oplog.Pruned{}) {
		t.Errorf("result = %+v", res)
	}
}

// measuredPlan is a whole-rule native command that deletes directly,
// counted by the "videos" measure, after the hold recheck.
func measuredPlan() engine.Plan {
	return engine.Plan{Actions: []engine.Action{{
		RuleID: "aerial-wallpapers", Kind: engine.ActionNative, Command: []string{"sudo", "fixture-delete"},
		PreAction: engine.PreActionHoldRecheck, Measure: "videos", Sudo: true, Bytes: 999_999,
	}}}
}

// A native command that deletes directly is counted as a prune is:
// the measure runs before and after, after the pre-action, and the
// journal and the result carry what it deleted, failed or not.
func TestExecuteMeasuredJournalsWhatItDeleted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cmdErr error
	}{{"done", nil}, {"failed midway", errors.New("exit status 1")}} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			left := engine.CacheUsage{Files: 3, Bytes: 3 << 20}
			log := &memLog{}
			e := &Executor{Log: log, Now: fixedNow,
				PreActions: map[string]PreAction{engine.PreActionHoldRecheck: func(context.Context, engine.Action) (*trash.Receipt, error) {
					calls = append(calls, "recheck")
					return nil, nil
				}},
				Measures: map[string]Measure{"videos": func(context.Context, engine.Action) (engine.CacheUsage, error) {
					calls = append(calls, "measure")
					return left, nil
				}},
				RunNative: func(context.Context, []string) error {
					calls = append(calls, "run")
					left = engine.CacheUsage{Files: 1, Bytes: 1 << 20}
					return tc.cmdErr
				},
			}
			res, err := e.Execute(context.Background(), measuredPlan())
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(calls, " "); got != "recheck measure run measure" {
				t.Errorf("calls %q, want the recheck, then the command between two measures", got)
			}
			want := oplog.Pruned{Files: 2, Bytes: 2 << 20}
			last := log.entries[len(log.entries)-1]
			if last.Pruned == nil || *last.Pruned != want || res.Pruned != want {
				t.Errorf("journal %+v, result %+v; want %+v deleted", last.Pruned, res.Pruned, want)
			}
			if tc.cmdErr == nil && (res.Done != 1 || res.Bytes != want.Bytes || res.FailedPruned != (oplog.Pruned{})) {
				t.Errorf("result = %+v, want the measured bytes freed", res)
			}
			if tc.cmdErr != nil && (last.Event != oplog.EventFail || res.Failed != 1 || res.Bytes != 0 || res.FailedPruned != want) {
				t.Errorf("last line %+v, result %+v; want one failure that deleted %+v", last, res, want)
			}
		})
	}
}

// What cannot be counted is not deleted: an unregistered measure, or a
// first count that fails, runs nothing.
func TestExecuteMeasuredRefusesUncounted(t *testing.T) {
	for _, tc := range []struct {
		name     string
		measures map[string]Measure
		want     string
	}{
		{"unregistered", nil, `measure "videos" is not registered`},
		{"first count fails", map[string]Measure{"videos": func(context.Context, engine.Action) (engine.CacheUsage, error) {
			return engine.CacheUsage{}, errors.New("permission denied")
		}}, "measure what it deletes before deleting: permission denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := &memLog{}
			e := &Executor{Log: log, Now: fixedNow, Measures: tc.measures,
				PreActions: map[string]PreAction{engine.PreActionHoldRecheck: func(context.Context, engine.Action) (*trash.Receipt, error) { return nil, nil }},
				RunNative: func(context.Context, []string) error {
					t.Fatal("the command must not run uncounted")
					return nil
				},
			}
			res, err := e.Execute(context.Background(), measuredPlan())
			if err != nil {
				t.Fatal(err)
			}
			fail := log.entries[len(log.entries)-1]
			if fail.Event != oplog.EventFail || !strings.Contains(fail.Error, tc.want) || fail.Pruned != nil || res.Failed != 1 {
				t.Errorf("fail line = %+v, result %+v; want a failure saying %q", fail, res, tc.want)
			}
		})
	}
}
