package scanner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// Docker Desktop stages an update in
// ~/Library/Application Support/com.docker.install/in_progress/Docker.app
// and installs it from there; a download that stopped stays. It is not a
// container, an image or a volume, and removing it stops nothing: Docker
// Desktop downloads the update again. The folder is an item, to the
// Trash, when the staged app is unfinished (no Info.plist) or not newer
// than the installed one, nothing in it changed in the last day, no
// running process uses it, and Docker Desktop, which downloads it, is
// not running. The docker-update-recheck pre-action judges it again
// right before the move.

const dockerUpdateQuiet = 24 * time.Hour

// dockerUpdate is the docker-update-download query with its seams:
// tests hand in the walker, the folders, the plist reader, the process
// lists and the clock.
type dockerUpdate struct {
	w      *walker
	dir    string   // in_progress
	apps   []string // where an installed Docker.app may be
	quiet  time.Duration
	plist  func(ctx context.Context, data []byte) ([]byte, error)
	procs  func(ctx context.Context) (processes, error)
	mapped func(ctx context.Context) ([]string, error)
	now    func() time.Time
}

func newDockerUpdate(home string) *dockerUpdate {
	return &dockerUpdate{
		w:      defaultWalker,
		dir:    filepath.Join(home, "Library/Application Support/com.docker.install/in_progress"),
		apps:   []string{"/Applications/Docker.app", filepath.Join(home, "Applications/Docker.app")},
		quiet:  dockerUpdateQuiet,
		plist:  plutilXML,
		procs:  runningProcesses,
		mapped: lsofMapped,
		now:    time.Now,
	}
}

func queryDockerUpdate(ctx context.Context) ([]engine.Item, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil
	}
	item, _, err := newDockerUpdate(home).judge(ctx)
	if err != nil || item == nil {
		return nil, err
	}
	return []engine.Item{*item}, nil
}

// RecheckDockerUpdate is the docker-update-recheck pre-action.
func RecheckDockerUpdate(ctx context.Context, path string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("recheck %s: %w", path, err)
	}
	ctx, cancel := context.WithTimeout(ctx, toolQueryTimeout)
	defer cancel()
	d := newDockerUpdate(home)
	if filepath.Clean(path) != d.dir {
		return fmt.Errorf("%s is not Docker Desktop's update folder %s: not moved", path, d.dir)
	}
	item, why, err := d.judge(ctx)
	switch {
	case err != nil:
		return fmt.Errorf("recheck %s: %w: not moved", path, err)
	case item == nil && why == "":
		return fmt.Errorf("%s is gone: not moved", path)
	case item == nil:
		return fmt.Errorf("%s is kept now (%s): not moved", path, why)
	}
	return nil
}

// judge decides the folder: an item, or why it stays (both empty when
// there is no folder). An unreadable Info.plist, or processes that
// cannot be listed, are errors.
func (d *dockerUpdate) judge(ctx context.Context) (*engine.Item, string, error) {
	if _, err := d.w.lstat(ctx, d.dir); absent(err) {
		return nil, "", nil
	} else if err != nil {
		return nil, "", fmt.Errorf("%s: %w", d.dir, err)
	}
	staged := filepath.Join(d.dir, "Docker.app")
	if _, err := d.w.lstat(ctx, staged); absent(err) {
		return nil, "it holds no Docker.app, so what it is cannot be told", nil
	} else if err != nil {
		return nil, "", fmt.Errorf("%s: %w", staged, err)
	}
	label := "Docker Desktop update, never finished downloading"
	version, found, err := d.version(ctx, staged)
	switch {
	case err != nil:
		return nil, "", err
	case found:
		installed, why, err := d.installed(ctx)
		if err != nil || why != "" {
			return nil, why, err
		}
		if engine.CompareVersion(version, installed) > 0 {
			return nil, fmt.Sprintf("it holds Docker %s, newer than the installed %s: the update is waiting", version, installed), nil
		}
		label = fmt.Sprintf("Docker Desktop %s update, not newer than the installed %s", version, installed)
	}
	if escapedByTools(d.dir) {
		return nil, "lsof and ps would not print its path as it is, so no process can be matched to it", nil
	}
	procs, err := d.procs(ctx)
	if err != nil {
		return nil, "", err
	}
	mapped, err := d.mapped(ctx)
	if err != nil {
		return nil, "", err
	}
	if procs.workIn(d.dir) {
		return nil, "a running process works in it or names it", nil
	}
	real, err := bounded(ctx, d.w, d.dir, func() (string, error) { return filepath.EvalSymlinks(d.dir) })
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", d.dir, err)
	}
	if real != d.dir && procs.workIn(real) {
		return nil, "a running process works in it or names it", nil
	}
	for _, m := range mapped {
		if within(m, real) || within(m, d.dir) {
			return nil, "a running process uses " + m, nil
		}
	}
	for _, m := range mapped {
		if strings.Contains(foldName(m), "/docker.app/contents/") {
			return nil, "Docker Desktop is running: it may be downloading the update", nil
		}
	}
	u, found, err := d.w.usage(ctx, d.dir)
	switch {
	case err != nil:
		return nil, "", err
	case !found:
		return nil, "", nil
	case u.Partial:
		return nil, "it cannot all be read", nil
	case d.now().Sub(u.Newest) < d.quiet:
		return nil, "it changed in the last day: Docker Desktop may be downloading", nil
	}
	return &engine.Item{Path: d.dir, Label: label, Bytes: u.Bytes, LastUsed: u.Newest}, "", nil
}

// version reads an app's version from its Info.plist; found is false
// when the app has none (an unfinished download).
func (d *dockerUpdate) version(ctx context.Context, app string) (string, bool, error) {
	plistPath := filepath.Join(app, "Contents", "Info.plist")
	data, err := bounded(ctx, d.w, plistPath, func() ([]byte, error) { return readLimited(plistPath, maxInfoPlist) })
	switch {
	case absent(err):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("read %s: %w", plistPath, err)
	}
	if bytes.HasPrefix(data, []byte("bplist")) {
		if data, err = d.plist(ctx, data); err != nil {
			return "", false, fmt.Errorf("read %s: %w", plistPath, err)
		}
	}
	s, err := plistStrings(data)
	if err != nil {
		return "", false, fmt.Errorf("read %s: %w", plistPath, err)
	}
	v := s["CFBundleShortVersionString"]
	if !dottedVersionRe.MatchString(v) {
		return "", false, fmt.Errorf("%s names no version regrow can compare (%q)", plistPath, v)
	}
	return v, true, nil
}

// installed is the version of the Docker.app installed, or why the
// staged one cannot be judged against it.
func (d *dockerUpdate) installed(ctx context.Context) (string, string, error) {
	for _, app := range d.apps {
		v, found, err := d.version(ctx, app)
		if err != nil {
			return "", "", err
		}
		if found {
			return v, "", nil
		}
	}
	return "", "no installed Docker.app to compare the staged one with", nil
}
