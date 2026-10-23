package scanner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// App leftovers: the folders an app keeps under ~/Library, named after
// it, which stay behind when the app is deleted. Two rules read this
// provider. app-leftovers offers a folder named exactly by a bundle or
// group id that nothing installed has, once nothing in it changed for
// 90 days; app-leftovers-kept shows every other leftover it finds, each
// with why it is not offered, and offers none.
//
// A folder is owned, and not a leftover, when its id is the bundle id
// of anything in /Applications, ~/Applications or /System/Applications
// (an app, its plug-ins, helpers and login items: every Info.plist
// under them), or in an app Spotlight knows anywhere or a running app;
// or when an installed bundle's entitlements name it (its app groups).
// A folder that something still points into is kept: a launchd job, a
// browser's native messaging host, an installed app's entitlements or
// a running process. One maker's folders go together, since an app's
// groups and helpers have ids of their own: a change, a pointer or a
// read in part in any of them keeps them all, and so does a location
// that could not be read. Every failure to read an owner or a pointer
// leaves nothing offered.
//
// The app-leftover-recheck pre-action runs the same judgement on the
// one folder right before it goes to the Trash.

const (
	// appLeftoversIdle is how long a leftover must go unchanged.
	appLeftoversIdle = 90 * 24 * time.Hour
	// entitlementReaders bounds the codesign processes run at once.
	entitlementReaders = 8
	// maxLeftoverFile bounds an Info.plist, a launchd job or a native
	// messaging manifest read whole.
	maxLeftoverFile = 1 << 20
	// ownersReuse is how long rechecks reuse the installed apps one of
	// them read: reading them runs codesign on every bundle, seconds in
	// all, and a run rechecks each folder it moves.
	ownersReuse = time.Minute
)

// leftoverLocations hold one folder per app under ~/Library.
var leftoverLocations = []string{
	"Application Support", "Caches", "Containers", "Group Containers",
	"HTTPStorages", "WebKit", "Saved Application State", "Application Scripts",
}

// madeByMacOS are the locations whose folders macOS makes for a running
// process and names by its bundle or group id.
var madeByMacOS = map[string]bool{
	"Containers": true, "Group Containers": true, "HTTPStorages": true,
	"WebKit": true, "Saved Application State": true, "Application Scripts": true,
}

// appSigns are the locations whose folders macOS makes only for an app
// bundle of the id: a sandbox, a group, saved windows. A folder in
// Application Support or Caches is named by whatever made it, a
// command-line tool, a library or a game engine as often as an app, so
// its name is an app's id only when one of these has a folder of it. A
// settings file, web data and a cookie store are no sign: a tool with a
// bundle id makes them too, and a settings domain is any name a program
// gives (Unity's is unity.<company>.<product>, whatever the app's id).
var appSigns = []string{"Containers", "Group Containers", "Saved Application State", "Application Scripts"}

// settingsLocations hold the settings files apps keep under ~/Library,
// named by their ids: <id>.plist, and <id>.<host>.plist per host.
var settingsLocations = []string{"Preferences", "Preferences/ByHost"}

// markMakers are the makers a file beside the folders may be named for:
// those of its whole name, of the name less its extension (<id>.plist,
// <id>.binarycookies) and, per host, less its host label too. A file
// can only keep folders, so every reading counts and none needs an
// id's shape: a file with no extension, a ByHost file with no host
// label and a two-label domain (net.example.plist) each find their
// maker.
func markMakers(loc, name string) []string {
	readings := []string{name, strings.TrimSuffix(name, filepath.Ext(name))}
	if loc == "Preferences/ByHost" {
		readings = append(readings, strings.TrimSuffix(readings[1], filepath.Ext(readings[1])))
	}
	var makers []string
	for _, r := range readings {
		if m := vendorOf(r); m != "" && !slices.Contains(makers, m) {
			makers = append(makers, m)
		}
	}
	return makers
}

// appInstallRoots are where apps are installed.
var appInstallRoots = []string{"/Applications", "~/Applications", "/System/Applications"}

// launchdFolders hold the jobs launchd starts.
var launchdFolders = []string{"~/Library/LaunchAgents", "/Library/LaunchAgents", "/Library/LaunchDaemons"}

// nativeMessagingFolders hold the native messaging manifests of the
// Chromium browsers and Firefox, the user's and the system's.
var nativeMessagingFolders = []string{
	"~/Library/Application Support/Google/Chrome*/NativeMessagingHosts",
	"~/Library/Application Support/Chromium/NativeMessagingHosts",
	"~/Library/Application Support/Arc/User Data/NativeMessagingHosts",
	"~/Library/Application Support/Microsoft Edge*/NativeMessagingHosts",
	"~/Library/Application Support/BraveSoftware/Brave-Browser*/NativeMessagingHosts",
	"~/Library/Application Support/Vivaldi*/NativeMessagingHosts",
	"~/Library/Application Support/com.operasoftware.Opera*/NativeMessagingHosts",
	"~/Library/Application Support/Mozilla/NativeMessagingHosts",
	"/Library/Google/Chrome/NativeMessagingHosts",
	"/Library/Application Support/Chromium/NativeMessagingHosts",
	"/Library/Microsoft/Edge/NativeMessagingHosts",
	"/Library/BraveSoftware/Brave-Browser/NativeMessagingHosts",
	"/Library/Application Support/Mozilla/NativeMessagingHosts",
}

// codeBundleExts name the bundles that can carry entitlements.
var codeBundleExts = []string{".app", ".appex", ".xpc", ".systemextension"}

// leftoverFolder is one folder in a leftover location.
type leftoverFolder struct {
	loc, name, path string
	// id is the bundle or group id the name spells; "" for a name
	// that is no id.
	id string
}

// maker is the maker of the app the folder belongs to, by whose folders
// it goes: an app's groups and helpers are named by ids of their own
// (group.com.example, com.example.app.helper); "" for a name that is
// no id.
func (f leftoverFolder) maker() string {
	if f.id == "" {
		return ""
	}
	return vendorOf(f.id)
}

// label names the folder in a row.
func (f leftoverFolder) label() string {
	name := f.name
	if f.id != "" {
		name = f.id
	}
	return name + " · " + f.loc
}

// appLeftovers answers both queries. Tests hand in the walker, the host
// (Root re-anchors the system folders), the clock, Spotlight, codesign
// and the process list.
type appLeftovers struct {
	w    *walker
	host engine.Host
	idle time.Duration
	now  func() time.Time
	// spotlight lists the app bundles Spotlight knows, wherever they
	// are. It can only add owners.
	spotlight func(ctx context.Context) ([]string, error)
	// entitlements returns a bundle's entitlements as codesign prints
	// them, nothing for a bundle that has none.
	entitlements func(ctx context.Context, bundle string) ([]byte, error)
	// procs lists the executables, mapped files and working folders of
	// the user's running processes.
	procs func(ctx context.Context) ([]string, error)
	// memo, when set, holds the installed apps across rechecks.
	memo *ownersMemo
	// getuid is the user regrow runs as; nil is os.Getuid.
	getuid func() int

	mu      sync.Mutex
	done    bool
	offered []engine.Item
	kept    []engine.Item
	err     error
}

func newAppLeftovers(home string) *appLeftovers {
	return &appLeftovers{
		w:            defaultWalker,
		host:         engine.Host{Home: home},
		idle:         appLeftoversIdle,
		now:          time.Now,
		spotlight:    mdfindApps,
		entitlements: codesignEntitlements,
		procs:        lsofCwdAndMapped,
	}
}

// Offered is the app-leftovers query.
func (a *appLeftovers) Offered(ctx context.Context) ([]engine.Item, error) {
	a.load(ctx)
	return a.offered, a.err
}

// Kept is the app-leftovers-kept query.
func (a *appLeftovers) Kept(ctx context.Context) ([]engine.Item, error) {
	a.load(ctx)
	return a.kept, a.err
}

func (a *appLeftovers) load(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.done {
		return
	}
	a.done = true
	if a.host.Home == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			a.err = fmt.Errorf("the home folder is unknown, so its app leftovers cannot be judged: %w", err)
			return
		}
		fresh := newAppLeftovers(home)
		a.w, a.host, a.idle, a.now, a.spotlight, a.entitlements, a.procs = fresh.w, fresh.host, fresh.idle, fresh.now, fresh.spotlight, fresh.entitlements, fresh.procs
	}
	a.offered, a.kept, a.err = a.judge(ctx, "")
}

// RecheckAppLeftover is the app-leftovers pre-action: right before a
// folder goes to the Trash it judges the folder again and refuses unless
// it is still offered. The scan may be hours old; the app may have been
// installed again since, or the folder used.
func RecheckAppLeftover(ctx context.Context, path string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("recheck %s: %w", path, err)
	}
	// The run's context has no deadline of its own.
	ctx, cancel := context.WithTimeout(ctx, toolQueryTimeout)
	defer cancel()
	a := newAppLeftovers(home)
	a.memo = recheckOwners
	return a.recheck(ctx, path)
}

// ownersMemo is the installed apps one reading found, and when.
type ownersMemo struct {
	mu   sync.Mutex
	home string
	at   time.Time
	in   *installedApps
	// unread holds the code bundles running then that the reading did
	// not read, each with whether it had an Info.plist then.
	unread map[string]bool
}

var recheckOwners = &ownersMemo{}

// unread is each running code bundle in does not hold, with whether an
// app on its path, or the bundle, has an Info.plist.
func (a *appLeftovers) unread(ctx context.Context, in *installedApps, running []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, b := range running {
		if _, read := in.bundles[b]; read {
			continue
		}
		_, ok, err := a.outermost(ctx, b)
		if err != nil {
			return nil, err
		}
		out[b] = ok
	}
	return out, nil
}

// saw reports that the memo's reading read every code bundle running
// now, or saw it running with or without an Info.plist as it is now.
// Otherwise an app was installed and started since, anywhere, or where
// a bundle without one ran: the apps are read again.
func (a *appLeftovers) saw(ctx context.Context, running []string) (bool, error) {
	now, err := a.unread(ctx, a.memo.in, running)
	if err != nil {
		return false, err
	}
	for b, described := range now {
		if then, seen := a.memo.unread[b]; !seen || then != described {
			return false, nil
		}
	}
	return true, nil
}

// owners reads the installed apps, or takes them from the memo while
// its reading is younger than ownersReuse.
func (a *appLeftovers) owners(ctx context.Context, procs []string) (*installedApps, error) {
	if a.memo == nil {
		return a.installed(ctx, procs)
	}
	a.memo.mu.Lock()
	defer a.memo.mu.Unlock()
	now := a.now()
	running := runningBundles(procs)
	if age := now.Sub(a.memo.at); a.memo.in != nil && a.memo.home == a.host.Home && age >= 0 && age < ownersReuse {
		saw, err := a.saw(ctx, running)
		if err != nil {
			return nil, err
		}
		if saw {
			return a.memo.in, nil
		}
	}
	in, err := a.installed(ctx, procs)
	if err != nil {
		return nil, err
	}
	unread, err := a.unread(ctx, in, running)
	if err != nil {
		return nil, err
	}
	a.memo.home, a.memo.at, a.memo.in, a.memo.unread = a.host.Home, now, in, unread
	return in, nil
}

func (a *appLeftovers) recheck(ctx context.Context, path string) error {
	offered, kept, err := a.judge(ctx, path)
	if err != nil {
		return fmt.Errorf("recheck %s: %w", path, err)
	}
	for _, it := range offered {
		if it.Path == path {
			return nil
		}
	}
	for _, it := range kept {
		if it.Path == path {
			return fmt.Errorf("%s is kept now: %s", path, it.Label)
		}
	}
	return fmt.Errorf("%s is no leftover now: it is gone, or an installed app has its name", path)
}

// judge decides every folder in the leftover locations, or, for the
// recheck, the folder at only and the other folders of its maker: the
// items offered, then the items kept. An owner or a pointer that
// cannot be read is an error.
func (a *appLeftovers) judge(ctx context.Context, only string) (offered, kept []engine.Item, err error) {
	folders, j, blocked, err := a.folders(ctx, only)
	if err != nil || len(folders) == 0 {
		return blocked, nil, err
	}
	if err := a.sameUser(ctx); err != nil {
		return nil, nil, err
	}
	procs, err := a.procs(ctx)
	if err != nil {
		return nil, nil, err
	}
	owners, err := a.owners(ctx, procs)
	if err != nil {
		return nil, nil, err
	}
	ptrs, err := a.pointers(ctx, procs, owners)
	if err != nil {
		return nil, nil, err
	}
	j.in, j.ptrs, j.recheck = owners, ptrs, only != ""
	if only == "" {
		if j.system, err = a.systemNames(ctx); err != nil {
			return nil, nil, err
		}
	}
	var looks []look
	for _, f := range folders {
		l, ok, err := a.look(ctx, f, j)
		if err != nil {
			return nil, nil, err
		}
		if ok {
			looks = append(looks, l)
		}
	}
	// inUse is, per maker, why the first of its folders in use is.
	inUse := map[string]string{}
	for _, l := range looks {
		if maker := l.f.maker(); maker != "" && l.use != "" && inUse[maker] == "" {
			inUse[maker] = tilde(a.host, l.f.path) + ", of the same maker: " + l.use
		}
	}
	// A folder of the same maker may be where regrow could not read.
	var unreadAt string
	for _, loc := range leftoverLocations {
		if j.unread[loc] {
			unreadAt = "~/Library/" + loc + " could not be read in full, so a folder of the same maker there is unknown"
			break
		}
	}
	unread := map[string]bool{}
	for _, l := range looks {
		switch {
		case l.exact:
			why := l.use
			if why == "" {
				why = inUse[l.f.maker()]
			}
			if why == "" {
				why = unreadAt
			}
			if why == "" {
				offered = append(offered, l.item)
				continue
			}
			l.item.Label += " — " + why
			kept = append(kept, l.item)
		case l.listed:
			kept = append(kept, l.item)
		case l.item.Partial:
			unread[l.f.loc] = true
		}
	}
	offered = append(offered, blocked...)
	for _, loc := range leftoverLocations {
		if unread[loc] {
			kept = append(kept, unreadableMarker("folders under ~/Library/"+loc))
		}
	}
	return offered, kept, nil
}

// judging is what every folder is judged against.
type judging struct {
	// unread holds the locations not read in full.
	unread map[string]bool
	// signed holds the coreIDs of the folders macOS made only for an
	// app.
	signed map[string]bool
	// marks holds, per maker, the files named by its ids beside the
	// folders: settings in ~/Library/Preferences and its ByHost folder,
	// cookie stores in HTTPStorages; marked, per maker, why one of them
	// changed lately ("" when none did), once asked.
	marks  map[string][]string
	marked map[string]string
	in     *installedApps
	ptrs   *pointers
	// system holds the nameKeys of macOS's own programs and frameworks;
	// nil in a recheck, which judges no product names.
	system map[string]bool
	// recheck is set when the scan offered the folder: what showed it
	// is an app's may be gone since, moved by the same run.
	recheck bool
}

// sameUser refuses a home another user owns. lsof lists the processes
// of the user regrow runs as, so under sudo with the home kept, the
// owner's running processes, and the apps among them, would go unseen.
func (a *appLeftovers) sameUser(ctx context.Context) error {
	uid := os.Getuid()
	if a.getuid != nil {
		uid = a.getuid()
	}
	library := filepath.Join(a.host.Home, "Library")
	fi, err := a.w.stat(ctx, library)
	if err != nil {
		return fmt.Errorf("%s: %w", library, err)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != uid {
		return fmt.Errorf("%s belongs to another user than the one regrow runs as, whose running processes it cannot list", library)
	}
	return nil
}

// folders lists the leftover locations: every folder that could be a
// leftover (not macOS's own, not one whose name says nothing), or, for
// only, it and the other folders of its maker; the files of each
// maker beside them; the ids macOS made app folders for; and a marker
// for each location that could not be read in full.
func (a *appLeftovers) folders(ctx context.Context, only string) ([]leftoverFolder, *judging, []engine.Item, error) {
	j := &judging{unread: map[string]bool{}, signed: map[string]bool{}, marks: map[string][]string{}, marked: map[string]string{}}
	var folders []leftoverFolder
	var blocked []engine.Item
	library := filepath.Join(a.host.Home, "Library")
	for _, loc := range append(slices.Clone(settingsLocations), leftoverLocations...) {
		dir := filepath.Join(library, loc)
		settings := slices.Contains(settingsLocations, loc)
		if _, err := a.w.lstat(ctx, dir); absent(err) {
			continue
		}
		entries, partial, err := a.w.list(ctx, dir)
		if err != nil {
			return nil, nil, nil, err
		}
		if partial {
			if settings {
				return nil, nil, nil, fmt.Errorf("%s could not be read in full, so when an app last changed its settings is unknown", dir)
			}
			blocked = append(blocked, unreadableMarker("~/Library/"+loc))
			j.unread[loc] = true
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if !e.IsDir() {
				for _, maker := range markMakers(loc, e.Name()) {
					j.marks[maker] = append(j.marks[maker], filepath.Join(dir, e.Name()))
				}
				continue
			}
			if settings {
				continue
			}
			f := leftoverFolder{loc: loc, name: e.Name(), path: filepath.Join(dir, e.Name()), id: leftoverID(loc, e.Name())}
			if f.id != "" && slices.Contains(appSigns, loc) {
				j.signed[coreID(f.id)] = true
			}
			if (f.id == "" && loc != "Application Support") || (f.id != "" && appleID(f.id)) {
				continue
			}
			folders = append(folders, f)
		}
	}
	slices.SortFunc(folders, func(x, y leftoverFolder) int { return strings.Compare(x.path, y.path) })
	if only != "" {
		i := slices.IndexFunc(folders, func(f leftoverFolder) bool { return f.path == only })
		switch {
		case i < 0:
			folders = nil
		case folders[i].maker() == "":
			folders = folders[i : i+1]
		default:
			maker := folders[i].maker()
			folders = slices.DeleteFunc(folders, func(f leftoverFolder) bool { return f.maker() != maker })
		}
	}
	return folders, j, blocked, nil
}

// leftoverID returns the bundle or group id a folder's name spells in
// its location, "" when it spells none. Saved Application State names
// a folder <id>.savedState.
func leftoverID(loc, name string) string {
	if loc == "Saved Application State" {
		id, ok := strings.CutSuffix(name, ".savedState")
		if !ok {
			return ""
		}
		name = id
	}
	if !idShaped(name) {
		return ""
	}
	return name
}

// idShaped reports a reverse-DNS name of three or more labels made of
// what a bundle id may hold: letters, digits and hyphens.
func idShaped(s string) bool {
	labels := strings.Split(s, ".")
	if len(labels) < 3 {
		return false
	}
	for _, l := range labels {
		if l == "" || strings.ContainsFunc(l, func(r rune) bool { return !idRune(r) }) {
			return false
		}
	}
	return true
}

func idRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-'
}

// idCore strips what a group id adds in front of the id it is built
// from: a team id (UBF8T346G9.com.example) and a label that ends in
// "group" or "groups" (group.com.example, 243LU875E5.groups.com.apple.x).
func idCore(id string) []string {
	labels := strings.Split(foldName(id), ".")
	if len(labels) > 2 && teamID(labels[0]) {
		labels = labels[1:]
	}
	if len(labels) > 2 && (strings.HasSuffix(labels[0], "group") || strings.HasSuffix(labels[0], "groups")) {
		labels = labels[1:]
	}
	return labels
}

func teamID(label string) bool {
	return len(label) == 10 && !strings.ContainsFunc(label, func(r rune) bool { return r == '-' || !idRune(r) })
}

// appleID reports an id of macOS's own: one with com.apple among its
// labels, which a group id built on one has after its prefixes. Those
// folders belong to the system, installed or not.
func appleID(id string) bool {
	labels := strings.Split(foldName(id), ".")
	for i := 0; i+2 < len(labels); i++ {
		if labels[i] == "com" && labels[i+1] == "apple" {
			return true
		}
	}
	return false
}

// vendorOf is an id's first two labels, the maker's domain: com.docker
// for com.docker.docker and for group.com.docker.
func vendorOf(id string) string {
	labels := idCore(id)
	if len(labels) < 2 {
		return ""
	}
	return labels[0] + "." + labels[1]
}

// installedApps is what the installed bundles say about their owners.
// Each value is the installed app a row names: the outermost app the
// bundle sits in.
type installedApps struct {
	// ids are the bundle ids of every Info.plist, folded.
	ids map[string]string
	// vendors are the first two labels of the apps' ids.
	vendors map[string]string
	// names are the nameKeys of the apps' names (the bundle's file
	// name, CFBundleName, CFBundleDisplayName, CFBundleExecutable),
	// and of their makers' (google for com.google).
	names map[string]appName
	// products are the nameKeys of the outermost apps' names only, not
	// of the helpers inside them ("Browser Helper").
	products map[string]string
	// entitled are the strings of every code bundle's entitlements,
	// folded: app groups, keychain groups, the application identifier.
	entitled map[string]string
	// bundles are the code bundles read, each with the app a row names.
	bundles map[string]string
}

type appName struct {
	app   string
	maker bool
}

func newInstalledApps() *installedApps {
	return &installedApps{ids: map[string]string{}, vendors: map[string]string{}, names: map[string]appName{}, products: map[string]string{}, entitled: map[string]string{}, bundles: map[string]string{}}
}

func setOnce[V any](m map[string]V, k string, v V) {
	if _, seen := m[k]; !seen && k != "" {
		m[k] = v
	}
}

// add records what an Info.plist says, and returns the code bundle it
// describes, "" for none. described holds the bundles the plists read
// with it describe, for the app a row names.
func (in *installedApps) add(plist string, info map[string]string, described map[string]bool) string {
	id := info["CFBundleIdentifier"]
	if id == "" {
		return ""
	}
	app := appOf(plist, described)
	setOnce(in.ids, foldName(id), app)
	bundle := bundleOf(plist)
	if bundle == "" {
		return ""
	}
	in.bundles[bundle] = app
	if !strings.EqualFold(filepath.Ext(bundle), ".app") {
		return bundle
	}
	if v := vendorOf(id); v != "" {
		setOnce(in.vendors, v, app)
		setOnce(in.names, nameKey(strings.SplitN(v, ".", 2)[1]), appName{app: app, maker: true})
	}
	base := filepath.Base(bundle)
	for _, n := range []string{strings.TrimSuffix(base, filepath.Ext(base)), info["CFBundleName"], info["CFBundleDisplayName"], info["CFBundleExecutable"]} {
		setOnce(in.names, nameKey(n), appName{app: app})
		if bundle == app {
			setOnce(in.products, nameKey(n), app)
		}
	}
	return bundle
}

// readInfo reads one Info.plist into in, and returns the code bundle it
// describes, "" for none.
func (a *appLeftovers) readInfo(ctx context.Context, in *installedApps, p string, described map[string]bool) (string, error) {
	data, err := bounded(ctx, a.w, p, func() ([]byte, error) { return readLimited(p, maxLeftoverFile) })
	if err != nil {
		return "", fmt.Errorf("%s cannot be read, so the bundle it describes cannot be ruled out as an owner: %w", p, err)
	}
	info, err := plistTopStrings(data)
	if err != nil {
		return "", fmt.Errorf("%s: %w", p, err)
	}
	return in.add(p, info, described), nil
}

// describedBy is the set of bundles some of plists describe.
func describedBy(plists []string) map[string]bool {
	described := map[string]bool{}
	for _, p := range plists {
		if b := bundleOf(p); b != "" {
			described[b] = true
		}
	}
	return described
}

// installed reads every bundle under the app roots, the apps Spotlight
// knows and the running apps.
func (a *appLeftovers) installed(ctx context.Context, procs []string) (*installedApps, error) {
	plists, err := a.infoPlists(ctx)
	if err != nil {
		return nil, err
	}
	in := newInstalledApps()
	var code []string
	described := describedBy(plists)
	for _, p := range plists {
		b, err := a.readInfo(ctx, in, p, described)
		if err != nil {
			return nil, err
		}
		if b != "" {
			code = append(code, b)
		}
	}
	var extra []string
	if a.spotlight != nil {
		apps, err := a.spotlight(ctx)
		if err != nil {
			return nil, err
		}
		extra = append(extra, apps...)
	}
	apps, err := a.appBundles(ctx, append(extra, runningBundles(procs)...))
	if err != nil {
		return nil, err
	}
	more, err := a.readBundles(ctx, in, apps)
	if err != nil {
		return nil, err
	}
	code = append(code, more...)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := a.readEntitlements(ctx, code, in.entitled); err != nil {
		return nil, err
	}
	return in, nil
}

// appBundles turns the bundles Spotlight knows or that run into the
// apps to read, sorted: each one's outermost app that has an Info.plist
// (a login item's app), or itself, less those without an Info.plist
// (Spotlight lists apps deleted and folders named like apps; a checkout
// named site.app is no app), those inside an app root, which the
// roots' walk read, and those inside another of them.
func (a *appLeftovers) appBundles(ctx context.Context, bundles []string) ([]string, error) {
	var roots []string
	for _, r := range appInstallRoots {
		roots = append(roots, a.host.ExpandPath(r))
	}
	var apps []string
	for _, b := range bundles {
		if slices.ContainsFunc(roots, func(root string) bool { return strings.HasPrefix(b, root+string(filepath.Separator)) }) {
			continue
		}
		app, ok, err := a.outermost(ctx, b)
		if err != nil {
			return nil, err
		}
		if ok {
			apps = append(apps, app)
		}
	}
	slices.Sort(apps)
	var outer []string
	for _, b := range slices.Compact(apps) {
		if !slices.ContainsFunc(outer, func(dir string) bool { return strings.HasPrefix(b, dir+string(filepath.Separator)) }) {
			outer = append(outer, b)
		}
	}
	return outer, nil
}

// outermost is the first app on a bundle's path that has an Info.plist,
// or the bundle itself when none has; ok is false when the bundle has
// none either.
func (a *appLeftovers) outermost(ctx context.Context, bundle string) (app string, ok bool, err error) {
	parts := strings.Split(bundle, string(filepath.Separator))
	for i, p := range parts {
		if !strings.EqualFold(filepath.Ext(p), ".app") {
			continue
		}
		app := strings.Join(parts[:i+1], string(filepath.Separator))
		if ok, err := a.describes(ctx, app); err != nil || ok {
			return app, ok, err
		}
	}
	if strings.EqualFold(filepath.Ext(bundle), ".app") {
		return bundle, false, nil
	}
	ok, err = a.describes(ctx, bundle)
	return bundle, ok, err
}

// readBundles reads each app whole into in, and returns the code
// bundles read. One with a folder that cannot be read, or an Info.plist
// that cannot be read or does not parse, is an error: it is there, and
// what it owns is unknown.
func (a *appLeftovers) readBundles(ctx context.Context, in *installedApps, apps []string) ([]string, error) {
	var code []string
	for _, b := range apps {
		nested, err := a.bundlePlists(ctx, b)
		if err != nil {
			return nil, err
		}
		described := describedBy(nested)
		for _, p := range nested {
			c, err := a.readInfo(ctx, in, p, described)
			if err != nil {
				return nil, err
			}
			if c != "" {
				code = append(code, c)
			}
		}
	}
	return code, nil
}

// describes reports a bundle with an Info.plist.
func (a *appLeftovers) describes(ctx context.Context, bundle string) (bool, error) {
	for _, p := range []string{filepath.Join(bundle, "Contents", "Info.plist"), filepath.Join(bundle, "Info.plist")} {
		data, err := bounded(ctx, a.w, p, func() ([]byte, error) { return readLimited(p, maxLeftoverFile) })
		switch {
		case absent(err):
			continue
		case err != nil:
			return false, fmt.Errorf("%s cannot be read, so the app it describes cannot be ruled out as an owner: %w", p, err)
		}
		if _, err := plistTopStrings(data); err != nil {
			return false, fmt.Errorf("%s: %w", p, err)
		}
		return true, nil
	}
	return false, nil
}

// bundlePlists returns every Info.plist in a bundle. A bundle that
// cannot be read in full is an error: a bundle inside it cannot be
// ruled out as an owner.
func (a *appLeftovers) bundlePlists(ctx context.Context, bundle string) ([]string, error) {
	var mu sync.Mutex
	var plists []string
	partial, err := a.w.walk(ctx, bundle, func(dir string, batch []fs.DirEntry) ([]string, bool) {
		var sub []string
		for _, e := range batch {
			p := childPath(dir, e.Name())
			switch {
			case e.IsDir():
				sub = append(sub, p)
			case strings.EqualFold(e.Name(), "Info.plist"):
				mu.Lock()
				plists = append(plists, p)
				mu.Unlock()
			}
		}
		return sub, true
	})
	if err != nil {
		return nil, err
	}
	if partial {
		return nil, fmt.Errorf("%s could not be read in full, so a bundle inside it cannot be ruled out as an owner", bundle)
	}
	slices.Sort(plists)
	return plists, nil
}

// bundleOf is the code bundle an Info.plist describes, "" when it
// describes none: X.app for X.app/Contents/Info.plist, and for
// X.app/Info.plist, the shape of an iOS app's wrapped bundle.
func bundleOf(plist string) string {
	dir := filepath.Dir(plist)
	if filepath.Base(dir) == "Contents" {
		dir = filepath.Dir(dir)
	}
	if !hasExt(dir, codeBundleExts) {
		return ""
	}
	return dir
}

// appOf is the app an Info.plist belongs to, which a row names: the
// first app on its path that one of described is (/Applications/X.app
// for anything inside it; not a checkout named site.app above it), or
// the code bundle it describes outside one (a framework's service, a
// plug-in in a plain folder), else the folder holding it.
func appOf(plist string, described map[string]bool) string {
	parts := strings.Split(plist, string(filepath.Separator))
	for i, p := range parts {
		if app := strings.Join(parts[:i+1], string(filepath.Separator)); strings.EqualFold(filepath.Ext(p), ".app") && described[app] {
			return app
		}
	}
	if b := bundleOf(plist); b != "" {
		return b
	}
	return filepath.Dir(plist)
}

// infoPlists walks the app roots and returns every Info.plist under
// them. A link in a root, or in a folder directly in one that is no
// bundle (/Applications/Utilities), is followed one hop. A root or a
// folder under it that cannot be read, or a link there that cannot be
// followed, is an error: an app behind it cannot be ruled out.
func (a *appLeftovers) infoPlists(ctx context.Context) ([]string, error) {
	var mu sync.Mutex
	var plists []string
	for _, r := range appInstallRoots {
		root := a.host.ExpandPath(r)
		if _, err := a.w.lstat(ctx, root); absent(err) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("%s cannot be read, so an app installed there cannot be ruled out: %w", root, err)
		}
		walks := []string{root}
		for i := 0; i < len(walks); i++ {
			top := walks[i]
			var links []string
			partial, err := a.w.walk(ctx, top, func(dir string, batch []fs.DirEntry) ([]string, bool) {
				var sub []string
				for _, e := range batch {
					p := childPath(dir, e.Name())
					switch {
					case e.IsDir():
						sub = append(sub, p)
					case e.Type()&fs.ModeSymlink != 0 && i == 0 && (dir == top || filepath.Dir(dir) == top && !hasExt(dir, bundleExts)):
						mu.Lock()
						links = append(links, p)
						mu.Unlock()
					case strings.EqualFold(e.Name(), "Info.plist"):
						mu.Lock()
						plists = append(plists, p)
						mu.Unlock()
					}
				}
				return sub, true
			})
			if err != nil {
				return nil, err
			}
			if partial {
				return nil, fmt.Errorf("%s could not be read in full, so an app installed there cannot be ruled out", top)
			}
			mu.Lock()
			found := slices.Sorted(slices.Values(links))
			mu.Unlock()
			for _, l := range found {
				folder, unknown := linkedFolder(ctx, a.w, l)
				if unknown {
					return nil, fmt.Errorf("%s cannot be followed to a folder, so an app behind it cannot be ruled out", l)
				}
				if folder {
					walks = append(walks, l)
				}
			}
		}
	}
	slices.Sort(plists)
	return plists, nil
}

// bundleExts name folders that are bundles, whose links are their own.
var bundleExts = append([]string{".bundle", ".framework", ".plugin"}, codeBundleExts...)

func hasExt(path string, exts []string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return slices.Contains(exts, ext)
}

// runningBundle is the code bundle a running executable sits in
// (/Applications/Foo.app for /Applications/Foo.app/Contents/MacOS/Foo),
// "" for any other path.
func runningBundle(path string) string {
	dir := filepath.Dir(path)
	if filepath.Base(dir) != "MacOS" || filepath.Base(filepath.Dir(dir)) != "Contents" {
		return ""
	}
	bundle := filepath.Dir(filepath.Dir(dir))
	if !hasExt(bundle, codeBundleExts) {
		return ""
	}
	return bundle
}

// runningBundles are the code bundles of the running executables,
// sorted.
func runningBundles(procs []string) []string {
	var out []string
	for _, p := range procs {
		if b := runningBundle(p); b != "" {
			out = append(out, b)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// readEntitlements adds every string of each bundle's entitlements to
// entitled. A bundle codesign cannot read for any reason but having no
// signature is an error: the groups it may own are unknown.
func (a *appLeftovers) readEntitlements(ctx context.Context, bundles []string, entitled map[string]string) error {
	slices.Sort(bundles)
	bundles = slices.Compact(bundles)
	type result struct {
		texts []string
		err   error
	}
	results := make([]result, len(bundles))
	sem := make(chan struct{}, entitlementReaders)
	var wg sync.WaitGroup
	for i, b := range bundles {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out, err := a.entitlements(ctx, b)
			if err != nil || len(bytes.TrimSpace(out)) == 0 {
				results[i] = result{err: err}
				return
			}
			texts, err := plistTexts(out)
			if err != nil {
				err = fmt.Errorf("the entitlements of %s: %w", b, err)
			}
			results[i] = result{texts: texts, err: err}
		}()
	}
	wg.Wait()
	for i, r := range results {
		if r.err != nil {
			return r.err
		}
		for _, s := range r.texts {
			if _, seen := entitled[foldName(s)]; !seen {
				entitled[foldName(s)] = bundles[i]
			}
		}
	}
	return ctx.Err()
}

// codesignEntitlements reads a bundle's entitlements with codesign, as
// XML. A bundle that is not signed, or that codesign does not take for
// code (a folder named like an app that holds none), has none.
func codesignEntitlements(ctx context.Context, bundle string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/codesign", "-d", "--entitlements", "-", "--xml", bundle)
	cmd.WaitDelay = toolWaitDelay
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	msg := stderr.String()
	if strings.Contains(msg, "code object is not signed at all") || strings.Contains(msg, "bundle format unrecognized, invalid, or unsuitable") {
		return nil, nil
	}
	return nil, fmt.Errorf("codesign could not read the entitlements of %s, so the app groups it owns are unknown: %s", bundle, strings.TrimSpace(lastLine(msg)))
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	return s[strings.LastIndexByte(s, '\n')+1:]
}

// mdfindApps lists the app bundles Spotlight knows. Spotlight off, or
// mdfind missing, finds none; mdfind failing is an error.
func mdfindApps(ctx context.Context) ([]string, error) {
	out, ok, err := runTool(ctx, "mdfind", "kMDItemContentTypeTree == 'com.apple.application-bundle'")
	if err != nil {
		return nil, fmt.Errorf("mdfind could not list the apps Spotlight knows, so an app outside the Applications folders cannot be ruled out as an owner: %w", err)
	}
	if !ok {
		return nil, nil
	}
	var paths []string
	for line := range strings.SplitSeq(string(out), "\n") {
		if filepath.IsAbs(line) {
			paths = append(paths, line)
		}
	}
	return paths, nil
}

// lsofCwdAndMapped lists the working folders, executables, mapped and
// open files of the user's processes.
func lsofCwdAndMapped(ctx context.Context) ([]string, error) {
	out, err := processTool(ctx, "lsof", "-a", "-u", strconv.Itoa(os.Getuid()), "-F", "pfn")
	if err != nil {
		return nil, err
	}
	return othersFiles(string(out), os.Getpid())
}

// othersFiles reads the file names of `lsof -F pfn` output taken by the
// process self, less self's open files: its walks hold folders open.
// Its working folder and program stay. A listing without self is not a
// listing of the user's processes.
func othersFiles(out string, self int) ([]string, error) {
	var names []string
	listed, mine, fd := false, false, ""
	for line := range strings.SplitSeq(out, "\n") {
		switch {
		case strings.HasPrefix(line, "p"):
			mine = line == "p"+strconv.Itoa(self)
			listed = listed || mine
		case strings.HasPrefix(line, "f"):
			fd = line[1:]
		case strings.HasPrefix(line, "n") && len(line) > 1:
			if mine && fd != "cwd" && fd != "txt" {
				continue
			}
			names = append(names, line[1:])
		}
	}
	if !listed {
		return nil, errors.New("lsof did not list regrow's own process, so its answer cannot be trusted")
	}
	return names, nil
}

// pointers is every string that can point into a leftover: each launchd
// job's and native messaging manifest's strings and where the file
// links to, each installed bundle's entitlements and each running
// process's paths, folded.
type pointers struct {
	// whole maps a string to where it comes from, to find one that
	// names a folder whole (a job labelled with its id).
	whole map[string]string
	// into holds, per folded location, what follows
	// "/library/<location>/" in a string, with where it comes from.
	into map[string][]pointerTail
}

type pointerTail struct{ rest, from string }

// unquote undoes what a shell line wraps a path in: an escaped space,
// and quotes, which may close mid-path ("$HOME/Library/Caches"/x).
var unquote = strings.NewReplacer(`\ `, " ", `"`, "", "'", "")

func (p *pointers) add(text, from string) {
	t := foldName(unquote.Replace(text))
	if _, seen := p.whole[t]; !seen {
		p.whole[t] = from
	}
	for _, loc := range leftoverLocations {
		key := foldName(loc)
		needle := "/library/" + key + "/"
		for rest := t; ; {
			i := strings.Index(rest, needle)
			if i < 0 {
				break
			}
			rest = rest[i+len(needle):]
			p.into[key] = append(p.into[key], pointerTail{rest: rest, from: from})
		}
	}
}

// at returns where a string pointing into the folder comes from, "" when
// none does.
func (p *pointers) at(f leftoverFolder) string {
	for _, n := range []string{f.name, f.id} {
		if from, ok := p.whole[foldName(n)]; ok && n != "" {
			return from + " names it"
		}
	}
	name := foldName(f.name)
	for _, t := range p.into[foldName(f.loc)] {
		if rest, ok := strings.CutPrefix(t.rest, name); ok && pathEnds(rest) {
			return t.from + " points into it"
		}
	}
	return ""
}

// pathEnds reports that a folder's name ends where the string's path
// component does: at the end, a slash, or anything a name in a reverse
// DNS id or a product folder's name runs on with.
func pathEnds(rest string) bool {
	if rest == "" {
		return true
	}
	c := rest[0]
	return !idRune(rune(c)) && c != '.' && c != '_' && c < 0x80
}

// pointers reads every launchd job and native messaging manifest. One
// that cannot be read is an error: what it points to is unknown. An
// installed bundle's entitlements point too: a sandbox exception names
// a folder outside the app's container.
func (a *appLeftovers) pointers(ctx context.Context, procs []string, in *installedApps) (*pointers, error) {
	ptrs := &pointers{whole: map[string]string{}, into: map[string][]pointerTail{}}
	for _, p := range procs {
		ptrs.add(p, "a running process")
	}
	for _, s := range slices.Sorted(maps.Keys(in.entitled)) {
		ptrs.add(s, "an entitlement of "+filepath.Base(in.bundles[in.entitled[s]]))
	}
	read := func(dir, ext, kind string, parse func([]byte) ([]string, error)) error {
		entries, partial, err := a.w.list(ctx, dir)
		if err != nil {
			return err
		}
		if partial {
			return fmt.Errorf("%s could not be read in full, so what its %ss point to is unknown", dir, kind)
		}
		for _, e := range entries {
			if !strings.EqualFold(filepath.Ext(e.Name()), ext) {
				continue
			}
			p := filepath.Join(dir, e.Name())
			from := kind + " " + tilde(a.host, p)
			if e.Type()&fs.ModeSymlink != 0 {
				target, err := bounded(ctx, a.w, p, func() (string, error) { return os.Readlink(p) })
				if err != nil {
					return fmt.Errorf("%s cannot be followed, so what it points to is unknown: %w", p, err)
				}
				if !filepath.IsAbs(target) {
					target = filepath.Join(dir, target)
				}
				ptrs.add(target, from)
				if resolved, err := bounded(ctx, a.w, p, func() (string, error) { return filepath.EvalSymlinks(p) }); err == nil {
					ptrs.add(resolved, from)
				}
			}
			fi, err := a.w.stat(ctx, p)
			switch {
			case absent(err):
				continue
			case err != nil:
				return fmt.Errorf("%s cannot be read, so what it points to is unknown: %w", p, err)
			case !fi.Mode().IsRegular():
				continue
			}
			data, err := bounded(ctx, a.w, p, func() ([]byte, error) { return readLimited(p, maxLeftoverFile) })
			if err != nil {
				return fmt.Errorf("%s cannot be read, so what it points to is unknown: %w", p, err)
			}
			texts, err := parse(data)
			if err != nil {
				return fmt.Errorf("%s: %w", p, err)
			}
			for _, s := range texts {
				ptrs.add(s, from)
			}
		}
		return nil
	}
	for _, d := range launchdFolders {
		dir := a.host.ExpandPath(d)
		if _, err := a.w.lstat(ctx, dir); absent(err) {
			continue
		}
		if err := read(dir, ".plist", "the launchd job", plistTexts); err != nil {
			return nil, err
		}
	}
	for _, pattern := range nativeMessagingFolders {
		dirs, partial := a.w.glob(ctx, a.host.ExpandPath(pattern))
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if partial {
			return nil, fmt.Errorf("the folders %s cannot all be read, so what their native messaging hosts point to is unknown", pattern)
		}
		for _, dir := range dirs {
			if _, err := a.w.lstat(ctx, dir); absent(err) {
				continue
			}
			if err := read(dir, ".json", "the native messaging host", jsonTexts); err != nil {
				return nil, err
			}
		}
	}
	return ptrs, ctx.Err()
}

// jsonTexts returns every string of a JSON document, keys included. A
// file that is not JSON is read as one string: a browser skips such a
// manifest, and a path in it still keeps its folder.
func jsonTexts(data []byte) ([]string, error) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return []string{string(data)}, nil
	}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			out = append(out, t)
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			for k, e := range t {
				out = append(out, k)
				walk(e)
			}
		}
	}
	walk(v)
	return out, nil
}

// look is one folder judged on its own.
type look struct {
	f    leftoverFolder
	item engine.Item
	// exact is set for a leftover by its exact id; listed for a fuzzy
	// match the kept rule shows, its reason in the label.
	exact, listed bool
	// use is why the folder is in use or cannot be judged: a pointer, a
	// read in part, a recent change; "" when nothing says so.
	use string
}

// look judges one folder; ok is false for one owned, or gone. A fuzzy
// match is listed once read in full and unchanged for 90 days: one that
// changed lately is most likely a live program's. Its only errors are
// ctx's and a file of the maker beside the folders that cannot be read.
func (a *appLeftovers) look(ctx context.Context, f leftoverFolder, j *judging) (l look, ok bool, err error) {
	exact, fuzzy := a.match(f, j)
	if !exact && fuzzy == "" {
		return look{}, false, nil
	}
	u, found, err := a.w.usage(ctx, f.path)
	if err != nil || !found {
		return look{}, false, err
	}
	l = look{f: f, exact: exact, item: engine.Item{Path: f.path, Label: f.label(), Bytes: u.Bytes, LastUsed: u.Newest, Partial: u.Partial}}
	idleFor := a.now().Sub(u.Newest)
	why := j.ptrs.at(f)
	switch {
	case why != "":
		l.use = why
	case u.Partial:
		l.use = "could not be read in full"
	case escapedByTools(f.path):
		l.use = "lsof would not print its path as it is, so a process running from it cannot be ruled out"
	case idleFor < a.idle:
		l.use = fmt.Sprintf("changed %s; offered after %d days without a change", daysAgo(idleFor), int(a.idle.Hours()/24))
	default:
		if l.use, err = a.marksChanged(ctx, f, j); err != nil {
			return look{}, false, err
		}
	}
	if !exact && !u.Partial && idleFor >= a.idle {
		if why == "" {
			why = fuzzy
		}
		l.item.Label += " — " + why
		l.listed = true
	}
	return l, true, nil
}

// marksChanged says when a file of the folder's maker beside the
// folders changed, if inside the idle window: a settings file or a
// cookie store named by any id of the maker. A link counts by what it
// leads to: settings kept in a synced folder.
func (a *appLeftovers) marksChanged(ctx context.Context, f leftoverFolder, j *judging) (string, error) {
	maker := f.maker()
	if maker == "" {
		return "", nil
	}
	if why, ok := j.marked[maker]; ok {
		return why, nil
	}
	var why string
	var newest time.Time
	for _, p := range j.marks[maker] {
		fi, err := a.w.stat(ctx, p)
		switch {
		case absent(err):
			if li, err := a.w.lstat(ctx, p); err == nil && li.Mode()&fs.ModeSymlink != 0 {
				why = tilde(a.host, p) + " leads to nothing, so when the app last changed it is unknown"
				j.marked[maker] = why
				return why, nil
			}
			continue
		case err != nil:
			return "", fmt.Errorf("%s cannot be read, so when the app last changed it is unknown: %w", p, err)
		}
		if idleFor := a.now().Sub(fi.ModTime()); idleFor < a.idle && fi.ModTime().After(newest) {
			newest = fi.ModTime()
			why = fmt.Sprintf("%s, of the same maker, changed %s", tilde(a.host, p), daysAgo(idleFor))
		}
	}
	j.marked[maker] = why
	return why, nil
}

// match says whether a folder is a leftover by its exact id, or why it
// is only a fuzzy one; neither when an installed bundle owns it, or it
// is macOS's own.
func (a *appLeftovers) match(f leftoverFolder, j *judging) (exact bool, fuzzy string) {
	in := j.in
	if f.id == "" {
		key := nameKey(f.name)
		if key == "" || macOSName(key, j.system) {
			return false, ""
		}
		if _, owned := in.names[key]; owned {
			return false, ""
		}
		var near []string
		for n := range in.names {
			if len(n) >= 4 && len(key) >= 4 && (strings.HasPrefix(key, n) || strings.HasPrefix(n, key)) {
				near = append(near, n)
			}
		}
		if len(near) > 0 {
			slices.Sort(near)
			n := in.names[near[0]]
			if n.maker {
				return false, "named for a product, not an app id; an app of the same maker is installed: " + filepath.Base(n.app)
			}
			return false, "named for a product, not an app id; may belong to " + filepath.Base(n.app)
		}
		return false, "named for a product, not an app id; no installed app has that name"
	}
	id := foldName(f.id)
	if _, owned := in.ids[id]; owned {
		return false, ""
	}
	if _, owned := in.entitled[id]; owned {
		return false, ""
	}
	if b, ok := in.vendors[vendorOf(f.id)]; ok {
		return false, "an app of the same maker is installed: " + filepath.Base(b)
	}
	if labels := idCore(f.id); len(labels) > 2 {
		if app, ok := in.products[nameKey(labels[2])]; ok && len(nameKey(labels[2])) >= 4 {
			return false, "named like an app id, but may belong to " + filepath.Base(app) + ", which has another id"
		}
	}
	if !j.recheck && !madeByMacOS[f.loc] && !j.signed[coreID(f.id)] {
		for _, loc := range appSigns {
			if j.unread[loc] {
				return false, "named like an app id; ~/Library/" + loc + " could not be read in full, so whether macOS keeps a folder of the app under it is unknown"
			}
		}
		return false, "named like an app id, but macOS keeps no container, group, saved state or app scripts under it"
	}
	return true, ""
}

// systemFolders hold macOS's own frameworks and programs, and
// systemJobs the launchd jobs that start its daemons and agents.
var (
	systemFolders = []string{
		"/System/Library/Frameworks", "/System/Library/PrivateFrameworks", "/System/Library/CoreServices",
		"/usr/libexec", "/usr/sbin",
	}
	systemJobs = []string{"/System/Library/LaunchAgents", "/System/Library/LaunchDaemons"}
)

// systemNames are the nameKeys of macOS's own programs and frameworks,
// which name its folders in Application Support (contactsd,
// CallHistoryTransactions, Knowledge), and of MobileSync, the iPhone
// backups, which no program's name gives. A folder that cannot be read
// in full is an error: a folder of macOS's could pass for a leftover.
func (a *appLeftovers) systemNames(ctx context.Context) (map[string]bool, error) {
	names := map[string]bool{"mobilesync": true}
	add := func(name string) {
		if k := nameKey(strings.TrimSuffix(name, filepath.Ext(name))); k != "" {
			names[k] = true
		}
	}
	list := func(d string) ([]fs.DirEntry, string, error) {
		dir := a.host.ExpandPath(d)
		if _, err := a.w.lstat(ctx, dir); absent(err) {
			return nil, dir, nil
		}
		entries, partial, err := a.w.list(ctx, dir)
		if err == nil && partial {
			err = fmt.Errorf("%s could not be read in full, so macOS's own folders cannot be told from leftovers", dir)
		}
		return entries, dir, err
	}
	for _, d := range systemFolders {
		entries, _, err := list(d)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			add(e.Name())
		}
	}
	for _, d := range systemJobs {
		entries, dir, err := list(d)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			data, err := bounded(ctx, a.w, p, func() ([]byte, error) { return readLimited(p, maxLeftoverFile) })
			switch {
			case absent(err):
				continue
			case err != nil:
				return nil, fmt.Errorf("%s cannot be read, so macOS's own folders cannot be told from leftovers: %w", p, err)
			}
			texts, err := plistTexts(data)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", p, err)
			}
			for _, t := range texts {
				if filepath.IsAbs(t) {
					add(filepath.Base(t))
				}
			}
		}
	}
	return names, ctx.Err()
}

// macOSName reports a product name that is macOS's own: a system name,
// or one that starts the other when the shorter has six letters or
// more (Knowledge and knowledge-agent, CallHistoryTransactions and
// CallHistory).
func macOSName(key string, system map[string]bool) bool {
	if system[key] {
		return true
	}
	for n := range system {
		if min(len(n), len(key)) >= 6 && (strings.HasPrefix(key, n) || strings.HasPrefix(n, key)) {
			return true
		}
	}
	return false
}

// coreID is an id less a team id and a group label in front: an app's
// container and its group (com.example.app,
// ABCDE12345.group.com.example.app) share one.
func coreID(id string) string {
	return strings.Join(idCore(id), ".")
}

func daysAgo(d time.Duration) string {
	days := int(d.Hours() / 24)
	switch {
	case days < 1:
		return "today"
	case days == 1:
		return "a day ago"
	}
	return strconv.Itoa(days) + " days ago"
}
