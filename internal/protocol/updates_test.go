package protocol

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/selfupdate"
)

func TestUpdatesSaysWhatTheInstallLinesRegrowDoes(t *testing.T) {
	m := newMachine(t)
	srv := m.server()
	checked := time.Date(2026, 10, 29, 9, 0, 0, 0, time.FixedZone("CET", 3600))
	answers := []selfupdate.Install{
		{Path: "/Users/dev/.local/bin/regrow", Version: "0.2.0", CheckedAt: checked},
		{Path: "/Users/dev/.local/bin/regrow", Version: "0.0.0-dev", Why: "it is a development build (0.0.0-dev)", Off: true},
		{},
	}
	srv.Updates = func(context.Context) (selfupdate.Install, error) {
		in := answers[0]
		answers = answers[1:]
		return in, nil
	}
	p := start(t, srv)
	p.expect("hello")
	for i, want := range []string{
		`"cli":{"path":"/Users/dev/.local/bin/regrow","version":"0.2.0","off":false,"checked_at":"2026-10-29T08:00:00Z"}`,
		`"cli":{"path":"/Users/dev/.local/bin/regrow","version":"0.0.0-dev","why":"it is a development build (0.0.0-dev)","off":true}`,
		`"cli":{"off":false}`,
	} {
		p.send(`{"type":"updates","id":"u"}`)
		if got := p.expect("updates/u", "done/u")[0]; !strings.Contains(got.raw, want) {
			t.Errorf("answer %d = %s, want %s", i, got.raw, want)
		}
	}

	srv2 := newMachine(t).server()
	srv2.Updates = func(context.Context) (selfupdate.Install, error) { return selfupdate.Install{}, errors.New("no home") }
	q := start(t, srv2)
	q.expect("hello")
	q.send(`{"type":"updates","id":"u"}`)
	q.expectError("u", CodeUpdatesFailed)

	none := start(t, newMachine(t).server())
	none.expect("hello")
	none.send(`{"type":"updates","id":"u"}`)
	none.expectError("u", CodeUpdatesFailed)
}

// A slow `version` holds up nothing else the engine is asked.
func TestUpdatesAnswersOffTheReadLoop(t *testing.T) {
	srv := newMachine(t).server()
	release := make(chan struct{})
	srv.Updates = func(context.Context) (selfupdate.Install, error) {
		<-release
		return selfupdate.Install{}, nil
	}
	p := start(t, srv)
	p.expect("hello")
	p.send(`{"type":"updates","id":"u"}`)
	p.send(`{"type":"rules","id":"r"}`)
	p.expectError("r", CodeRulesFailed)
	close(release)
	p.expect("updates/u", "done/u")
}

// An updates still being read when stdin ends is answered before the
// engine exits.
func TestUpdatesReadAtShutdownIsAnswered(t *testing.T) {
	srv := newMachine(t).server()
	srv.Updates = func(context.Context) (selfupdate.Install, error) {
		time.Sleep(50 * time.Millisecond)
		return selfupdate.Install{}, nil
	}
	p := start(t, srv)
	p.expect("hello")
	p.send(`{"type":"updates","id":"u"}`)
	rest, err := p.closeAndWait()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range rest {
		got = append(got, e.Event+"/"+e.Re)
	}
	if strings.Join(got, " ") != "updates/u done/u" {
		t.Errorf("after stdin ended: %v, want [updates/u done/u]", got)
	}
}
