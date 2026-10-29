package scanner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
)

// macOS's /usr/bin/xcrun and /usr/bin/git are shims that run the tool
// from the active developer directory. With no Xcode and no Command
// Line Tools there, a shim offers to install the tools in a dialog, and
// Install would download gigabytes, so a scan never runs one then.

// selectLinks are the links `xcode-select -s` writes, read in the
// shims' order.
var selectLinks = []string{"/var/select/developer_dir", "/var/db/xcode_select_link"}

// defaultDeveloperDirs are where the shims look, in order, with no link.
var defaultDeveloperDirs = []string{
	"/Applications/Xcode.app/Contents/Developer",
	"/Library/Developer/CommandLineTools",
	"/Applications/Xcode-beta.app/Contents/Developer",
}

// gitShim is macOS's git; any other git runs without developer tools.
var gitShim = "/usr/bin/git"

// devTools finds the active developer directory the way the shims do,
// reading paths and running nothing. Root re-anchors every path it
// reads; empty is the real filesystem.
type devTools struct {
	w      *walker
	root   string
	getenv func(string) string
}

// present reports whether the active developer directory holds xcrun's
// library (or, in older layouts, xcrun): $DEVELOPER_DIR, else the first
// select link there is, read through the link, either of them an Xcode
// app or its Contents/Developer; else the first default that holds it.
// A link whose folder lacks it, or any path that cannot be read, means
// no tools.
func (d devTools) present(ctx context.Context) bool {
	unread := false
	holds := func(dir string, app bool) bool {
		devs := []string{dir}
		if app {
			devs = append(devs, filepath.Join(dir, "Contents", "Developer"))
		}
		for _, dev := range devs {
			for _, f := range []string{"usr/lib/libxcrun.dylib", "usr/bin/xcrun"} {
				_, err := d.w.stat(ctx, filepath.Join(dev, f))
				if err == nil {
					return true
				}
				if !absent(err) {
					unread = true
					return false
				}
			}
		}
		return false
	}
	if dir := d.getenv("DEVELOPER_DIR"); dir != "" {
		return holds(filepath.Join(d.root, dir), true)
	}
	for _, link := range selectLinks {
		p := filepath.Join(d.root, link)
		_, err := d.w.lstat(ctx, p)
		if absent(err) {
			continue
		}
		return err == nil && holds(p, true)
	}
	for _, dir := range defaultDeveloperDirs {
		if holds(filepath.Join(d.root, dir), false) {
			return true
		}
		if unread {
			return false
		}
	}
	return false
}

// developerTools is devTools on this Mac; tests replace it.
var developerTools = func(ctx context.Context) bool {
	return devTools{w: defaultWalker, getenv: os.Getenv}.present(ctx)
}

// gitRuns reports whether the git a scan would run, the first on PATH,
// is not the shim, by file and not by name, with no developer tools
// behind it. A shim that cannot be read counts as the shim.
func gitRuns(ctx context.Context) bool {
	p, err := exec.LookPath("git")
	if err != nil {
		return false
	}
	found, err := defaultWalker.stat(ctx, p)
	if err != nil {
		return false
	}
	shim, err := defaultWalker.stat(ctx, gitShim)
	if absent(err) || (err == nil && !os.SameFile(found, shim)) {
		return true
	}
	return developerTools(ctx)
}
