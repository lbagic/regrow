package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/lbagic/regrow/internal/aerials"
	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/oplog"
)

// runAerials is `regrow aerials [status|off|on]`: status reads the
// picks macOS downloads aerial videos for; off clears them as root and
// stops the downloader; on puts back what off cleared.
func runAerials(host engine.Host, args []string, asJSON bool) error {
	verb := "status"
	if len(args) > 0 {
		verb = args[0]
	}
	if len(args) > 1 || (verb != "status" && verb != "off" && verb != "on") {
		return fmt.Errorf("usage: regrow aerials [status|off|on] [--json]")
	}
	recordPath, err := aerials.RecordPath()
	if err != nil {
		return err
	}
	a := cliAerials(host, recordPath, asJSON)
	// A stop is journaled; a second one ends regrow at once.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	context.AfterFunc(ctx, stop)
	if verb == "status" {
		st := a.Status(ctx)
		if asJSON {
			return emitJSON(st)
		}
		for _, line := range aerialsStatusLines(st) {
			fmt.Println(line)
		}
		return nil
	}

	logPath, err := oplog.DefaultPath()
	if err != nil {
		return err
	}
	log, err := oplog.Open(logPath)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	a.Log = log
	if !asJSON {
		// sudo prompts on the terminal while it runs; the plan comes
		// first, once the lock is held, so a run refused as busy prints
		// none.
		authorize := a.Authorize
		a.Authorize = func(ctx context.Context) error {
			for _, line := range aerialsBeforeLines(verb, a.Status(ctx), recordPath) {
				fmt.Println(line)
			}
			return authorize(ctx)
		}
	}

	var res aerials.Result
	if verb == "off" {
		res, err = a.Off(ctx)
	} else {
		res, err = a.On(ctx)
	}
	if asJSON {
		if jerr := emitJSON(res); jerr != nil {
			return jerr
		}
	} else {
		for _, line := range aerialsResultLines(verb, res) {
			fmt.Println(line)
		}
	}
	if errors.Is(err, aerials.ErrNotApplicable) {
		return fmt.Errorf("nothing changed: %s", notApplicableText(res.Before))
	}
	return err
}

// cliAerials runs the root step through sudo: `sudo -v` asks for the
// password before the picks are read, and the step's `sudo -n` then asks
// for none, so it fails rather than prompt. With --json, sudo writes to
// stderr, leaving stdout to the JSON.
func cliAerials(host engine.Host, recordPath string, asJSON bool) *aerials.Aerials {
	out := os.Stdout
	if asJSON {
		out = os.Stderr
	}
	run := func(ctx context.Context, argv []string) error { return aerials.RunRoot(ctx, argv, out, os.Stderr) }
	return &aerials.Aerials{
		Version:    host.Version,
		RecordPath: recordPath,
		Store:      aerials.WallpaperStorePath(host.Home),
		Authorize:  func(ctx context.Context) error { return run(ctx, []string{"sudo", "-v"}) },
		Root:       func(script string) []string { return []string{"sudo", "-n", "/bin/sh", "-c", script} },
		Run:        run,
	}
}

func notApplicableText(st aerials.Status) string {
	switch st.State {
	case aerials.StateAbsent:
		return "this Mac has no aerial database, so nothing is picked"
	case aerials.StateUnknown:
		return "the aerial picks could not be read: " + st.Detail
	}
	return st.Detail
}

// aerialsStatusLines says where the downloads stand and the one
// command that changes it.
func aerialsStatusLines(st aerials.Status) []string {
	var lines []string
	switch st.State {
	case aerials.StateOn:
		lines = append(lines,
			"Aerial downloads: on. Picked: "+st.Counts.Text()+".",
			"  macOS downloads at least 4 videos for each picked category, about 0.5 GB each, and more every day or two.",
			"  `regrow aerials off` clears the picks and stops the downloader; it asks for your password once.")
	case aerials.StateOff:
		lines = append(lines, "Aerial downloads: off. Nothing is picked, so macOS downloads no aerial videos.")
	case aerials.StateAbsent:
		return []string{"Aerial downloads: none. This Mac has no aerial database, so nothing is picked."}
	case aerials.StateUnknown:
		return []string{"Aerial downloads: unknown. The picks could not be read: " + st.Detail}
	case aerials.StateUnmeasured:
		return []string{"Aerial downloads: not measured here. " + capitalize(st.Detail) + "."}
	}
	if st.RecordError != "" {
		lines = append(lines, "  The record of the last `regrow aerials off` could not be read: "+st.RecordError)
	}
	lines = append(lines, aerialSetLines(st)...)
	if rec := st.Recorded; rec.Waiting() {
		lines = append(lines, fmt.Sprintf("  `regrow aerials on` puts back what off cleared on %s: %s.",
			rec.Cleared.Local().Format("2006-01-02 15:04"), aerials.Count(rec.Picks).Text()))
	} else if st.State == aerials.StateOff {
		lines = append(lines, "  Picking an aerial in System Settings ▸ Wallpaper or Screen Saver turns them on again.")
	}
	return lines
}

// aerialsBeforeLines is printed before off or on asks for the password.
func aerialsBeforeLines(verb string, st aerials.Status, recordPath string) []string {
	switch {
	case verb == "off" && st.State == aerials.StateOn && st.RecordError == "":
		return append(append([]string{
			"Clearing the aerial picks (" + st.Counts.Text() + ") and stopping the downloader.",
			"  Recorded first in " + recordPath + ", so `regrow aerials on` can put them back.",
		}, aerialSetLines(st)...), "  sudo asks for your password:")
	case verb == "on" && (st.State == aerials.StateOn || st.State == aerials.StateOff) && st.Recorded.Waiting():
		return []string{
			"Putting back the aerial picks off cleared (" + aerials.Count(st.Recorded.Picks).Text() + ").",
			"  sudo asks for your password:",
		}
	}
	return nil
}

// aerialSetLines warns about a surface that still shows an aerial:
// once the picks are clear it has no videos left to play, and macOS may
// pick its aerial again.
func aerialSetLines(st aerials.Status) []string {
	if st.SetForError != "" {
		return []string{"  Whether an aerial is set as your wallpaper or screen saver could not be read: " + st.SetForError}
	}
	var lines []string
	for _, surface := range st.SetFor {
		pane := "Wallpaper"
		if surface == aerials.SurfaceScreenSaver {
			pane = "Screen Saver"
		}
		lines = append(lines, "  Warning: an aerial is set as your "+surface+". With the picks cleared it has no videos to play, and macOS may pick it again. Choose another in System Settings ▸ "+pane+": open "+aerials.SettingsURL(surface))
	}
	return lines
}

func aerialsResultLines(verb string, res aerials.Result) []string {
	if !res.Ran {
		// Anything else that ran no root step was refused or stopped,
		// and the error says why.
		switch st := res.Before; {
		case verb == "off" && st.State == aerials.StateOff:
			return []string{"Aerial downloads are already off: nothing is picked. Nothing changed."}
		case verb == "on" && (st.State == aerials.StateOn || st.State == aerials.StateOff) && st.RecordError == "" && !st.Recorded.Waiting():
			return []string{"Nothing to put back: no `regrow aerials off` has cleared picks that are still waiting. Nothing changed.",
				"  To pick an aerial, use System Settings ▸ Wallpaper or Screen Saver."}
		}
		return nil
	}
	after := res.After
	var lines []string
	switch verb {
	case "off":
		switch after.State {
		case aerials.StateOn:
			lines = append(lines, fmt.Sprintf("Aerial downloads: still on. %d of %d picks are gone; picked now: %s.",
				res.Gone(), len(res.Before.Picks), after.Counts.Text()),
				"  Run `regrow aerials off` again.")
		case aerials.StateOff:
			lines = append(lines, "Aerial downloads: off. Cleared "+res.Before.Counts.Text()+".",
				"  `regrow aerials on` puts them back. Picking an aerial in System Settings picks it again.")
		}
	case "on":
		if after.State == aerials.StateOn {
			lines = append(lines, "Aerial downloads: on. Picked: "+after.Counts.Text()+".",
				"  macOS downloads for them the next time it starts the downloader: at login, or when a wallpaper or screen saver setting changes.")
		}
		if res.Missing > 0 {
			lines = append(lines, fmt.Sprintf("  %d recorded picks are no longer in macOS's aerial catalogue and were not put back.", res.Missing))
		}
	}
	if after.State == aerials.StateUnknown {
		lines = append(lines, "The picks could not be read afterwards: "+after.Detail)
	}
	return lines
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
