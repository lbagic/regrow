package main

import (
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/aerials"
	"github.com/lbagic/regrow/internal/engine"
)

func TestAerialsStatusText(t *testing.T) {
	cleared := time.Date(2026, 10, 29, 12, 0, 0, 0, time.Local)
	picks := []aerials.Pick{{Table: "Z_2ACTIVEFORUSERS"}, {Table: "Z_2ACTIVEFORUSERS"}}
	for _, c := range []struct {
		name string
		st   aerials.Status
		want [][]string
	}{
		{"on", aerials.Status{State: aerials.StateOn, Counts: aerials.Count(picks)},
			[][]string{{"Aerial downloads: on", "2 categories"}, {"`regrow aerials off`", "password once"}}},
		{"off with a record", aerials.Status{State: aerials.StateOff, Recorded: &aerials.Record{Cleared: cleared, Picks: picks}},
			[][]string{{"Aerial downloads: off"}, {"`regrow aerials on` puts back", "2026-10-29 12:00", "2 categories"}}},
		{"off, restored", aerials.Status{State: aerials.StateOff, Recorded: &aerials.Record{Cleared: cleared, Picks: picks, Restored: &cleared}},
			[][]string{{"Aerial downloads: off"}, {"System Settings ▸ Wallpaper or Screen Saver turns them on again"}}},
		{"aerial wallpaper", aerials.Status{State: aerials.StateOn, Counts: aerials.Count(picks), SetFor: []string{aerials.SurfaceWallpaper, aerials.SurfaceScreenSaver}},
			[][]string{{"Warning", "aerial is set as your wallpaper", "System Settings ▸ Wallpaper: open x-apple.systempreferences:com.apple.Wallpaper-Settings.extension"},
				{"Warning", "your screen saver", "System Settings ▸ Screen Saver: open x-apple.systempreferences:com.apple.ScreenSaver-Settings.extension"}}},
		{"absent", aerials.Status{State: aerials.StateAbsent}, [][]string{{"Aerial downloads: none", "no aerial database"}}},
		{"unknown", aerials.Status{State: aerials.StateUnknown, Detail: "read the aerial picks: no such table"}, [][]string{{"unknown", "no such table"}}},
		{"unmeasured", aerials.Status{State: aerials.StateUnmeasured, Detail: "the aerial picks were measured on macOS 15 only; on macOS 26.1 regrow changes nothing"},
			[][]string{{"not measured here", "The aerial picks", "macOS 26.1"}}},
	} {
		out := strings.Join(aerialsStatusLines(c.st), "\n")
		for _, want := range c.want {
			if !lineWith(out, want...) {
				t.Errorf("%s: no line with %q in:\n%s", c.name, want, out)
			}
		}
	}
}

func TestAerialsResultText(t *testing.T) {
	picks := []aerials.Pick{{Table: "Z_2ACTIVEFORUSERS"}, {Table: "Z_1PINNEDFORUSERS"}}
	on := aerials.Status{State: aerials.StateOn, Picks: picks, Counts: aerials.Count(picks)}
	off := aerials.Status{State: aerials.StateOff}
	for _, c := range []struct {
		name, verb string
		res        aerials.Result
		want       []string
	}{
		{"off", "off", aerials.Result{Ran: true, Before: on, After: off}, []string{"Aerial downloads: off", "Cleared 1 category, 1 video"}},
		{"second off", "off", aerials.Result{Before: off, After: off}, []string{"already off", "Nothing changed"}},
		{"off, a pick made meanwhile", "off", aerials.Result{Ran: true, Before: on, After: aerials.Status{State: aerials.StateOn, Picks: picks[:1], Counts: aerials.Count(picks[:1])}},
			[]string{"still on", "1 of 2 picks are gone", "picked now: 1 category"}},
		{"off, both cleared and another made meanwhile", "off", aerials.Result{Ran: true, Before: on, After: aerials.Status{State: aerials.StateOn, Picks: []aerials.Pick{{Table: "Z_3ACTIVEFORUSERS"}}}},
			[]string{"still on", "2 of 2 picks are gone"}},
		{"on", "on", aerials.Result{Ran: true, Before: off, After: on, Missing: 1}, []string{"1 recorded picks are no longer"}},
		{"on, nothing recorded", "on", aerials.Result{Before: off, After: off}, []string{"Nothing to put back", "Nothing changed"}},
	} {
		out := strings.Join(aerialsResultLines(c.verb, c.res), "\n")
		if !lineWith(out, c.want...) {
			t.Errorf("%s: no line with %q in:\n%s", c.name, c.want, out)
		}
	}
}

// off with a record it cannot read changes nothing, so it promises no
// password prompt.
func TestAerialsBeforeOffWithABrokenRecord(t *testing.T) {
	picks := []aerials.Pick{{Table: "Z_2ACTIVEFORUSERS"}}
	st := aerials.Status{State: aerials.StateOn, Picks: picks, Counts: aerials.Count(picks), RecordError: "aerials.json: unexpected end of JSON input"}
	if lines := aerialsBeforeLines("off", st, "aerials.json"); len(lines) != 0 {
		t.Errorf("before off with a broken record: %q, want nothing", lines)
	}
}

// off and on that ran no root step because they were refused or
// stopped print nothing of their own: the error says why.
func TestAerialsResultTextWhenRefused(t *testing.T) {
	picks := []aerials.Pick{{Table: "Z_2ACTIVEFORUSERS"}}
	broken := aerials.Status{State: aerials.StateOn, Picks: picks, Counts: aerials.Count(picks), RecordError: "aerials.json: unexpected end of JSON input"}
	waiting := aerials.Status{State: aerials.StateOff, Recorded: &aerials.Record{Cleared: time.Now(), Picks: picks}}
	for name, c := range map[string]struct {
		verb string
		st   aerials.Status
	}{
		"off with a broken record": {"off", broken},
		"off refused":              {"off", aerials.Status{State: aerials.StateOn, Picks: picks, Counts: aerials.Count(picks)}},
		"on stopped":               {"on", waiting},
		"on with a broken record":  {"on", aerials.Status{State: aerials.StateOff, RecordError: "aerials.json: unexpected end of JSON input"}},
	} {
		if lines := aerialsResultLines(c.verb, aerials.Result{Before: c.st, After: c.st}); len(lines) != 0 {
			t.Errorf("%s: %q, want nothing", name, lines)
		}
	}
}

func TestAerialsArgs(t *testing.T) {
	cmd, opts, ids, _, err := parseArgs([]string{"aerials", "off", "--json"})
	if err != nil || cmd != "aerials" || !opts.asJSON || len(ids) != 1 || ids[0] != "off" {
		t.Errorf("parseArgs = %q %+v %q %v", cmd, opts, ids, err)
	}
	if err := runAerials(engine.Host{OS: "darwin", Version: "15.7.7", Home: t.TempDir()}, []string{"of"}, false); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Errorf("a typo'd verb: %v", err)
	}
}

// The terminal's off and on ask for the password with `sudo -v` before
// they read the picks; the step's `sudo -n` then asks for none.
func TestAerialsCLIAsksFirst(t *testing.T) {
	a := cliAerials(engine.Host{OS: "darwin", Version: "15.7.7", Home: t.TempDir()}, "aerials.json", false)
	if got := strings.Join(a.Root("true"), " "); got != "sudo -n /bin/sh -c true" {
		t.Errorf("root step = %q, want sudo -n", got)
	}
	if a.Authorize == nil || a.Run == nil {
		t.Error("the CLI asks for no password before it reads the picks")
	}
}
