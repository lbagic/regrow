package scanner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/lbagic/regrow/internal/engine"
)

// Simulator runtimes outlive Xcode: the images stay on disk, and with
// Xcode gone `xcrun simctl` is gone too, so sim-runtimes lists nothing.
// CoreSimulator, which Xcode installs outside its app bundle, keeps a
// simctl of its own. This query lists the runtimes through that one
// for sim-runtimes-no-xcode, and only while xcrun finds no simctl
// (with one, sim-runtimes lists the same runtimes) and no Xcode app is
// installed: xcrun also finds none while xcode-select points at the
// Command Line Tools, and the rows would then say Xcode is gone.

// coreSimulatorSimctl is the simctl CoreSimulator keeps outside Xcode.
// The rule's note names the same path.
const coreSimulatorSimctl = "/Library/Developer/PrivateFrameworks/CoreSimulator.framework/Versions/A/Resources/bin/simctl"

// simRuntimesNoXcode is the sim-runtimes-no-xcode query. The zero
// value asks the real machine; tests inject both tools.
type simRuntimesNoXcode struct {
	// HasSimctl reports whether xcrun finds simctl; nil asks xcrun.
	HasSimctl func(ctx context.Context) bool
	// HasXcode reports an installed Xcode app; nil looks for one.
	HasXcode func(ctx context.Context) bool
	// List runs CoreSimulator's `simctl runtime list -j`; ok is false
	// when that simctl is not there. nil runs it.
	List func(ctx context.Context) (out []byte, ok bool, err error)
	// Elsewhere reports an Xcode HasXcode cannot see, or a search that
	// cannot rule one out. It takes seconds, so it runs only once there
	// are runtimes to list; nil asks LaunchServices and Spotlight.
	Elsewhere func(ctx context.Context) bool
}

func (s *simRuntimesNoXcode) Runtimes(ctx context.Context) ([]engine.Item, error) {
	hasSimctl, hasXcode, list, elsewhere := s.HasSimctl, s.HasXcode, s.List, s.Elsewhere
	if hasSimctl == nil {
		hasSimctl = xcrunFindsSimctl
	}
	if hasXcode == nil {
		hasXcode = func(ctx context.Context) bool {
			home, _ := os.UserHomeDir()
			return xcodeInstalled(ctx, defaultWalker, "", home, spotlightXcodes)
		}
	}
	if list == nil {
		list = func(ctx context.Context) ([]byte, bool, error) {
			return runTool(ctx, coreSimulatorSimctl, "runtime", "list", "-j")
		}
	}
	if elsewhere == nil {
		elsewhere = func(ctx context.Context) bool {
			return !spotlightIndexes(ctx, runTool) || xcodeRegistered(ctx, defaultWalker, lsregisterBundles)
		}
	}
	if hasSimctl(ctx) || hasXcode(ctx) {
		return nil, nil
	}
	out, ok, err := list(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s runtime list: %w", coreSimulatorSimctl, err)
	}
	if !ok {
		return nil, nil
	}
	items, err := parseSimRuntimes(out)
	if err != nil || len(items) == 0 || elsewhere(ctx) {
		return nil, err
	}
	return items, nil
}

func xcrunFindsSimctl(ctx context.Context) bool {
	_, ok, err := runTool(ctx, "xcrun", "--find", "simctl")
	return ok && err == nil
}

// xcodeInstalled reports an app with Xcode's bundle id in either
// Applications folder or a folder directly inside one (a linked folder
// followed), or wherever Spotlight knows one. Only apps named like
// Xcode are opened; the Xcodes version manager is named so and is not
// Xcode. An Xcode-named app or a folder that cannot be read counts as
// Xcode, and so does a link among them, or an Applications folder that
// is a link, that cannot be followed, or a Spotlight with no answer.
func xcodeInstalled(ctx context.Context, w *walker, root, home string, spotlight func(context.Context) ([]string, bool)) bool {
	isXcode := func(app string) bool {
		data, err := readSource(ctx, w, filepath.Join(app, "Contents", "Info.plist"))
		if absent(err) {
			_, err := w.stat(ctx, app)
			return err != nil
		}
		if err != nil {
			return true
		}
		texts, err := plistTexts(data)
		return err != nil || slices.Contains(texts, xcodeBundleID)
	}
	apps := []string{filepath.Join(root, "/Applications")}
	if home != "" {
		apps = append(apps, filepath.Join(home, "Applications"))
	}
	for _, dir := range apps {
		if unfollowableLink(ctx, w, dir) {
			return true
		}
		entries, partial, err := w.list(ctx, dir)
		if partial || err != nil {
			return true
		}
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			if !strings.HasSuffix(e.Name(), ".app") && (e.IsDir() || e.Type()&fs.ModeSymlink != 0) {
				if e.Type()&fs.ModeSymlink != 0 {
					folder, unknown := linkedFolder(ctx, w, p)
					if unknown {
						return true
					}
					if !folder {
						continue
					}
				}
				inner, partial, err := w.list(ctx, p)
				if partial || err != nil {
					return true
				}
				for _, in := range inner {
					if xcodeNamed(in.Name()) && isXcode(filepath.Join(p, in.Name())) {
						return true
					}
				}
				continue
			}
			if xcodeNamed(e.Name()) && isXcode(p) {
				return true
			}
		}
	}
	found, ok := spotlight(ctx)
	if !ok {
		return true
	}
	for _, p := range found {
		if _, err := w.lstat(ctx, p); err == nil {
			return true
		}
	}
	return false
}

const xcodeBundleID = "com.apple.dt.Xcode"

// linkedFolder follows a link in an Applications folder: folder when
// it leads to one. A link that cannot be followed is unknown, as an app
// may be installed behind it on a disk not mounted now; a link whose
// target was deleted cannot be told from one of those.
func linkedFolder(ctx context.Context, w *walker, link string) (folder, unknown bool) {
	fi, err := w.stat(ctx, link)
	if err != nil {
		return false, true
	}
	return fi.IsDir(), false
}

// unfollowableLink reports a path that is a link that cannot be
// followed, as linkedFolder judges it.
func unfollowableLink(ctx context.Context, w *walker, path string) bool {
	fi, err := w.lstat(ctx, path)
	if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		return false
	}
	_, unknown := linkedFolder(ctx, w, path)
	return unknown
}

func xcodeNamed(name string) bool {
	name = strings.ToLower(name)
	return strings.HasPrefix(name, "xcode") && strings.HasSuffix(name, ".app")
}

// spotlightXcodes asks Spotlight for apps with Xcode's bundle id; ok is
// false when mdfind is missing or fails.
func spotlightXcodes(ctx context.Context) (paths []string, ok bool) {
	out, ok, err := runTool(ctx, "mdfind", "kMDItemCFBundleIdentifier == '"+xcodeBundleID+"'")
	if !ok || err != nil {
		return nil, false
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if filepath.IsAbs(line) {
			paths = append(paths, line)
		}
	}
	return paths, true
}

// spotlightIndexes reports Spotlight indexing both volumes of the
// startup disk. Off for either, mdfind finds no Xcode outside the
// Applications folders whether there is one or not.
func spotlightIndexes(ctx context.Context, run func(ctx context.Context, name string, args ...string) ([]byte, bool, error)) bool {
	for _, vol := range []string{"/", "/System/Volumes/Data"} {
		out, ok, err := run(ctx, "mdutil", "-s", vol)
		if !ok || err != nil || !strings.Contains(string(out), "Indexing enabled.") {
			return false
		}
	}
	return true
}

// lsregister prints LaunchServices' register: every bundle Finder, a
// launch or an install has met, in any folder and on any disk, with
// whether that disk is mounted.
const lsregister = "/System/Library/Frameworks/CoreServices.framework/Versions/A/Frameworks/LaunchServices.framework/Versions/A/Support/lsregister"

func lsregisterBundles(ctx context.Context) ([]byte, error) {
	out, ok, err := runTool(ctx, lsregister, "-dump", "Bundle")
	if err == nil && !ok {
		err = errors.New(lsregister + " not found")
	}
	return out, err
}

// lsPathRe takes the register's id off a path line's value.
var lsPathRe = regexp.MustCompile(`^(.+) \(0x[0-9a-f]+\)$`)

// xcodeRegistered reports a bundle with Xcode's id in LaunchServices'
// register that is still on disk, or that sits on a disk not mounted
// now. The register keeps a bundle deleted from a mounted disk for a
// while, and that one is gone. A dump that fails, or holds no bundle
// with a path and an id, cannot rule an Xcode out.
func xcodeRegistered(ctx context.Context, w *walker, dump func(context.Context) ([]byte, error)) bool {
	out, err := dump(ctx)
	if err != nil {
		return true
	}
	type bundle struct{ path, id, mount string }
	var bundles []bundle
	var b bundle
	flush := func() {
		if b.path != "" && b.id != "" {
			bundles = append(bundles, b)
		}
		b = bundle{}
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "bundle id":
			flush()
		case "path":
			if m := lsPathRe.FindStringSubmatch(value); m != nil {
				value = m[1]
			}
			b.path = value
		case "identifier":
			b.id = value
		case "mount state":
			b.mount = value
		}
	}
	flush()
	if len(bundles) == 0 {
		return true
	}
	for _, b := range bundles {
		if !strings.EqualFold(b.id, xcodeBundleID) {
			continue
		}
		if b.mount != "mounted" {
			return true
		}
		if _, err := w.lstat(ctx, b.path); !absent(err) {
			return true
		}
	}
	return false
}
