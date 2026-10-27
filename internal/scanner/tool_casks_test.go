package scanner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lbagic/regrow/internal/engine"
)

// casksJSON is `brew info --cask --json=v2 --installed`, cut to the
// fields read. App names are made up so no real app in /Applications
// can match one.
const casksJSON = `{"formulae":[],"casks":[
 {"token":"gone-plain","installed":"1.0","artifacts":[{"uninstall":[{"quit":"dev.example.gone"}]},{"app":["Regrow Test Gone.app"]},{"zap":[{"trash":["~/Library/x"]}]}]},
 {"token":"gone-bare","installed":"2.0","artifacts":[{"app":["Regrow Test Bare.app"]},{"binary":["/x/bin/bare"]}]},
 {"token":"gone-launchctl","installed":"3.0","artifacts":[{"uninstall":[{"launchctl":"dev.example.helper","quit":"dev.example.l"}]},{"app":["Regrow Test Launchctl.app"]}]},
 {"token":"gone-pkg","installed":"4.0","artifacts":[{"app":["Regrow Test Pkg.app"]},{"pkg":["Pkg.pkg"]}]},
 {"token":"gone-postflight","installed":"5.0","artifacts":[{"app":["Regrow Test Post.app"]},{"uninstall_postflight":null}]},
 {"token":"present-appdir","installed":"6.0","artifacts":[{"app":["Regrow Test Here.app"]}]},
 {"token":"present-home","installed":"7.0","artifacts":[{"app":["Regrow Test Home.app"]}]},
 {"token":"renamed","installed":"8.0","artifacts":[{"app":["regrow test renamed.app",{"target":"Regrow Test Renamed.app"}]}]},
 {"token":"no-app","installed":"9.0","artifacts":[{"binary":["/x/bin/tool"]}]},
 {"token":"appdir-gone","installed":"10.0","artifacts":[{"app":["Regrow Test Elsewhere.app"]}]},
 {"token":"bad-config","installed":"11.0","artifacts":[{"app":["Regrow Test Bad.app"]}]}
]}`

func caskFixture(t *testing.T) (*caskRecords, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	caskroom := filepath.Join(root, "Caskroom")
	apps := filepath.Join(root, "Apps")
	tokens := []string{"gone-plain", "gone-bare", "gone-launchctl", "gone-pkg", "gone-postflight", "present-appdir", "present-home", "renamed", "no-app", "appdir-gone", "bad-config"}
	for _, tok := range tokens {
		writeFile(t, filepath.Join(caskroom, tok, "1.0", "marker"), 2048)
		cfg := `{"default":{"appdir":"` + apps + `"},"env":null,"explicit":null}`
		switch tok {
		case "appdir-gone":
			cfg = `{"default":{"appdir":"/Applications"},"env":null,"explicit":{"appdir":"` + filepath.Join(root, "NotMounted") + `"}}`
		case "bad-config":
			cfg = `{"default":`
		}
		writeText(t, filepath.Join(caskroom, tok, ".metadata", "config.json"), cfg)
	}
	writeFile(t, filepath.Join(apps, "Regrow Test Here.app", "Contents", "Info.plist"), 10)
	writeFile(t, filepath.Join(home, "Applications", "Regrow Test Home.app", "Contents", "Info.plist"), 10)
	c := &caskRecords{
		Home: home,
		Apps: []string{filepath.Join(root, "Applications"), filepath.Join(home, "Applications")},
		w:    testWalker(),
		Brew: func(_ context.Context, args ...string) ([]byte, bool, error) {
			switch strings.Join(args, " ") {
			case "info --cask --json=v2 --installed":
				return []byte(casksJSON), true, nil
			case "--caskroom":
				return []byte(caskroom + "\n"), true, nil
			}
			return nil, false, errors.New("unexpected brew " + strings.Join(args, " "))
		},
	}
	return c, caskroom
}

func caskTokens(items []engine.Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Arg
	}
	slices.Sort(out)
	return out
}

func TestCaskRecords(t *testing.T) {
	c, caskroom := caskFixture(t)
	plain, err := c.Plain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sudo, err := c.NeedsPassword(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := caskTokens(plain), []string{"gone-bare", "gone-plain", "renamed"}; !slices.Equal(got, want) {
		t.Errorf("plain %q, want %q", got, want)
	}
	if got, want := caskTokens(sudo), []string{"gone-launchctl", "gone-pkg", "gone-postflight"}; !slices.Equal(got, want) {
		t.Errorf("needs a password %q, want %q", got, want)
	}
	for _, it := range append(plain, sudo...) {
		if it.Path != filepath.Join(caskroom, it.Arg) || it.Bytes <= 0 || !strings.HasPrefix(it.Label, it.Arg+" ") {
			t.Errorf("item %+v: want its Caskroom folder, size and token", it)
		}
	}
	if i := slices.IndexFunc(plain, func(it engine.Item) bool { return it.Arg == "renamed" }); i < 0 || !strings.Contains(plain[i].Label, "Regrow Test Renamed.app gone") {
		t.Errorf("the renamed cask is not labelled by its target: %+v", plain)
	}

	if err := c.recheck(context.Background(), filepath.Join(caskroom, "gone-plain")); err != nil {
		t.Errorf("recheck of a plain record: %v", err)
	}
	for _, tok := range []string{"gone-launchctl", "present-appdir", "nothing-here"} {
		if err := c.recheck(context.Background(), filepath.Join(caskroom, tok)); err == nil {
			t.Errorf("recheck of %s passed", tok)
		}
	}
	// gone-launchctl is on the -sudo row; the refusal says what that row means.
	err = c.recheck(context.Background(), filepath.Join(caskroom, "gone-launchctl"))
	if err == nil || !strings.Contains(err.Error(), "may ask for a password, or does more than quit the app") {
		t.Errorf("recheck of a -sudo record: %v; want the -sudo row's words", err)
	}
}

// oneCask is a single installed cask brew describes as js, its record's
// appdir <root>/Apps, the apps named in present installed there,
// <root>/Applications and <home>/Applications the app folders, and
// <apps>, <root> and <home> in js standing for those folders.
func oneCask(t *testing.T, js string, present ...string) *caskRecords {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	caskroom := filepath.Join(root, "Caskroom")
	apps := filepath.Join(root, "Apps")
	writeFile(t, filepath.Join(caskroom, "c", "1.0", "marker"), 2048)
	writeText(t, filepath.Join(caskroom, "c", ".metadata", "config.json"), `{"default":{"appdir":"`+apps+`"},"env":{},"explicit":{}}`)
	if err := os.MkdirAll(apps, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, app := range present {
		writeFile(t, filepath.Join(root, app, "Contents", "Info.plist"), 10)
	}
	js = strings.NewReplacer("<apps>", apps, "<root>", root, "<home>", home).Replace(js)
	folders := []string{filepath.Join(root, "Applications"), filepath.Join(home, "Applications")}
	return &caskRecords{Home: home, Apps: folders, w: testWalker(), Brew: func(_ context.Context, args ...string) ([]byte, bool, error) {
		switch strings.Join(args, " ") {
		case "info --cask --json=v2 --installed":
			return []byte(`{"casks":[{"token":"c","installed":"1.0","artifacts":[` + js + `]}]}`), true, nil
		case "--caskroom":
			return []byte(caskroom + "\n"), true, nil
		}
		return nil, false, errors.New("unexpected brew " + strings.Join(args, " "))
	}}
}

// Homebrew 7 prints each relocated artifact's resolved target beside it.
// That is where the app is, and not an artifact kind.
func TestCaskJudge(t *testing.T) {
	const (
		quit   = `{"uninstall":[{"quit":"dev.example.c"}]}`
		app7   = `{"app":["Regrow Test C.app"],"target":"<apps>/Regrow Test C.app"}`
		appOld = `{"app":["Regrow Test C.app"]}`
	)
	cases := []struct {
		name    string
		js      string
		present []string // under <root>
		links   []string // files under <root>, the links brew made
		roDir   bool     // <root>/ro is not the owner's to write
		locked  string   // a folder under <root> nobody may read
		dead    []string // links under <root> to <root>/NotMounted, a disk not mounted
		want    string   // "plain", "sudo" or "" (not listed)
	}{
		{name: "quit only, brew 7", js: quit + `,` + app7 + `,{"zap":[{"trash":["~/Library/x"]}]}`, want: "plain"},
		{name: "quit only, before brew 7", js: quit + `,` + appOld, want: "plain"},
		{name: "the app installed, brew 7", js: quit + `,` + app7, present: []string{"Apps/Regrow Test C.app"}},
		{name: "an app in a subfolder, installed under its own name, before brew 7",
			js: quit + `,{"app":["Regrow Test Folder/Regrow Test C.app"]}`, present: []string{"Apps/Regrow Test C.app"}},
		{name: "an app in a subfolder, installed under its own name, brew 7",
			js: quit + `,{"app":["Regrow Test Folder/Regrow Test C.app"],"target":"<apps>/Regrow Test C.app"}`, present: []string{"Apps/Regrow Test C.app"}},
		{name: "an app in a subfolder, gone", js: quit + `,{"app":["Regrow Test Folder/Regrow Test C.app"]}`, want: "plain"},
		{name: "a target brew resolved outside the app folder, installed",
			js: quit + `,{"app":["Regrow Test C.app",{"target":"<root>/Other/C.app"}],"target":"<root>/Other/C.app"}`, present: []string{"Other/C.app"}},
		{name: "an app moved to ~/Applications", js: quit + `,` + app7, present: []string{"home/Applications/Regrow Test C.app"}},
		{name: "an app moved one folder down in its app folder", js: quit + `,` + app7, present: []string{"Apps/Dev/Regrow Test C.app"}},
		{name: "an app moved one folder down in /Applications", js: quit + `,` + app7, present: []string{"Applications/Utilities/Regrow Test C.app"}},
		{name: "an app moved one folder down in ~/Applications", js: quit + `,` + app7, present: []string{"home/Applications/Dev/Regrow Test C.app"}},
		{name: "an app moved one folder down, its name in another case", js: quit + `,` + app7, present: []string{"Apps/Dev/regrow test c.app"}},
		{name: "an app moved one folder down from a target outside the app folder",
			js: quit + `,{"app":["Regrow Test C.app",{"target":"<root>/Other/C.app"}],"target":"<root>/Other/C.app"}`, present: []string{"Other/Sub/C.app"}},
		{name: "a folder in the app folder regrow cannot read", js: quit + `,` + app7, locked: "Apps/Locked"},
		{name: "a folder link that leads nowhere", js: quit + `,` + app7, dead: []string{"Applications/Dev"}},
		{name: "an app link one folder down that leads nowhere", js: quit + `,` + app7, dead: []string{"Apps/Dev/Regrow Test C.app"}},
		{name: "another app one folder down", js: quit + `,` + app7, present: []string{"Apps/Dev/Regrow Test D.app"}, want: "plain"},
		{name: "a link one folder down under another name that leads nowhere", js: quit + `,` + app7, dead: []string{"Apps/Dev/Other"}, want: "plain"},
		{name: "an app two folders down", js: quit + `,` + app7, present: []string{"Apps/Dev/Old/Regrow Test C.app"}, want: "plain"},
		// brew signals and removes login items even when the app is gone,
		// so one moved where regrow does not look is hit.
		{name: "signal only", js: `{"uninstall":[{"signal":["TERM","dev.example.c"]}]},` + app7, want: "sudo"},
		{name: "quit and a login item", js: `{"uninstall":[{"quit":"dev.example.c","login_item":"C"}]},` + app7, want: "sudo"},
		{name: "launchctl", js: `{"uninstall":[{"launchctl":"dev.example.helper","quit":"dev.example.c"}]},` + app7, want: "sudo"},
		{name: "a binary in a folder brew writes", links: []string{"bin/c"},
			js: quit + `,` + app7 + `,{"binary":["<apps>/Regrow Test C.app/Contents/MacOS/c"],"target":"<root>/bin/c"}`, want: "plain"},
		{name: "a binary in a folder the owner cannot write, brew 7", links: []string{"ro/c"}, roDir: true,
			js: quit + `,` + app7 + `,{"binary":["<apps>/Regrow Test C.app/Contents/MacOS/c",{"target":"<root>/ro/c"}],"target":"<root>/ro/c"}`, want: "sudo"},
		{name: "a binary in a folder the owner cannot write, before brew 7", links: []string{"ro/c"}, roDir: true,
			js: quit + `,` + appOld + `,{"binary":["<apps>/Regrow Test C.app/Contents/MacOS/c",{"target":"<root>/ro/c"}]}`, want: "sudo"},
		{name: "a binary already gone from a folder the owner cannot write", links: []string{"ro/other"}, roDir: true,
			js: quit + `,` + app7 + `,{"binary":["<apps>/Regrow Test C.app/Contents/MacOS/c"],"target":"<root>/ro/c"}`, want: "plain"},
		{name: "a binary under brew's own folder, before brew 7", js: quit + `,` + appOld + `,{"binary":["<apps>/Regrow Test C.app/Contents/MacOS/c"]}`, want: "plain"},
		{name: "a manual page in a folder the owner cannot write", links: []string{"ro/c.1"}, roDir: true,
			js: quit + `,` + app7 + `,{"manpage":["<apps>/Regrow Test C.app/c.1"],"target":"<root>/ro/c.1"}`, want: "sudo"},
		{name: "install steps that may use sudo", js: quit + `,` + app7 +
			`,{"postflight_steps":[{"steps":[{"source":{"path":"{{appdir}}/x"},"target":{"path":"/usr/local/bin/x"},"uninstall":true,"sudo":"if_needed","type":"symlink"}]}]}`, want: "sudo"},
		{name: "install steps that set ownership", js: quit + `,` + app7 + `,{"preflight_steps":[{"steps":[{"paths":["x"],"type":"set_ownership"}]}]}`, want: "sudo"},
		{name: "install steps with no sudo", js: quit + `,` + app7 +
			`,{"postflight_steps":[{"steps":[{"paths":[{"base":"staged_path","path":"c"}],"permissions":"0755","type":"set_permissions"}]}]}`, want: "plain"},
		{name: "install steps regrow cannot read", js: quit + `,` + app7 + `,{"postflight_steps":{"steps":1}}`, want: "sudo"},
		{name: "install steps with no steps list", js: quit + `,` + app7 + `,{"postflight_steps":[{"steps":null}]}`, want: "sudo"},
		{name: "install steps under another key", js: quit + `,` + app7 + `,{"postflight_steps":[{"stages":[{"type":"remove","paths":["x"]}]}]}`, want: "sudo"},
		{name: "uninstall steps, which run as they are", js: quit + `,` + app7 + `,{"uninstall_postflight_steps":[{"steps":[{"type":"remove","paths":["~/x"]}]}]}`, want: "sudo"},
		{name: "uninstall preflight steps", js: quit + `,` + app7 + `,{"uninstall_preflight_steps":[{"steps":[{"type":"terminate_process","name":"c"}]}]}`, want: "sudo"},
		{name: "a target on a disk not mounted", js: quit + `,{"app":["Regrow Test C.app",{"target":"<root>/NotMounted/C.app"}],"target":"<root>/NotMounted/C.app"}`},
		{name: "a target brew printed with a .. inside", js: quit + `,{"app":["Regrow Test C.app"],"target":"<apps>/Sub/../Regrow Test C.app"}`},
		{name: "a pkg", js: quit + `,` + app7 + `,{"pkg":["C.pkg"]}`, want: "sudo"},
		{name: "a flight block", js: quit + `,` + app7 + `,{"uninstall_postflight":null}`, want: "sudo"},
		{name: "a command wrapper in a folder brew writes", links: []string{"bin/c"}, js: quit + `,` + app7 + `,{"command_wrapper":["c",{"executable":"<apps>/Regrow Test C.app/Contents/MacOS/c"}],"target":"<root>/bin/c"}`, want: "plain"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := oneCask(t, tc.js, tc.present...)
			root := filepath.Dir(c.Home)
			for _, l := range tc.links {
				writeFile(t, filepath.Join(root, l), 10)
			}
			for _, l := range tc.dead {
				link := filepath.Join(root, l)
				if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "NotMounted", filepath.Base(l)), link); err != nil {
					t.Fatal(err)
				}
			}
			if tc.roDir {
				if os.Geteuid() == 0 {
					t.Skip("root writes every folder")
				}
				ro := filepath.Join(root, "ro")
				if err := os.Chmod(ro, 0o555); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })
			}
			if tc.locked != "" {
				if os.Geteuid() == 0 {
					t.Skip("root reads every folder")
				}
				locked := filepath.Join(root, tc.locked)
				if err := os.MkdirAll(locked, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(locked, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
			}
			plain, err := c.Plain(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			sudo, err := c.NeedsPassword(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			got := ""
			switch {
			case len(plain)+len(sudo) > 1:
				t.Fatalf("plain %q, sudo %q: listed twice", caskTokens(plain), caskTokens(sudo))
			case len(plain) == 1:
				got = "plain"
			case len(sudo) == 1:
				got = "sudo"
			}
			if got != tc.want {
				t.Errorf("listed as %q, want %q", got, tc.want)
			}
		})
	}
}

// Without Apps the rule looks in /Applications and ~/Applications.
func TestCaskAppFolders(t *testing.T) {
	c := oneCask(t, `{"uninstall":[{"quit":"dev.example.c"}]},{"app":["Regrow Test C.app"],"target":"<apps>/Regrow Test C.app"}`, "home/Applications/Dev/Regrow Test C.app")
	c.Apps = nil
	if got, want := c.appFolders(), []string{"/Applications", filepath.Join(c.Home, "Applications")}; !slices.Equal(got, want) {
		t.Errorf("app folders %q, want %q", got, want)
	}
	plain, err := c.Plain(context.Background())
	if err != nil || len(plain) != 0 {
		t.Errorf("plain %q, err %v; the app is in ~/Applications/Dev", caskTokens(plain), err)
	}
}

// A folder regrow cannot read leaves the cask out; the refusal says
// it can no longer tell.
func TestCaskRecheckWhenAFolderCannotBeRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every folder")
	}
	c := oneCask(t, `{"uninstall":[{"quit":"dev.example.c"}]},{"app":["Regrow Test C.app"],"target":"<apps>/Regrow Test C.app"}`)
	root := filepath.Dir(c.Home)
	locked := filepath.Join(root, "Apps", "Locked")
	if err := os.MkdirAll(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	err := c.recheck(context.Background(), filepath.Join(root, "Caskroom", "c"))
	if err == nil || !strings.Contains(err.Error(), "or regrow can no longer tell") {
		t.Errorf("recheck: %v; want it to say regrow can no longer tell", err)
	}
}

func TestCaskRecordsBrew(t *testing.T) {
	c := &caskRecords{w: testWalker(), Brew: func(context.Context, ...string) ([]byte, bool, error) { return nil, false, nil }}
	if items, err := c.Plain(context.Background()); err != nil || len(items) != 0 {
		t.Errorf("without brew: items %v, err %v; want nothing", items, err)
	}
	c = &caskRecords{w: testWalker(), Brew: func(context.Context, ...string) ([]byte, bool, error) {
		return nil, false, errors.New("exit status 1")
	}}
	if _, err := c.NeedsPassword(context.Background()); err == nil {
		t.Error("a failing brew is no error")
	}
}

// The uninstall counts as done only once brew no longer lists the cask:
// brew has returned 0 and kept the record.
func TestCaskUninstallChecksTheRecord(t *testing.T) {
	r := catalogRule(t, "brew-cask-records")
	argv, err := r.NativeCommand.ExpandItem(engine.Item{Arg: "gone-plain"})
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(argv, func(a string) bool { return strings.Contains(a, "--zap") }) {
		t.Fatalf("command %q zaps: that deletes the app's data with no Trash", argv)
	}
	fake := `#!/bin/sh
d=$(dirname "$0")
echo "$*" >> "$d/calls"
case "$1" in
uninstall) exit "$UNINSTALL_EXIT";;
list) [ "$LIST_EXIT" = 0 ] || exit "$LIST_EXIT"; printf 'other\n%s\n' "$LISTED"; exit 0;;
esac
exit 2
`
	cases := []struct {
		name                    string
		uninstallExit, listExit string
		listed                  string
		ok                      bool
		stderr                  string
	}{
		{"gone", "0", "0", "", true, ""},
		{"brew kept the record", "0", "0", "gone-plain", false, "still lists cask gone-plain"},
		{"a cask whose name it ends with", "0", "0", "x-gone-plain", true, ""},
		{"the uninstall fails", "1", "0", "", false, ""},
		{"the listing fails", "0", "1", "", false, "brew list failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			writeText(t, filepath.Join(bin, "brew"), fake)
			if err := os.Chmod(filepath.Join(bin, "brew"), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(argv[0], argv[1:]...)
			cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "UNINSTALL_EXIT=" + tc.uninstallExit, "LIST_EXIT=" + tc.listExit, "LISTED=" + tc.listed}
			var stderr strings.Builder
			cmd.Stderr = &stderr
			err := cmd.Run()
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v (stderr %q), want ok %v", err, stderr.String(), tc.ok)
			}
			if tc.stderr != "" && !strings.Contains(stderr.String(), tc.stderr) {
				t.Errorf("stderr %q does not say %q", stderr.String(), tc.stderr)
			}
			calls, err := os.ReadFile(filepath.Join(bin, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(calls), "uninstall --cask gone-plain\n") {
				t.Errorf("calls %q, want the uninstall first", calls)
			}
		})
	}
}

func TestCaskRecordRules(t *testing.T) {
	r := catalogRule(t, "brew-cask-records")
	if r.Risk != engine.RiskCaution || r.PreAction != engine.PreActionCaskRecordRecheck || r.Sudo || !r.Beta || !r.NativeCommand.PerItem() {
		t.Errorf("brew-cask-records: risk %q, pre_action %q, sudo %v, beta %v, command %q; want beta caution, per item, with the recheck", r.Risk, r.PreAction, r.Sudo, r.Beta, r.NativeCommand)
	}
	s := catalogRule(t, "brew-cask-records-sudo")
	if s.Risk != engine.RiskSurfaceOnly || !s.Beta {
		t.Errorf("brew-cask-records-sudo: risk %q, beta %v; want beta surface-only", s.Risk, s.Beta)
	}
	for _, want := range []string{"`brew uninstall --cask <token>`", "password", "`brew uninstall --cask --force <token>`"} {
		if !strings.Contains(s.Note, want) {
			t.Errorf("brew-cask-records-sudo note %q does not say %q", s.Note, want)
		}
	}
	for _, id := range []string{"brew-cask-records", "brew-cask-records-sudo"} {
		if q := catalogRule(t, id).ToolQuery; DefaultQueries(engine.Host{})[q] == nil {
			t.Errorf("%s: query %q is not registered", id, q)
		}
	}
}
