package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/oplog"
	"github.com/lbagic/regrow/internal/trash"
)

type fakeMover struct {
	fail  map[string]error
	moved []string
}

func (m *fakeMover) Move(_ context.Context, path string) (trash.Receipt, error) {
	if err := m.fail[path]; err != nil {
		return trash.Receipt{}, err
	}
	m.moved = append(m.moved, path)
	return trash.Receipt{Original: path, To: "/staging" + path, Method: trash.MethodStaging}, nil
}

type memLog struct {
	entries []oplog.Entry
	fail    bool
}

func (l *memLog) Append(e oplog.Entry) error {
	if l.fail {
		return errors.New("disk full")
	}
	l.entries = append(l.entries, e)
	return nil
}

func fixedNow() time.Time { return time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC) }

func testPlan() engine.Plan {
	return engine.Plan{Actions: []engine.Action{
		{RuleID: "npm-cache", Kind: engine.ActionTrash, Path: "/u/.npm", Bytes: 100, Command: trash.PreviewCommand("/u/.npm")},
		{RuleID: "go-build-cache", Kind: engine.ActionNative, Command: []string{"go", "clean", "-cache"}, Bytes: 200},
	}}
}

func TestExecuteJournalsBeforeActing(t *testing.T) {
	log := &memLog{}
	var order []string
	mover := &fakeMover{}
	e := &Executor{Trash: mover, Log: log, Now: fixedNow,
		RunNative: func(context.Context, []string) error { order = append(order, "native"); return nil }}

	res, err := e.Execute(context.Background(), testPlan())
	if err != nil {
		t.Fatal(err)
	}
	if res.Done != 2 || res.Failed != 0 || res.Bytes != 300 {
		t.Fatalf("result wrong: %+v", res)
	}
	// start(1), done(1), start(2), done(2) — start always precedes its action's outcome line.
	events := []string{}
	for _, en := range log.entries {
		events = append(events, en.Event)
	}
	want := []string{"start", "done", "start", "done"}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("journal order = %v, want %v", events, want)
	}
	if log.entries[1].Receipt == nil || log.entries[1].Receipt.Original != "/u/.npm" {
		t.Fatalf("done trash entry must carry the receipt: %+v", log.entries[1])
	}
	if log.entries[3].Receipt != nil {
		t.Fatal("native done entry must not carry a receipt")
	}
}

func TestExecuteRefusesWhenJournalDown(t *testing.T) {
	mover := &fakeMover{}
	e := &Executor{Trash: mover, Log: &memLog{fail: true}, Now: fixedNow}
	_, err := e.Execute(context.Background(), testPlan())
	if err == nil || !strings.Contains(err.Error(), "refusing to act") {
		t.Fatalf("journal failure must block actions, got %v", err)
	}
	if len(mover.moved) != 0 {
		t.Fatal("nothing may move when the journal cannot be written")
	}
}

func TestExecuteContinuesPastFailures(t *testing.T) {
	log := &memLog{}
	mover := &fakeMover{fail: map[string]error{"/u/.npm": errors.New("vanished since scan")}}
	e := &Executor{Trash: mover, Log: log, Now: fixedNow,
		RunNative: func(context.Context, []string) error { return nil }}

	res, err := e.Execute(context.Background(), testPlan())
	if err != nil {
		t.Fatal(err)
	}
	if res.Done != 1 || res.Failed != 1 || res.Bytes != 200 {
		t.Fatalf("one failure must not abort the run: %+v", res)
	}
	if log.entries[1].Event != oplog.EventFail || !strings.Contains(log.entries[1].Error, "vanished") {
		t.Fatalf("failure must be journaled: %+v", log.entries[1])
	}
}

func TestExecuteStopsOnCancel(t *testing.T) {
	log := &memLog{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := &Executor{Trash: &fakeMover{}, Log: log, Now: fixedNow}
	if _, err := e.Execute(ctx, testPlan()); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if len(log.entries) != 0 {
		t.Fatal("cancelled run must not journal or act")
	}
}

func TestUndoRestoresReverseAndJournals(t *testing.T) {
	// Build a real staged move so Restore has something to rename.
	staging := t.TempDir()
	target := t.TempDir() + "/cache"
	if err := writeFile(target+"/a.bin", "x"); err != nil {
		t.Fatal(err)
	}
	m := &trash.Mover{Home: "/nope", StagingDir: staging,
		RunFinder: func(context.Context, string) (string, error) { return "", errors.New("no Finder") }}
	receipt, err := m.Move(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}

	log := &memLog{}
	e := &Executor{Log: log, Now: fixedNow}
	run := oplog.Run{ID: "r1", Entries: []oplog.Entry{
		{Run: "r1", Seq: 1, Event: oplog.EventDone, RuleID: "x", Receipt: &receipt},
		{Run: "r1", Seq: 2, Event: oplog.EventDone, RuleID: "go-build-cache"}, // native
	}}
	res, err := e.Undo(run)
	if err != nil {
		t.Fatal(err)
	}
	if res.Restored != 1 || res.Failed != 0 || res.NativeSkipped != 1 {
		t.Fatalf("undo result wrong: %+v", res)
	}
	if len(log.entries) != 1 || log.entries[0].Event != oplog.EventUndo || log.entries[0].Error != "" {
		t.Fatalf("undo must journal the restore: %+v", log.entries)
	}
	if !exists(target + "/a.bin") {
		t.Fatal("undo did not bring the tree back")
	}
}

func TestUndoJournalsFailedRestore(t *testing.T) {
	log := &memLog{}
	e := &Executor{Log: log, Now: fixedNow}
	run := oplog.Run{ID: "r1", Entries: []oplog.Entry{
		{Run: "r1", Seq: 1, Event: oplog.EventDone, RuleID: "x",
			Receipt: &trash.Receipt{Original: "/u/x", To: "/gone/x", Method: trash.MethodStaging}},
	}}
	res, err := e.Undo(run)
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 1 || res.Restored != 0 {
		t.Fatalf("want failed restore counted: %+v", res)
	}
	if len(log.entries) != 1 || log.entries[0].Error == "" {
		t.Fatalf("failed restore must be journaled with its error: %+v", log.entries)
	}
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
