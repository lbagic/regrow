package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// leftoversFixture is a fake Mac: a root that re-anchors /Applications,
// /System/Applications and /Library, a home under it, and a clock 100
// days past the moment the fixture was made, so every folder it makes
// has gone unchanged past the idle window unless a case stamps it.
type leftoversFixture struct {
	t    *testing.T
	host engine.Host
	made time.Time
	a    *appLeftovers

	mu        sync.Mutex
	spot      []string
	ents      map[string]string
	entErr    map[string]error
	entCalls  int
	procs     []string
	procsErr  error
	unchanged time.Time
}

func newLeftoversFixture(t *testing.T) *leftoversFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &leftoversFixture{
		t:      t,
		host:   engine.Host{OS: "darwin", Home: filepath.Join(root, "Users", "dev"), Root: root},
		made:   time.Now(),
		ents:   map[string]string{},
		entErr: map[string]error{},
	}
	now := f.made.Add(100 * 24 * time.Hour)
	f.unchanged = now
	f.a = &appLeftovers{
		w:    testWalker(),
		host: f.host,
		idle: appLeftoversIdle,
		now:  func() time.Time { return now },
		spotlight: func(context.Context) []string {
			f.mu.Lock()
			defer f.mu.Unlock()
			return slices.Clone(f.spot)
		},
		entitlements: func(_ context.Context, bundle string) ([]byte, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.entCalls++
			if err := f.entErr[bundle]; err != nil {
				return nil, err
			}
			return []byte(f.ents[bundle]), nil
		},
		procs: func(context.Context) ([]string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return slices.Clone(f.procs), f.procsErr
		},
	}
	if err := os.MkdirAll(filepath.Join(f.host.Home, "Library"), 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

// folder makes ~/Library/<loc>/<name> holding one file, as old as the
// fixture.
func (f *leftoversFixture) folder(loc, name string) string {
	f.t.Helper()
	p := filepath.Join(f.host.Home, "Library", loc, name)
	writeFile(f.t, filepath.Join(p, "data.bin"), 4096)
	return p
}

// app installs a bundle at a rule path (/Applications/Foo.app) with
// the id given; ext bundles inside it take their own Contents.
func (f *leftoversFixture) app(path, id string) string {
	f.t.Helper()
	p := f.host.ExpandPath(path)
	f.plist(filepath.Join(p, "Contents", "Info.plist"), id)
	return p
}

func (f *leftoversFixture) plist(path, id string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(infoPlist(id, "1.0")), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// touch stamps a path changed at the given number of days before the
// fixture's now.
func (f *leftoversFixture) touch(path string, daysAgo int) {
	f.t.Helper()
	at := f.unchanged.Add(-time.Duration(daysAgo) * 24 * time.Hour)
	if err := os.Chtimes(path, at, at); err != nil {
		f.t.Fatal(err)
	}
}

func (f *leftoversFixture) write(path, content string) {
	f.t.Helper()
	p := f.host.ExpandPath(path)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *leftoversFixture) judge() (offered, kept []engine.Item) {
	f.t.Helper()
	offered, kept, err := f.a.judge(context.Background(), "")
	if err != nil {
		f.t.Fatal(err)
	}
	return offered, kept
}

func paths(items []engine.Item) []string {
	var out []string
	for _, it := range items {
		if it.Path != "" {
			out = append(out, it.Path)
		}
	}
	return out
}

// keptWhy returns the reason the kept row of path gives, "" when there
// is no such row.
func keptWhy(kept []engine.Item, path string) string {
	for _, it := range kept {
		if it.Path == path {
			_, why, _ := strings.Cut(it.Label, " — ")
			return why
		}
	}
	return ""
}

func launchdJob(label string, args ...string) string {
	var b strings.Builder
	for _, a := range args {
		fmt.Fprintf(&b, "<string>%s</string>", a)
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>Label</key><string>%s</string><key>ProgramArguments</key><array>%s</array></dict></plist>
`, label, b.String())
}

func entitlementsXML(groups ...string) string {
	var b strings.Builder
	for _, g := range groups {
		fmt.Fprintf(&b, "<string>%s</string>", g)
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>com.apple.security.application-groups</key><array>%s</array></dict></plist>`, b.String())
}

// A folder named by the id of anything installed is owned: an app in
// any of the three Applications folders or a folder in one, its
// plug-ins, helpers and login items, an iOS app's wrapped bundle, a
// binary Info.plist; the folder of an id nothing has is offered.
func TestAppLeftoversOwnedByInstalledBundles(t *testing.T) {
	f := newLeftoversFixture(t)
	app := f.app("/Applications/Alpha.app", "com.alpha.app")
	f.plist(filepath.Join(app, "Contents", "PlugIns", "Share.appex", "Contents", "Info.plist"), "net.share.ext")
	f.plist(filepath.Join(app, "Contents", "Library", "LoginItems", "Login.app", "Contents", "Info.plist"), "org.login.helper")
	f.plist(filepath.Join(app, "Contents", "Frameworks", "Kit.framework", "Versions", "A", "XPCServices", "Svc.xpc", "Contents", "Info.plist"), "io.svc.xpc")
	f.plist(filepath.Join(app, "Contents", "Library", "LoginItems", "Lower.app", "Contents", "info.plist"), "org.helper.lower")
	f.app("/Applications/Utilities/Beta.app", "com.beta.app")
	f.app("~/Applications/Gamma.app", "com.gamma.app")
	f.app("/System/Applications/Delta.app", "com.delta.app")
	f.plist(f.host.ExpandPath("/Applications/Phone.app/Wrapper/Phone.app/Info.plist"), "com.phone.app")
	bin, err := os.ReadFile(filepath.Join("testdata", "plist", "info.bplist"))
	if err != nil {
		t.Fatal(err)
	}
	f.write("/Applications/Example.app/Contents/Info.plist", string(bin))

	var owned []string
	for _, id := range []string{"com.alpha.app", "net.share.ext", "org.login.helper", "io.svc.xpc", "org.helper.lower", "com.beta.app", "com.gamma.app", "com.delta.app", "com.phone.app", "dev.example.app"} {
		owned = append(owned, f.folder("Containers", id))
	}
	owned = append(owned, f.folder("HTTPStorages", "COM.ALPHA.APP"))
	gone := f.folder("Containers", "org.gone.app")

	offered, kept := f.judge()
	if got := paths(offered); !slices.Equal(got, []string{gone}) {
		t.Errorf("offered %v, want only %s", got, gone)
	}
	for _, p := range owned {
		if why := keptWhy(kept, p); why != "" {
			t.Errorf("%s is owned, yet kept as a leftover: %s", p, why)
		}
	}
}

// An app group an installed bundle's entitlements name is owned; an
// installed bundle codesign cannot read for another reason than
// having no signature leaves nothing offered.
func TestAppLeftoversOwnedByEntitlements(t *testing.T) {
	f := newLeftoversFixture(t)
	app := f.app("/Applications/Alpha.app", "com.alpha.app")
	ext := filepath.Join(app, "Contents", "PlugIns", "Widget.appex")
	f.plist(filepath.Join(ext, "Contents", "Info.plist"), "com.alpha.app.widget")
	f.ents[ext] = entitlementsXML("ABCDE12345.net.shared.store")
	owned := f.folder("Group Containers", "ABCDE12345.net.shared.store")
	gone := f.folder("Group Containers", "ABCDE12345.net.gone.store")

	offered, _ := f.judge()
	if got := paths(offered); !slices.Equal(got, []string{gone}) {
		t.Fatalf("offered %v, want only %s, not the group %s names", got, gone, owned)
	}

	f.entErr[app] = errors.New("codesign could not read it")
	if offered, kept, err := f.a.judge(context.Background(), ""); err == nil {
		t.Errorf("offered %v and kept %v with an installed app's entitlements unread, want an error", paths(offered), paths(kept))
	}
}

// Spotlight and the running processes add owners outside the
// Applications folders; a bundle there that cannot be read adds none
// and is no error.
func TestAppLeftoversOwnedBySpotlightAndRunningApps(t *testing.T) {
	f := newLeftoversFixture(t)
	spotted := f.app("/Volumes/Games/Spotted.app", "com.spotted.app")
	running := f.app("/Volumes/Image/Running.app", "com.running.app")
	f.spot = []string{spotted, f.host.ExpandPath("/Volumes/Gone/Missing.app")}
	f.procs = []string{filepath.Join(running, "Contents", "MacOS", "Running")}
	f.folder("Application Support", "com.spotted.app")
	f.folder("Containers", "com.spotted.app")
	f.folder("Caches", "com.running.app")
	f.folder("HTTPStorages", "com.running.app")

	offered, kept := f.judge()
	for _, p := range append(paths(offered), paths(kept)...) {
		t.Errorf("%s is listed, but Spotlight or a running app owns it", p)
	}
}

// Every way the installed apps cannot all be read leaves nothing
// offered: a folder in an Applications folder that cannot be read, a
// link there that cannot be followed, an Info.plist that does not
// parse.
func TestAppLeftoversUnreadableOwnersAreAnError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads every folder")
	}
	cases := map[string]func(t *testing.T, f *leftoversFixture){
		"a folder that refuses": func(t *testing.T, f *leftoversFixture) {
			app := f.app("/Applications/Locked.app", "com.locked.app")
			chmod(t, filepath.Join(app, "Contents"), 0)
		},
		"a link to nowhere": func(t *testing.T, f *leftoversFixture) {
			if err := os.MkdirAll(f.host.ExpandPath("/Applications"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(f.host.ExpandPath("/Volumes/Gone/Away.app"), f.host.ExpandPath("/Applications/Away.app")); err != nil {
				t.Fatal(err)
			}
		},
		"an Info.plist that is no plist": func(_ *testing.T, f *leftoversFixture) {
			f.write("/Applications/Odd.app/Contents/Info.plist", "CFBundleIdentifier=com.odd.app")
		},
		"an Info.plist that refuses in an app Spotlight knows": func(t *testing.T, f *leftoversFixture) {
			locked := f.app("/Volumes/USB/Locked.app", "com.locked.app")
			chmod(t, filepath.Join(locked, "Contents", "Info.plist"), 0)
			f.spot = []string{locked}
		},
		"a running app's Info.plist that is no plist": func(_ *testing.T, f *leftoversFixture) {
			f.write("/Volumes/Y/Running.app/Contents/Info.plist", "not a plist")
			f.procs = []string{f.host.ExpandPath("/Volumes/Y/Running.app/Contents/MacOS/Running")}
		},
		"a folder that refuses in an app Spotlight knows": func(t *testing.T, f *leftoversFixture) {
			spotted := f.app("/Volumes/X/Spotted.app", "com.spotted.app")
			plugins := filepath.Join(spotted, "Contents", "PlugIns")
			if err := os.MkdirAll(plugins, 0o755); err != nil {
				t.Fatal(err)
			}
			chmod(t, plugins, 0)
			f.spot = []string{spotted}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newLeftoversFixture(t)
			f.app("/Applications/Alpha.app", "com.alpha.app")
			f.folder("Containers", "org.gone.app")
			setup(t, f)
			if offered, _, err := f.a.judge(context.Background(), ""); err == nil {
				t.Errorf("offered %v, want an error", paths(offered))
			}
		})
	}
}

// A link in an Applications folder, or in a folder directly in one,
// is followed to the apps behind it.
func TestAppLeftoversFollowLinkedAppFolders(t *testing.T) {
	f := newLeftoversFixture(t)
	elsewhere := f.app("/Volumes/Apps/Linked.app", "com.linked.app")
	if err := os.MkdirAll(f.host.ExpandPath("/Applications"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, f.host.ExpandPath("/Applications/Linked.app")); err != nil {
		t.Fatal(err)
	}
	inner := f.app("/Volumes/Apps/Inner.app", "com.inner.app")
	if err := os.MkdirAll(f.host.ExpandPath("/Applications/Tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inner, f.host.ExpandPath("/Applications/Tools/Inner.app")); err != nil {
		t.Fatal(err)
	}
	f.folder("Containers", "com.linked.app")
	f.folder("Containers", "com.inner.app")
	offered, _ := f.judge()
	if got := paths(offered); len(got) > 0 {
		t.Errorf("offered %v, want the linked apps to own their folders", got)
	}
}

// A launchd job that runs a program in a folder, works in it or is
// labelled with its id keeps it, in the user's and the system's job
// folders, however the home is spelled.
func TestAppLeftoversKeepLaunchdTargets(t *testing.T) {
	f := newLeftoversFixture(t)
	byPath := f.folder("Application Support", "com.bypath.agent")
	byTilde := f.folder("Containers", "com.bytilde.app")
	byShell := f.folder("Caches", "com.byshell.app")
	f.folder("Containers", "com.byshell.app")
	byLabel := f.folder("Containers", "com.bylabel.daemon")
	quoted := f.folder("Containers", "org.quoted.app")
	escaped := f.folder("Application Support", "org.escaped.app")
	f.folder("Containers", "org.escaped.app")
	free := f.folder("Containers", "org.near.free")
	f.folder("Containers", "com.bypath.agent")
	f.write("~/Library/LaunchAgents/com.bypath.agent.plist", launchdJob("job.one", byPath+"/bin/run", "--flag"))
	f.write("~/Library/LaunchAgents/tilde.plist", launchdJob("job.two", "~/Library/Containers/com.bytilde.app/Data/run"))
	f.write("/Library/LaunchAgents/shell.plist", launchdJob("job.three", "/bin/sh", "-c", `cd $HOME/Library/Caches/com.byshell.app &amp;&amp; ./run`))
	f.write("/Library/LaunchDaemons/com.bylabel.daemon.plist", launchdJob("com.bylabel.daemon", "/usr/local/bin/daemon"))
	f.write("~/Library/LaunchAgents/quoted.plist", launchdJob("job.five", "/bin/sh", "-c", `cd "$HOME/Library/Containers"/org.quoted.app &amp;&amp; ./run`))
	f.write("~/Library/LaunchAgents/escaped.plist", launchdJob("job.six", `~/Library/Application\ Support/org.escaped.app/run`))
	f.write("~/Library/LaunchAgents/near.plist", launchdJob("job.four", "~/Library/Containers/org.near.free2/run"))

	offered, kept := f.judge()
	for _, p := range []string{byPath, byTilde, byShell, byLabel, quoted, escaped} {
		if slices.Contains(paths(offered), p) {
			t.Errorf("offered %s, which a launchd job points into", p)
		}
		if why := keptWhy(kept, p); !strings.Contains(why, "the launchd job") {
			t.Errorf("%s: kept for %q, want the launchd job named", p, why)
		}
	}
	if !slices.Contains(paths(offered), free) {
		t.Errorf("offered %v, want %s: a job points into a folder whose name only starts like it", paths(offered), free)
	}
}

// A browser's native messaging host that runs a program in a folder
// keeps it, for every browser the rule reads, the user's and the
// system's.
func TestAppLeftoversKeepNativeMessagingTargets(t *testing.T) {
	for _, dir := range []string{
		"~/Library/Application Support/Google/Chrome/NativeMessagingHosts",
		"~/Library/Application Support/Google/Chrome Beta/NativeMessagingHosts",
		"~/Library/Application Support/Chromium/NativeMessagingHosts",
		"~/Library/Application Support/Arc/User Data/NativeMessagingHosts",
		"~/Library/Application Support/Microsoft Edge/NativeMessagingHosts",
		"~/Library/Application Support/BraveSoftware/Brave-Browser/NativeMessagingHosts",
		"~/Library/Application Support/Vivaldi/NativeMessagingHosts",
		"~/Library/Application Support/Mozilla/NativeMessagingHosts",
		"/Library/Google/Chrome/NativeMessagingHosts",
		"~/Library/Application Support/com.operasoftware.Opera/NativeMessagingHosts",
		"/Library/Microsoft/Edge/NativeMessagingHosts",
		"/Library/Application Support/Chromium/NativeMessagingHosts",
		"/Library/BraveSoftware/Brave-Browser/NativeMessagingHosts",
		"/Library/Application Support/Mozilla/NativeMessagingHosts",
	} {
		t.Run(dir, func(t *testing.T) {
			f := newLeftoversFixture(t)
			host := f.folder("Containers", "com.gone.bridge")
			f.write(dir+"/dev.example.bridge.json", fmt.Sprintf(`{"name":"dev.example.bridge","path":%q,"type":"stdio"}`, host+"/Data/bridge"))
			offered, kept := f.judge()
			if len(offered) > 0 {
				t.Errorf("offered %v, want the host's folder kept", paths(offered))
			}
			if why := keptWhy(kept, host); !strings.Contains(why, "the native messaging host") {
				t.Errorf("kept for %q, want the manifest named", why)
			}
		})
	}
}

// A running process whose executable, a mapped file or working folder
// is in a folder keeps it.
func TestAppLeftoversKeepRunningProcessFolders(t *testing.T) {
	f := newLeftoversFixture(t)
	exe := f.folder("Application Support", "com.gone.daemon")
	f.folder("Saved Application State", "com.gone.daemon.savedState")
	cwd := f.folder("Containers", "org.worker.app")
	f.procs = []string{exe + "/bin/daemon", cwd, "/usr/lib/dyld"}
	offered, kept := f.judge()
	if len(offered) > 0 {
		t.Errorf("offered %v, want the running process's folders kept", paths(offered))
	}
	for _, p := range []string{exe, cwd} {
		if why := keptWhy(kept, p); !strings.Contains(why, "a running process") {
			t.Errorf("%s: kept for %q, want the running process named", p, why)
		}
	}
}

// A pointer that cannot be read leaves nothing offered: a launchd job
// that does not parse, a process list that fails.
func TestAppLeftoversUnreadablePointersAreAnError(t *testing.T) {
	t.Run("job", func(t *testing.T) {
		f := newLeftoversFixture(t)
		f.folder("Containers", "org.gone.app")
		f.write("~/Library/LaunchAgents/broken.plist", "<plist><dict><key>Label</key>")
		if offered, _, err := f.a.judge(context.Background(), ""); err == nil {
			t.Errorf("offered %v, want an error", paths(offered))
		}
	})
	t.Run("processes", func(t *testing.T) {
		f := newLeftoversFixture(t)
		f.folder("Containers", "org.gone.app")
		f.procsErr = errors.New("lsof failed")
		if offered, _, err := f.a.judge(context.Background(), ""); err == nil {
			t.Errorf("offered %v, want an error", paths(offered))
		}
	})
}

// Only an exact id, read in full and unchanged for 90 days, is
// offered; everything else that could be a leftover is kept and says
// why.
func TestAppLeftoversOfferOnlyIdleExactIDs(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads every folder")
	}
	f := newLeftoversFixture(t)
	f.app("/Applications/Maker.app", "com.maker.app")
	idle := f.folder("Containers", "org.gone.app")
	evidenced := f.folder("Application Support", "net.gone.tool")
	contained := f.folder("Containers", "net.gone.tool")
	saved := f.folder("Saved Application State", "io.gone.app.savedState")
	recent := f.folder("Containers", "org.recent.app")
	f.touch(filepath.Join(recent, "data.bin"), 10)
	unread := f.folder("Containers", "org.locked.app")
	writeFile(t, filepath.Join(unread, "inner", "x.bin"), 10)
	chmod(t, filepath.Join(unread, "inner"), 0)
	sameMaker := f.folder("Containers", "com.maker.app.oldext")
	noEvidence := f.folder("Application Support", "com.vendor.cli")
	product := f.folder("Application Support", "Old Game")
	ownedProduct := f.folder("Application Support", "Maker")
	f.folder("Caches", "Some Tool")

	offered, kept := f.judge()
	want := []string{evidenced, contained, idle, saved}
	slices.Sort(want)
	if got := paths(offered); !slices.Equal(got, want) {
		t.Errorf("offered %v, want %v", got, want)
	}
	for path, why := range map[string]string{
		recent:     "changed 10 days ago",
		unread:     "could not be read in full",
		sameMaker:  "an app of the same maker is installed: Maker.app",
		noEvidence: "macOS keeps no container",
		product:    "named for a product, not an app id; no installed app has that name",
	} {
		if got := keptWhy(kept, path); !strings.Contains(got, why) {
			t.Errorf("%s: kept for %q, want %q", path, got, why)
		}
	}
	for _, it := range kept {
		if it.Path == unread && !it.Partial {
			t.Errorf("%s is kept as read in full", unread)
		}
		if it.Path == ownedProduct || strings.HasSuffix(it.Path, "Some Tool") {
			t.Errorf("listed %s: an installed app has its name, or it is no app's folder", it.Path)
		}
	}
	for _, it := range offered {
		if it.Label == "" || !strings.Contains(it.Label, " · ") {
			t.Errorf("offered %s with label %q, want the id and the location", it.Path, it.Label)
		}
	}
}

// A fuzzy match that changed in the last 90 days, or could not be read
// in full, is no row; one that could not be read leaves a marker.
func TestAppLeftoversFuzzyMatchesWaitTheIdleWindow(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads every folder")
	}
	f := newLeftoversFixture(t)
	busy := f.folder("Application Support", "Busy Tool")
	f.touch(filepath.Join(busy, "data.bin"), 3)
	locked := f.folder("Application Support", "Locked Tool")
	chmod(t, locked, 0)
	_, kept := f.judge()
	if got := paths(kept); len(got) > 0 {
		t.Errorf("kept rows %v, want none", got)
	}
	if !slices.ContainsFunc(kept, func(it engine.Item) bool {
		return it.Path == "" && it.Partial && it.Label == "folders under ~/Library/Application Support"
	}) {
		t.Errorf("kept %+v, want a marker for the folder that could not be read", kept)
	}
}

// macOS's own folders are never listed: com.apple ids, and group ids
// built on one behind a team id or a groups label.
func TestAppLeftoversLeaveMacOSFoldersAlone(t *testing.T) {
	f := newLeftoversFixture(t)
	for _, n := range []string{"com.apple.Safari", "group.com.apple.notes", "243LU875E5.groups.com.apple.podcasts", "59GAB85EFG.com.apple.dt.Xcode"} {
		f.folder("Group Containers", n)
	}
	offered, kept := f.judge()
	if len(offered)+len(kept) > 0 {
		t.Errorf("listed %v and %v, want macOS's folders left alone", paths(offered), paths(kept))
	}
}

// A location that blocks or refuses is a marker, never an empty
// location or a leftover.
func TestAppLeftoversBlockedLocations(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads every folder")
	}
	f := newLeftoversFixture(t)
	f.folder("Containers", "org.gone.app")
	f.folder("Group Containers", "ABCDE12345.org.gone")
	refused := filepath.Join(f.host.Home, "Library", "Group Containers")
	chmod(t, refused, 0)
	w, _ := blockingWalker(t, filepath.Join(f.host.Home, "Library", "Containers"))
	f.a.w = w
	offered, kept := f.judge()
	if got := paths(append(offered, kept...)); len(got) > 0 {
		t.Errorf("listed %v from locations that could not be read", got)
	}
	for _, loc := range []string{"~/Library/Containers", "~/Library/Group Containers"} {
		if !slices.ContainsFunc(offered, func(it engine.Item) bool { return it.Path == "" && it.Partial && it.Label == loc }) {
			t.Errorf("offered %+v, want a marker for %s", offered, loc)
		}
	}
}

// The recheck passes a folder still offered, refuses one an app owns
// now or one that changed, and reuses the installed apps it read for a
// minute.
func TestRecheckAppLeftover(t *testing.T) {
	f := newLeftoversFixture(t)
	f.app("/Applications/Alpha.app", "com.alpha.app")
	gone := f.folder("Containers", "org.gone.app")
	other := f.folder("Containers", "org.other.app")
	f.a.memo = &ownersMemo{}
	ctx := context.Background()
	if err := f.a.recheck(ctx, gone); err != nil {
		t.Fatalf("recheck of an offered folder: %v", err)
	}
	calls := f.entCalls
	f.touch(other, 1)
	if err := f.a.recheck(ctx, other); err == nil || !strings.Contains(err.Error(), "is kept now") {
		t.Errorf("recheck of a folder changed yesterday = %v, want it kept", err)
	}
	if f.entCalls != calls {
		t.Errorf("the second recheck within a minute read entitlements again")
	}
	later := f.unchanged.Add(2 * ownersReuse)
	f.a.now = func() time.Time { return later }
	if err := f.a.recheck(ctx, gone); err != nil || f.entCalls == calls {
		t.Errorf("a recheck past the memo's minute = %v, read entitlements again: %v", err, f.entCalls != calls)
	}
	calls = f.entCalls
	f.a.memo.home = "/elsewhere"
	if err := f.a.recheck(ctx, gone); err != nil || f.entCalls == calls {
		t.Errorf("a recheck for another home = %v, read entitlements again: %v", err, f.entCalls != calls)
	}
	dmg := f.app("/Volumes/Dmg/Dmg.app", "com.dmg.app")
	web := f.folder("HTTPStorages", "com.dmg.app")
	f.procs = []string{filepath.Join(dmg, "Contents", "MacOS", "Dmg")}
	if err := f.a.recheck(ctx, web); err == nil || !strings.Contains(err.Error(), "no leftover now") {
		t.Errorf("recheck of an app's folder after the app started = %v, want a refusal", err)
	}
	f.procs = nil
	scripted := f.folder("Application Support", "com.gone.scripted")
	scripts := f.folder("Application Scripts", "com.gone.scripted")
	if err := os.RemoveAll(scripts); err != nil {
		t.Fatal(err)
	}
	if err := f.a.recheck(ctx, scripted); err != nil {
		t.Errorf("recheck after the run moved the folder that showed an app = %v, want it passed", err)
	}
	f.app("/Applications/Gone.app", "org.gone.app")
	f.a.memo = &ownersMemo{}
	if err := f.a.recheck(ctx, gone); err == nil || !strings.Contains(err.Error(), "no leftover now") {
		t.Errorf("recheck after the app came back = %v, want a refusal", err)
	}
	if err := f.a.recheck(ctx, filepath.Join(f.host.Home, "Library", "Containers", "com.apple.Safari")); err == nil {
		t.Errorf("recheck of macOS's folder passed")
	}
}

// The id and maker of a folder's name.
func TestLeftoverNames(t *testing.T) {
	ids := map[[2]string]string{
		{"Containers", "com.example.app"}:                 "com.example.app",
		{"Saved Application State", "com.a.b.savedState"}: "com.a.b",
		{"Saved Application State", "com.a.b"}:            "",
		{"Application Support", "io.sentry"}:              "",
		{"Application Support", "unity.Team Cherry.Game"}: "",
		{"Application Support", "Docker Desktop"}:         "",
		{"Group Containers", "UBF8T346G9.ms"}:             "",
		{"Group Containers", "2BUA8C4S2C.com.1password"}:  "2BUA8C4S2C.com.1password",
		{"Caches", "com.foo_bar.app"}:                     "",
		{"Caches", "com..app"}:                            "",
	}
	for in, want := range ids {
		if got := leftoverID(in[0], in[1]); got != want {
			t.Errorf("leftoverID(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
	vendors := map[string]string{
		"com.docker.docker":                "com.docker",
		"group.com.docker":                 "com.docker",
		"UBF8T346G9.com.microsoft.to-do":   "com.microsoft",
		"6H4HRTU5E3.group.com.avast.osx":   "com.avast",
		"vgroup.com.viber.osx":             "com.viber",
		"243LU875E5.groups.com.apple.pods": "com.apple",
	}
	for id, want := range vendors {
		if got := vendorOf(id); got != want {
			t.Errorf("vendorOf(%q) = %q, want %q", id, got, want)
		}
	}
	for id, want := range map[string]bool{
		"com.apple.Safari": true, "group.com.apple.notes": true, "243LU875E5.groups.com.apple.podcasts": true,
		"com.applesauce.app": false, "net.apple.cider": false, "com.example.com.apple": false,
	} {
		if got := appleID(id); got != want {
			t.Errorf("appleID(%q) = %v, want %v", id, got, want)
		}
	}
}

// No rule names a folder the leftover rules could list: equal paths
// in the containment forest would refuse the other rule's item.
func TestAppLeftoversNeverListAnotherRulesFolder(t *testing.T) {
	catalog, err := engine.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range catalog {
		for _, e := range r.Paths["darwin"] {
			rest, ok := strings.CutPrefix(filepath.Clean(e.Path), "~/Library/")
			if !ok {
				continue
			}
			loc, name, ok := strings.Cut(rest, "/")
			if !ok || strings.Contains(name, "/") || !slices.Contains(leftoverLocations, loc) {
				continue
			}
			id := leftoverID(loc, name)
			if (id != "" && !appleID(id)) || (id == "" && loc == "Application Support") {
				t.Errorf("%s names %s, a folder the app leftover rules can list", r.ID, e.Path)
			}
		}
	}
}

// A settings file, web data or a cookie store is no sign that an app of
// the id ran: a tool, a library or a game engine names them as it
// likes. So a folder in Application Support named like an id beside
// only those is kept, while one beside saved state is offered; and an
// id whose product an installed app has the name of may be that app's.
func TestAppLeftoversAppSignsAreAnAppsOwn(t *testing.T) {
	f := newLeftoversFixture(t)
	f.app("/Applications/Overcooked 2.app", "com.team17.overcooked2")
	unity := f.folder("Application Support", "unity.Team17.Overcooked2")
	f.write("~/Library/Preferences/unity.Team17.Overcooked2.plist", "<plist/>")
	renamed := f.folder("Containers", "com.oldmaker.overcooked2")
	cli := f.folder("Application Support", "dev.deploy.cli")
	f.folder("HTTPStorages", "dev.deploy.cli")
	prefs := f.folder("Application Support", "io.tool.cli")
	f.write("~/Library/Preferences/io.tool.cli.plist", "<plist/>")
	web := f.folder("Application Support", "io.web.cli")
	f.folder("WebKit", "io.web.cli")
	saved := f.folder("Application Support", "com.gone.app")
	f.folder("Saved Application State", "com.gone.app.savedState")
	browser := f.app("/Applications/Browser.app", "com.browser.app")
	f.plist(filepath.Join(browser, "Contents", "Frameworks", "Browser Helper.app", "Contents", "Info.plist"), "com.browser.app.helper")
	helped := f.folder("Containers", "com.vault.browser-helper")
	f.app("/Applications/Pie.app", "com.bakery.pie")
	short := f.folder("Containers", "com.gone.pie")
	grouped := f.folder("Application Support", "com.signed.app")
	f.folder("Group Containers", "group.com.signed.app")
	teamed := f.folder("Caches", "com.teamed.app")
	f.folder("Application Scripts", "ABCDE12345.com.teamed.app")

	offered, kept := f.judge()
	for _, p := range []string{saved, helped, short, grouped, teamed} {
		if !slices.Contains(paths(offered), p) {
			t.Errorf("offered %v, want %s: macOS made it for an app nothing installed has", paths(offered), p)
		}
	}
	for _, p := range []string{unity, renamed, cli, prefs, web} {
		if slices.Contains(paths(offered), p) {
			t.Errorf("offered %s, which no sign of an app backs", p)
		}
	}
	for path, why := range map[string]string{
		unity:   "may belong to Overcooked 2.app",
		renamed: "may belong to Overcooked 2.app",
		cli:     "macOS keeps no container",
		prefs:   "macOS keeps no container",
		web:     "macOS keeps no container",
	} {
		if got := keptWhy(kept, path); !strings.Contains(got, why) {
			t.Errorf("%s: kept for %q, want %q", path, got, why)
		}
	}
}

// One app's folders go together: a change, a pointer or a read in part
// in any folder of the id, or in its settings file, keeps every folder
// of it, in the scan and in the recheck.
func TestAppLeftoversOneAppsFoldersGoTogether(t *testing.T) {
	f := newLeftoversFixture(t)
	dmg := f.folder("Application Support", "com.dmg.app")
	used := f.folder("Containers", "com.dmg.app")
	f.touch(filepath.Join(used, "data.bin"), 3)
	jobDir := f.folder("Application Support", "com.gone.app")
	f.folder("Containers", "com.gone.app")
	jobWeb := f.folder("HTTPStorages", "com.gone.app")
	f.write("~/Library/LaunchAgents/updater.plist", launchdJob("com.gone.updater", "~/Library/Application Support/com.gone.app/Updater"))
	grouped := f.folder("Containers", "com.grp.app")
	group := f.folder("Group Containers", "ABCDE12345.com.grp.app")
	f.touch(filepath.Join(group, "data.bin"), 5)
	settled := f.folder("Containers", "com.pref.app")
	f.write("~/Library/Preferences/com.pref.app.plist", "<plist/>")
	f.touch(f.host.ExpandPath("~/Library/Preferences/com.pref.app.plist"), 5)
	grouponly := f.folder("Group Containers", "ABCDE12345.com.prefgroup.app")
	f.write("~/Library/Preferences/com.prefgroup.app.plist", "<plist/>")
	f.touch(f.host.ExpandPath("~/Library/Preferences/com.prefgroup.app.plist"), 5)
	box := f.folder("Containers", "com.vendor.app")
	f.folder("Group Containers", "group.com.vendor")
	f.write("~/Library/LaunchAgents/com.vendor.agent.plist", launchdJob("com.vendor.agent", "~/Library/Group Containers/group.com.vendor/agent"))
	box2 := f.folder("Containers", "com.vendor2.app")
	shared := f.folder("Group Containers", "group.com.vendor2")
	f.touch(filepath.Join(shared, "data.bin"), 3)
	main := f.folder("Containers", "com.helped.app")
	helper := f.folder("Containers", "com.helped.app.helper")
	f.touch(filepath.Join(helper, "data.bin"), 2)
	free := f.folder("Containers", "com.free.app")

	offered, kept := f.judge()
	if got := paths(offered); !slices.Equal(got, []string{free}) {
		t.Errorf("offered %v, want only %s", got, free)
	}
	for path, why := range map[string]string{
		dmg:       "Containers/com.dmg.app",
		jobDir:    "the launchd job",
		jobWeb:    "the launchd job",
		grouped:   "ABCDE12345.com.grp.app",
		settled:   "Preferences/com.pref.app.plist",
		grouponly: "Preferences/com.prefgroup.app.plist",
		box:       "the launchd job",
		box2:      "group.com.vendor2",
		main:      "com.helped.app.helper",
	} {
		if got := keptWhy(kept, path); !strings.Contains(got, why) {
			t.Errorf("%s: kept for %q, want %q", path, got, why)
		}
	}
	f.a.memo = &ownersMemo{}
	if err := f.a.recheck(context.Background(), dmg); err == nil || !strings.Contains(err.Error(), "is kept now") {
		t.Errorf("recheck of %s = %v, want it kept for its container", dmg, err)
	}
}

// A launchd job or a native messaging manifest that is a link into a
// folder keeps it: launchd and the browser read the file there.
func TestAppLeftoversKeepLinkedJobTargets(t *testing.T) {
	f := newLeftoversFixture(t)
	dir := f.folder("Application Support", "com.gone.app")
	f.folder("Saved Application State", "com.gone.app.savedState")
	f.write("~/Library/Application Support/com.gone.app/agent.plist", launchdJob("com.other.agent", "/usr/bin/python3", "-m", "gone"))
	if err := os.MkdirAll(f.host.ExpandPath("~/Library/LaunchAgents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "agent.plist"), f.host.ExpandPath("~/Library/LaunchAgents/com.other.agent.plist")); err != nil {
		t.Fatal(err)
	}
	rel := f.folder("Application Support", "org.rel.app")
	f.folder("Saved Application State", "org.rel.app.savedState")
	f.write("~/Library/Application Support/org.rel.app/agent.plist", launchdJob("com.rel.agent", "/usr/bin/true"))
	if err := os.Symlink("../Application Support/org.rel.app/agent.plist", f.host.ExpandPath("~/Library/LaunchAgents/com.rel.agent.plist")); err != nil {
		t.Fatal(err)
	}
	chain := f.folder("Application Support", "net.chain.app")
	f.folder("Saved Application State", "net.chain.app.savedState")
	f.write("~/Library/Application Support/net.chain.app/agent.plist", launchdJob("com.chain.agent", "/usr/bin/true"))
	hop := f.host.ExpandPath("/Volumes/L/chain.plist")
	if err := os.MkdirAll(filepath.Dir(hop), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(chain, "agent.plist"), hop); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(hop, f.host.ExpandPath("~/Library/LaunchAgents/com.chain.agent.plist")); err != nil {
		t.Fatal(err)
	}
	dangling := f.folder("Application Support", "org.dang.app")
	f.folder("Saved Application State", "org.dang.app.savedState")
	if err := os.Symlink("../Application Support/org.dang.app/gone.plist", f.host.ExpandPath("~/Library/LaunchAgents/org.dang.agent.plist")); err != nil {
		t.Fatal(err)
	}
	offered, kept := f.judge()
	for _, p := range []string{dir, rel, chain, dangling} {
		if slices.Contains(paths(offered), p) {
			t.Errorf("offered %s, which a launchd job is a link into", p)
		}
		if why := keptWhy(kept, p); !strings.Contains(why, "the launchd job") {
			t.Errorf("%s: kept for %q, want the launchd job named", p, why)
		}
	}
}

// An app found only by Spotlight or because it runs owns what its
// plug-ins and services own, as an app in /Applications does.
func TestAppLeftoversNestedBundlesOutsideTheRoots(t *testing.T) {
	f := newLeftoversFixture(t)
	spotted := f.app("/Volumes/X/Spotted.app", "com.spotted.app")
	widget := filepath.Join(spotted, "Contents", "PlugIns", "W.appex")
	f.plist(filepath.Join(widget, "Contents", "Info.plist"), "net.other.widget")
	f.ents[widget] = entitlementsXML("group.io.other.shared")
	running := f.app("/Volumes/Y/Running.app", "com.running.app")
	f.plist(filepath.Join(running, "Contents", "XPCServices", "S.xpc", "Contents", "Info.plist"), "org.third.service")
	outer := f.app("/Volumes/Z/Main.app", "org.main.app")
	loginItem := filepath.Join(outer, "Contents", "Library", "LoginItems", "Helper.app")
	f.plist(filepath.Join(loginItem, "Contents", "Info.plist"), "net.vendor.helper")
	f.spot = []string{spotted}
	f.procs = []string{filepath.Join(running, "Contents", "MacOS", "Running"), filepath.Join(loginItem, "Contents", "MacOS", "Helper")}
	f.folder("Containers", "org.main.app")
	f.folder("Containers", "net.other.widget")
	f.folder("Group Containers", "group.io.other.shared")
	f.folder("Containers", "org.third.service")

	offered, kept := f.judge()
	for _, p := range append(paths(offered), paths(kept)...) {
		t.Errorf("listed %s, which a bundle inside an installed app owns", p)
	}
}

// Run for another user's home (sudo keeps HOME), lsof would list the
// wrong user's processes, so the rule refuses.
func TestAppLeftoversRefuseAnotherUsersHome(t *testing.T) {
	f := newLeftoversFixture(t)
	f.folder("Containers", "org.gone.app")
	f.a.getuid = func() int { return os.Getuid() + 1 }
	if offered, _, err := f.a.judge(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "another user") {
		t.Errorf("offered %v with err %v, want a refusal", paths(offered), err)
	}
}

// A process holding a file open in a folder keeps it.
func TestAppLeftoversKeepFoldersWithOpenFiles(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("no lsof")
	}
	f := newLeftoversFixture(t)
	dir := f.folder("Containers", "com.gone.sync")
	cmd := exec.Command("/bin/sh", "-c", `exec 3<"$1"; exec /bin/sleep 60`, "sh", filepath.Join(dir, "data.bin"))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	f.a.procs = lsofCwdAndMapped
	deadline := time.Now().Add(10 * time.Second)
	for {
		names, err := lsofCwdAndMapped(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(names, filepath.Join(dir, "data.bin")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lsof never listed the open file")
		}
		time.Sleep(50 * time.Millisecond)
	}
	offered, kept := f.judge()
	if slices.Contains(paths(offered), dir) {
		t.Errorf("offered %s, in which a process holds a file open", dir)
	}
	if why := keptWhy(kept, dir); !strings.Contains(why, "a running process") {
		t.Errorf("kept for %q, want the running process named", why)
	}
}

// regrow's own open files are its walks', no process's of the user.
func TestAppLeftoversIgnoreRegrowsOwnOpenFiles(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("no lsof")
	}
	f := newLeftoversFixture(t)
	dir := f.folder("Containers", "com.gone.self")
	held, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	f.a.procs = lsofCwdAndMapped
	offered, kept := f.judge()
	if !slices.Contains(paths(offered), dir) {
		t.Errorf("offered %v, kept for %q; want %s offered: only regrow holds it open", paths(offered), keptWhy(kept, dir), dir)
	}
}

// macOS's own folders in Application Support are named for a program
// or a framework of macOS, or are its iPhone backups; they are no app's
// leftovers and are not listed.
func TestAppLeftoversLeaveMacOSProductFoldersAlone(t *testing.T) {
	f := newLeftoversFixture(t)
	for _, d := range []string{"/System/Library/PrivateFrameworks/CallHistory.framework", "/System/Library/Frameworks/AddressBook.framework", "/System/Library/CoreServices/Dock.app"} {
		if err := os.MkdirAll(f.host.ExpandPath(d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.write("/usr/libexec/knowledge-agent", "")
	f.write("/usr/sbin/spindump", "")
	f.write("/usr/bin/python3", "")
	f.write("/System/Library/LaunchAgents/com.apple.homeenergyd.plist", launchdJob("com.apple.homeenergyd", "/System/Library/PrivateFrameworks/HomeEnergy.framework/Support/homeenergyd"))
	for _, n := range []string{"AddressBook", "CallHistoryTransactions", "Knowledge", "homeenergyd", "Dock", "spindump"} {
		f.folder("Application Support", n)
	}
	writeFile(t, filepath.Join(f.host.Home, "Library", "Application Support", "MobileSync", "Backup", "0000-0000", "Manifest.db"), 64)
	game := f.folder("Application Support", "Old Game")
	python := f.folder("Application Support", "Python")

	_, kept := f.judge()
	if got := paths(kept); !slices.Equal(got, []string{game, python}) {
		t.Errorf("kept rows %v, want only %s and %s", got, game, python)
	}
}

// A folder an installed app's sandbox exceptions name is in use.
func TestAppLeftoversEntitledPathsKeepFolders(t *testing.T) {
	f := newLeftoversFixture(t)
	app := f.app("/Applications/Alpha.app", "com.alpha.app")
	f.ents[app] = `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>com.apple.security.temporary-exception.files.home-relative-path.read-write</key><array><string>/Library/Application Support/org.shared.data/</string></array></dict></plist>`
	shared := f.folder("Application Support", "org.shared.data")
	f.folder("Containers", "org.shared.data")
	offered, kept := f.judge()
	if slices.Contains(paths(offered), shared) {
		t.Errorf("offered %s, which Alpha's entitlements name", shared)
	}
	if why := keptWhy(kept, shared); !strings.Contains(why, "Alpha.app") {
		t.Errorf("kept for %q, want Alpha's entitlements named", why)
	}
}

// What could not be read is never claimed absent: an unreadable
// ~/Library/Preferences is an error, and a location read in part is
// named where it might have held a sign of the app.
func TestAppLeftoversSayWhatCouldNotBeRead(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads every folder")
	}
	t.Run("preferences", func(t *testing.T) {
		f := newLeftoversFixture(t)
		f.folder("Containers", "org.gone.app")
		f.write("~/Library/Preferences/org.gone.app.plist", "<plist/>")
		chmod(t, f.host.ExpandPath("~/Library/Preferences"), 0)
		if offered, _, err := f.a.judge(context.Background(), ""); err == nil {
			t.Errorf("offered %v, want an error", paths(offered))
		}
	})
	t.Run("macOS's names", func(t *testing.T) {
		f := newLeftoversFixture(t)
		f.folder("Application Support", "Old Game")
		jobs := f.host.ExpandPath("/System/Library/LaunchAgents")
		if err := os.MkdirAll(jobs, 0o755); err != nil {
			t.Fatal(err)
		}
		chmod(t, jobs, 0)
		if _, kept, err := f.a.judge(context.Background(), ""); err == nil {
			t.Errorf("kept %v, want an error", paths(kept))
		}
	})
	t.Run("a system job", func(t *testing.T) {
		f := newLeftoversFixture(t)
		f.folder("Application Support", "homeenergyd")
		f.write("/System/Library/LaunchAgents/x.plist", launchdJob("com.apple.homeenergyd", "/usr/libexec/homeenergyd"))
		chmod(t, f.host.ExpandPath("/System/Library/LaunchAgents/x.plist"), 0)
		if _, kept, err := f.a.judge(context.Background(), ""); err == nil {
			t.Errorf("kept %v, want an error", paths(kept))
		}
	})
	t.Run("a system job that is no plist", func(t *testing.T) {
		f := newLeftoversFixture(t)
		f.folder("Application Support", "homeenergyd")
		f.write("/System/Library/LaunchAgents/x.plist", "not a plist")
		if _, kept, err := f.a.judge(context.Background(), ""); err == nil {
			t.Errorf("kept %v, want an error", paths(kept))
		}
	})
	t.Run("a sign's location", func(t *testing.T) {
		f := newLeftoversFixture(t)
		dir := f.folder("Application Support", "com.gone.app")
		f.folder("Containers", "com.gone.app")
		w, _ := blockingWalker(t, filepath.Join(f.host.Home, "Library", "Containers"))
		f.a.w = w
		_, kept := f.judge()
		if why := keptWhy(kept, dir); !strings.Contains(why, "Containers could not be read") {
			t.Errorf("kept for %q, want the unread location named", why)
		}
	})
}

// A folder of the same maker may be in a location that could not be
// read in full, so while one could not, no exact folder is offered.
func TestAppLeftoversUnreadLocationKeepsEveryApp(t *testing.T) {
	f := newLeftoversFixture(t)
	web := f.folder("HTTPStorages", "com.gone.app")
	saved := f.folder("Saved Application State", "com.gone.app.savedState")
	f.touch(filepath.Join(saved, "data.bin"), 1)
	w, _ := blockingWalker(t, filepath.Join(f.host.Home, "Library", "Saved Application State"))
	f.a.w = w
	offered, kept := f.judge()
	if got := paths(offered); len(got) > 0 {
		t.Errorf("offered %v while a location could not be read", got)
	}
	if why := keptWhy(kept, web); !strings.Contains(why, "Saved Application State could not be read") {
		t.Errorf("kept for %q, want the unread location named", why)
	}
}

// With no home folder there is nothing to judge, and that is an error.
func TestAppLeftoversNeedAHome(t *testing.T) {
	t.Setenv("HOME", "")
	if items, err := (&appLeftovers{}).Offered(context.Background()); err == nil {
		t.Errorf("offered %v, want an error", paths(items))
	}
}

// A path lsof would print escaped cannot be matched to a process's.
func TestAppLeftoversKeepPathsLsofEscapes(t *testing.T) {
	f := newLeftoversFixture(t)
	f.host.Home = filepath.Join(filepath.Dir(f.host.Home), "de\u200dv")
	f.a.host = f.host
	dir := f.folder("Containers", "org.gone.app")
	offered, kept := f.judge()
	if len(offered) > 0 {
		t.Errorf("offered %v, want none", paths(offered))
	}
	if why := keptWhy(kept, dir); !strings.Contains(why, "lsof would not print") {
		t.Errorf("kept for %q, want lsof's escaping named", why)
	}
}

// Entitlements that do not parse leave the groups unknown: an error.
func TestAppLeftoversUnparsableEntitlementsAreAnError(t *testing.T) {
	f := newLeftoversFixture(t)
	app := f.app("/Applications/Alpha.app", "com.alpha.app")
	f.ents[app] = "<?xml version=\"1.0\"?><plist><dict><key>x</key><array><string>y"
	f.folder("Containers", "org.gone.app")
	if offered, _, err := f.a.judge(context.Background(), ""); err == nil {
		t.Errorf("offered %v, want an error", paths(offered))
	}
}
