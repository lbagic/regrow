package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	for _, id := range []string{"com.alpha.app", "net.share.ext", "org.login.helper", "io.svc.xpc", "com.beta.app", "com.gamma.app", "com.delta.app", "com.phone.app", "dev.example.app"} {
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
	byPath := f.folder("Application Support", "com.gone.agent")
	byTilde := f.folder("Containers", "com.gone.tilde")
	byShell := f.folder("Caches", "com.gone.shell")
	f.folder("HTTPStorages", "com.gone.shell")
	byLabel := f.folder("Containers", "com.gone.daemon")
	free := f.folder("Containers", "com.gone.free")
	f.folder("Containers", "com.gone.agent")
	f.write("~/Library/LaunchAgents/com.gone.agent.plist", launchdJob("job.one", byPath+"/bin/run", "--flag"))
	f.write("~/Library/LaunchAgents/tilde.plist", launchdJob("job.two", "~/Library/Containers/com.gone.tilde/Data/run"))
	f.write("/Library/LaunchAgents/shell.plist", launchdJob("job.three", "/bin/sh", "-c", `cd $HOME/Library/Caches/com.gone.shell &amp;&amp; ./run`))
	f.write("/Library/LaunchDaemons/com.gone.daemon.plist", launchdJob("com.gone.daemon", "/usr/local/bin/daemon"))
	f.write("~/Library/LaunchAgents/near.plist", launchdJob("job.four", "~/Library/Containers/com.gone.free2/run"))

	offered, kept := f.judge()
	for _, p := range []string{byPath, byTilde, byShell, byLabel} {
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
		"/Library/Microsoft/Edge/NativeMessagingHosts",
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
	f.write("~/Library/Preferences/com.gone.daemon.plist", "<plist/>")
	cwd := f.folder("Containers", "com.gone.worker")
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
	f.write("~/Library/Preferences/net.gone.tool.plist", "<plist/>")
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
	want := []string{evidenced, idle, saved}
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
