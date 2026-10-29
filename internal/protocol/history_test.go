package protocol

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/headroom"
	"github.com/lbagic/regrow/internal/oplog"
)

func TestHistoryAnswersAtOnce(t *testing.T) {
	m := newMachine(t)
	srv := m.server()
	srv.Catalog = []engine.Rule{{ID: "fixture-trash", Title: "Fixture to the Trash"}, {ID: "fixture-other", Title: "Not in any run"}}
	srv.History = func() (History, error) {
		return History{
			Runs: []oplog.Summary{{
				ID: "r1", Start: tickAt, Done: 2, Restorable: 1, FreesNow: 7, AfterTrash: 3,
				Rules: map[string]int64{"fixture-trash": 3, "fixture-native": 7},
			}},
			Samples: []headroom.Sample{{At: tickAt.Add(-time.Hour), Total: 100, Free: 9}, {At: tickAt, Total: 100, Free: 7}},
		}, nil
	}
	p := start(t, srv)
	p.expect("hello")
	p.send(`{"type":"history","id":"h1"}`)
	got := p.expect("history/h1", "done/h1")[0]
	for _, want := range []string{
		`"event":"history","re":"h1","runs":[{"id":"r1","start":"2026-10-04T12:00:00Z","done":2,"failed":0,"undone":0,"restorable":1,"prune":false`,
		`"frees_now":7,"after_trash":3,"staged":0,"exported":0,"rules":{"fixture-native":7,"fixture-trash":3}`,
		`"samples":[{"at":"2026-10-04T11:00:00Z","total":100,"free":9,`,
		`"titles":{"fixture-trash":"Fixture to the Trash"}`,
	} {
		if !strings.Contains(got.raw, want) {
			t.Errorf("history = %s, missing %s", got.raw, want)
		}
	}
	if strings.Contains(got.raw, "samples_unreadable") {
		t.Errorf("history = %s, says its samples are unreadable", got.raw)
	}
}

func TestHistoryWithNothingYetIsEmptyLists(t *testing.T) {
	m := newMachine(t)
	srv := m.server()
	srv.History = func() (History, error) { return History{SamplesUnreadable: true}, nil }
	p := start(t, srv)
	p.expect("hello")
	p.send(`{"type":"history","id":"h"}`)
	got := p.expect("history/h", "done/h")[0]
	if !strings.Contains(got.raw, `"runs":[],"samples":[],"samples_unreadable":true,"titles":{}`) {
		t.Errorf("history = %s, want empty lists and the unreadable samples said", got.raw)
	}
}

func TestHistoryFailures(t *testing.T) {
	m := newMachine(t)
	srv := m.server()
	srv.History = func() (History, error) { return History{}, errors.New("oplog.jsonl line 7: invalid character") }
	p := start(t, srv)
	p.expect("hello")
	p.send(`{"type":"history","id":"h"}`)
	if e := p.expectError("h", CodeHistoryFailed); !strings.Contains(e.Message, "line 7") {
		t.Fatalf("error = %s, want the oplog's reason", e.raw)
	}

	none := start(t, newMachine(t).server())
	none.expect("hello")
	none.send(`{"type":"history","id":"h"}`)
	none.expectError("h", CodeHistoryFailed)
}

func TestReadHistory(t *testing.T) {
	dir := t.TempDir()
	logPath, samplesPath := filepath.Join(dir, "oplog.jsonl"), filepath.Join(dir, headroom.FileName)
	log, err := oplog.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []oplog.Entry{
		{Time: tickAt, Run: "r1", Seq: 1, Event: oplog.EventStart, RuleID: "npm", Kind: "native", Bytes: 7},
		{Time: tickAt, Run: "r1", Seq: 1, Event: oplog.EventDone, RuleID: "npm"},
	} {
		if err := log.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	_ = log.Close()

	h, err := ReadHistory(logPath, samplesPath)
	if err != nil || len(h.Runs) != 1 || h.Runs[0].FreesNow != 7 || len(h.Samples) != 0 || h.SamplesUnreadable {
		t.Fatalf("no headroom history yet: %+v, %v; want one run of 7 bytes, no samples, nothing unreadable", h, err)
	}
	if err := headroom.Append(samplesPath, headroom.Sample{At: tickAt, Total: 100, Free: 7}); err != nil {
		t.Fatal(err)
	}
	if h, _ := ReadHistory(logPath, samplesPath); len(h.Samples) != 1 || h.Samples[0].Free != 7 {
		t.Errorf("samples = %+v, want the one appended", h.Samples)
	}
	if err := os.Remove(samplesPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(samplesPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if h, err := ReadHistory(logPath, samplesPath); err != nil || !h.SamplesUnreadable || h.Samples != nil {
		t.Errorf("an unreadable headroom history: %+v, %v; want the runs, no samples, and it said", h, err)
	}
	if err := os.WriteFile(logPath, []byte("{\"run\":\"r1\"}\nnot json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadHistory(logPath, samplesPath); err == nil {
		t.Error("a journal with a bad line must fail the history")
	}
}
