package scanner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
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
// under them), of an app Spotlight knows anywhere, or of a running app;
// or when an installed bundle's entitlements name it (its app groups).
// A folder that something still points into is kept: a launchd job, a
// browser's native messaging host or a running process. Every failure
// to read an owner or a pointer leaves nothing offered.
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
// app and names by its bundle or group id. A folder in Application
// Support or Caches is named by whatever made it, a command-line tool or
// a library as often as an app, so its name is an app's id only when
// macOS also made a folder or a preferences file under that id.
var madeByMacOS = map[string]bool{
	"Containers": true, "Group Containers": true, "HTTPStorages": true,
	"WebKit": true, "Saved Application State": true, "Application Scripts": true,
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
	// are. It can only add owners: no answer is none from it.
	spotlight func(ctx context.Context) []string
	// entitlements returns a bundle's entitlements as codesign prints
	// them, nothing for a bundle that has none.
	entitlements func(ctx context.Context, bundle string) ([]byte, error)
	// procs lists the executables, mapped files and working folders of
	// the user's running processes.
	procs func(ctx context.Context) ([]string, error)
	// memo, when set, holds the installed apps across rechecks.
	memo *ownersMemo

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
}

var recheckOwners = &ownersMemo{}

// owners reads the installed apps, or takes them from the memo while
// its reading is younger than ownersReuse.
func (a *appLeftovers) owners(ctx context.Context, procs []string) (*installedApps, error) {
	if a.memo == nil {
		return a.installed(ctx, procs)
	}
	a.memo.mu.Lock()
	defer a.memo.mu.Unlock()
	now := a.now()
	if age := now.Sub(a.memo.at); a.memo.in != nil && a.memo.home == a.host.Home && age >= 0 && age < ownersReuse {
		return a.memo.in, nil
	}
	in, err := a.installed(ctx, procs)
	if err != nil {
		return nil, err
	}
	a.memo.home, a.memo.at, a.memo.in = a.host.Home, now, in
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

// judge decides every folder in the leftover locations, or only the one
// at only: the items offered, then the items kept. An owner or a pointer
// that cannot be read is an error.
func (a *appLeftovers) judge(ctx context.Context, only string) (offered, kept []engine.Item, err error) {
	folders, names, blocked, err := a.folders(ctx, only)
	if err != nil || len(folders) == 0 {
		return blocked, nil, err
	}
	procs, err := a.procs(ctx)
	if err != nil {
		return nil, nil, err
	}
	owners, err := a.owners(ctx, procs)
	if err != nil {
		return nil, nil, err
	}
	ptrs, err := a.pointers(ctx, procs)
	if err != nil {
		return nil, nil, err
	}
	unread := map[string]bool{}
	for _, f := range folders {
		item, offer, listed, err := a.decide(ctx, f, names, owners, ptrs)
		switch {
		case err != nil:
			return nil, nil, err
		case offer:
			offered = append(offered, item)
		case listed:
			kept = append(kept, item)
		case item.Partial:
			unread[f.loc] = true
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

// folders lists the leftover locations: every folder that could be a
// leftover (not macOS's own, not one whose name says nothing), the
// names in each location and in ~/Library/Preferences by folded name,
// and a marker for each location that could not be read in full.
func (a *appLeftovers) folders(ctx context.Context, only string) ([]leftoverFolder, map[string]map[string]bool, []engine.Item, error) {
	names := map[string]map[string]bool{}
	var folders []leftoverFolder
	var blocked []engine.Item
	library := filepath.Join(a.host.Home, "Library")
	for _, loc := range append([]string{"Preferences"}, leftoverLocations...) {
		dir := filepath.Join(library, loc)
		names[loc] = map[string]bool{}
		if _, err := a.w.lstat(ctx, dir); absent(err) {
			continue
		}
		entries, partial, err := a.w.list(ctx, dir)
		if err != nil {
			return nil, nil, nil, err
		}
		if partial && loc != "Preferences" {
			blocked = append(blocked, unreadableMarker("~/Library/"+loc))
		}
		for _, e := range entries {
			names[loc][foldName(e.Name())] = true
			if loc == "Preferences" || !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			f := leftoverFolder{loc: loc, name: e.Name(), path: filepath.Join(dir, e.Name()), id: leftoverID(loc, e.Name())}
			if only != "" && f.path != only {
				continue
			}
			if (f.id == "" && loc != "Application Support") || (f.id != "" && appleID(f.id)) {
				continue
			}
			folders = append(folders, f)
		}
	}
	slices.SortFunc(folders, func(x, y leftoverFolder) int { return strings.Compare(x.path, y.path) })
	return folders, names, blocked, nil
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
	// entitled are the strings of every code bundle's entitlements,
	// folded: app groups, keychain groups, the application identifier.
	entitled map[string]string
}

type appName struct {
	app   string
	maker bool
}

// installed reads every bundle under the app roots, the apps Spotlight
// knows and the running apps.
func (a *appLeftovers) installed(ctx context.Context, procs []string) (*installedApps, error) {
	plists, err := a.infoPlists(ctx)
	if err != nil {
		return nil, err
	}
	in := &installedApps{ids: map[string]string{}, vendors: map[string]string{}, names: map[string]appName{}, entitled: map[string]string{}}
	var code []string
	setOnce := func(m map[string]string, k, v string) {
		if _, seen := m[k]; !seen && k != "" {
			m[k] = v
		}
	}
	name := func(k string, n appName) {
		if _, seen := in.names[k]; !seen && k != "" {
			in.names[k] = n
		}
	}
	add := func(plist string, info map[string]string) {
		id := info["CFBundleIdentifier"]
		if id == "" {
			return
		}
		app := outermostApp(plist)
		setOnce(in.ids, foldName(id), app)
		bundle := bundleOf(plist)
		if bundle == "" {
			return
		}
		code = append(code, bundle)
		if !strings.EqualFold(filepath.Ext(bundle), ".app") {
			return
		}
		if v := vendorOf(id); v != "" {
			setOnce(in.vendors, v, app)
			name(nameKey(strings.SplitN(v, ".", 2)[1]), appName{app: app, maker: true})
		}
		base := filepath.Base(bundle)
		for _, n := range []string{strings.TrimSuffix(base, filepath.Ext(base)), info["CFBundleName"], info["CFBundleDisplayName"], info["CFBundleExecutable"]} {
			name(nameKey(n), appName{app: app})
		}
	}
	for _, p := range plists {
		data, err := bounded(ctx, a.w, p, func() ([]byte, error) { return readLimited(p, maxLeftoverFile) })
		if err != nil {
			return nil, fmt.Errorf("%s cannot be read, so the bundle it describes cannot be ruled out as an owner: %w", p, err)
		}
		info, err := plistTopStrings(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		add(p, info)
	}
	// Spotlight and the running apps only add: a bundle there that
	// cannot be read is no owner from them.
	var extra []string
	if a.spotlight != nil {
		extra = append(extra, a.spotlight(ctx)...)
	}
	for _, p := range procs {
		if b := runningBundle(p); b != "" {
			extra = append(extra, b)
		}
	}
	slices.Sort(extra)
	for _, b := range slices.Compact(extra) {
		for _, p := range []string{filepath.Join(b, "Contents", "Info.plist"), filepath.Join(b, "Info.plist")} {
			data, err := bounded(ctx, a.w, p, func() ([]byte, error) { return readLimited(p, maxLeftoverFile) })
			if err != nil {
				continue
			}
			if info, err := plistTopStrings(data); err == nil {
				add(p, info)
			}
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := a.readEntitlements(ctx, code, in.entitled); err != nil {
		return nil, err
	}
	return in, nil
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

// outermostApp is the first app on a path (/Applications/X.app for
// anything inside it), or the folder holding an Info.plist outside one.
func outermostApp(plist string) string {
	parts := strings.Split(plist, string(filepath.Separator))
	for i, p := range parts {
		if strings.EqualFold(filepath.Ext(p), ".app") {
			return strings.Join(parts[:i+1], string(filepath.Separator))
		}
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
					case e.Name() == "Info.plist" && !e.IsDir():
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

// runningBundle is the bundle a running executable sits in
// (/Applications/Foo.app/Contents/MacOS/Foo), "" for any other path.
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
// mdfind missing, finds none.
func mdfindApps(ctx context.Context) []string {
	out, ok, err := runTool(ctx, "mdfind", "kMDItemContentTypeTree == 'com.apple.application-bundle'")
	if !ok || err != nil {
		return nil
	}
	var paths []string
	for line := range strings.SplitSeq(string(out), "\n") {
		if filepath.IsAbs(line) {
			paths = append(paths, line)
		}
	}
	return paths
}

// lsofCwdAndMapped lists the working folders, executables and mapped
// files of the user's processes (lsof's cwd and txt entries).
func lsofCwdAndMapped(ctx context.Context) ([]string, error) {
	out, err := processTool(ctx, "lsof", "-a", "-u", strconv.Itoa(os.Getuid()), "-d", "cwd,txt", "-F", "pn")
	if err != nil {
		return nil, err
	}
	return mappedNames(string(out), os.Getpid())
}

// pointers is every string that can point into a leftover: each launchd
// job's and native messaging manifest's strings and each running
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

func (p *pointers) add(text, from string) {
	t := foldName(strings.ReplaceAll(text, `\ `, " "))
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
// that cannot be read is an error: what it points to is unknown.
func (a *appLeftovers) pointers(ctx context.Context, procs []string) (*pointers, error) {
	ptrs := &pointers{whole: map[string]string{}, into: map[string][]pointerTail{}}
	for _, p := range procs {
		ptrs.add(p, "a running process")
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
			from := kind + " " + tilde(a.host, p)
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

// decide judges one folder. offer is true for a leftover the rule
// offers; listed for a folder the kept rule shows. A folder neither
// offered nor listed is owned, in use, or a fuzzy match that changed in
// the last 90 days; item.Partial then means it could not be read in
// full. Its only error is ctx's.
func (a *appLeftovers) decide(ctx context.Context, f leftoverFolder, names map[string]map[string]bool, in *installedApps, ptrs *pointers) (item engine.Item, offer, listed bool, err error) {
	exact, fuzzy := a.match(f, names, in)
	if !exact && fuzzy == "" {
		return engine.Item{}, false, false, nil
	}
	u, found, err := a.w.usage(ctx, f.path)
	if err != nil || !found {
		return engine.Item{}, false, false, err
	}
	item = engine.Item{Path: f.path, Label: f.label(), Bytes: u.Bytes, LastUsed: u.Newest, Partial: u.Partial}
	idleFor := a.now().Sub(u.Newest)
	why := ptrs.at(f)
	if !exact {
		if u.Partial || idleFor < a.idle {
			return item, false, false, nil
		}
		if why == "" {
			why = fuzzy
		}
		item.Label += " — " + why
		return item, false, true, nil
	}
	switch {
	case why != "":
	case u.Partial:
		why = "could not be read in full"
	case escapedByTools(f.path):
		why = "lsof would not print its path as it is, so a process running from it cannot be ruled out"
	case idleFor < a.idle:
		why = fmt.Sprintf("changed %s; offered after %d days without a change", daysAgo(idleFor), int(a.idle.Hours()/24))
	default:
		return item, true, false, nil
	}
	item.Label += " — " + why
	return item, false, true, nil
}

// match says whether a folder is a leftover by its exact id, or why it
// is only a fuzzy one; neither when an installed bundle owns it.
func (a *appLeftovers) match(f leftoverFolder, names map[string]map[string]bool, in *installedApps) (exact bool, fuzzy string) {
	if f.id == "" {
		key := nameKey(f.name)
		if key == "" {
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
	if !madeByMacOS[f.loc] && !madeForID(id, names) {
		return false, "named like an app id, but macOS keeps no container, web data, saved state or preferences under it"
	}
	return true, ""
}

// madeForID reports a folder or preferences file macOS made for the id.
func madeForID(id string, names map[string]map[string]bool) bool {
	for loc := range madeByMacOS {
		key := id
		if loc == "Saved Application State" {
			key += ".savedstate"
		}
		if names[loc][key] {
			return true
		}
	}
	return names["Preferences"][id+".plist"]
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
