package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lbagic/regrow/internal/engine"
)

// Homebrew cask records whose app is gone: an app deleted by hand, or
// replaced by its own updater under another name, leaves brew listing
// the cask, its Caskroom folder and its shims. One brew call lists the
// installed casks for two rules. A cask whose every app is gone from
// where brew put it, /Applications and ~/Applications, is an item:
// brew-cask-records offers `brew uninstall --cask <token>` when the
// cask's uninstall steps ask for no password (none, or only quitting
// the app), and brew-cask-records-sudo shows the rest, whose launchctl,
// pkgutil, delete or script steps make brew ask for one. Never --zap:
// that deletes the app's data with no Trash. A cask that installs no
// app is left out, and so is one whose install record or app folder
// cannot be read. The cask-record-recheck pre-action asks brew again
// right before the uninstall.

// plainArtifacts are the artifact kinds brew removes as the owner, with
// no password; a cask with any other kind needs the owner at a terminal.
var plainArtifacts = map[string]bool{
	"app": true, "binary": true, "manpage": true, "bash_completion": true, "zsh_completion": true, "fish_completion": true,
	"generate_completions_from_executable": true, "preflight_steps": true, "postflight_steps": true, "zap": true, "uninstall": true,
}

// caskRecords lists the installed casks once per scan for both rules.
// Tests hand in brew, the home folder and the walker.
type caskRecords struct {
	Brew func(ctx context.Context, args ...string) (out []byte, ok bool, err error)
	Home string
	w    *walker

	mu    sync.Mutex
	done  bool
	plain []engine.Item
	sudo  []engine.Item
	err   error
}

// Plain is the brew-cask-records query: records brew removes with no
// password.
func (c *caskRecords) Plain(ctx context.Context) ([]engine.Item, error) {
	c.load(ctx)
	return c.plain, c.err
}

// NeedsPassword is the brew-cask-records-sudo query: records whose
// uninstall asks for a password.
func (c *caskRecords) NeedsPassword(ctx context.Context) ([]engine.Item, error) {
	c.load(ctx)
	return c.sudo, c.err
}

// RecheckCaskRecord is the cask-record-recheck pre-action: right before
// `brew uninstall --cask`, it asks brew about the casks again and
// refuses unless the one at path is still installed, its apps still
// gone, and its uninstall still asks for no password.
func RecheckCaskRecord(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, toolQueryTimeout)
	defer cancel()
	return (&caskRecords{}).recheck(ctx, path)
}

func (c *caskRecords) recheck(ctx context.Context, path string) error {
	plain, sudo, err := c.list(ctx)
	if err != nil {
		return fmt.Errorf("recheck %s: %w: not uninstalled", path, err)
	}
	for _, it := range plain {
		if it.Path == filepath.Clean(path) {
			return nil
		}
	}
	for _, it := range sudo {
		if it.Path == filepath.Clean(path) {
			return fmt.Errorf("cask %s now has uninstall steps that ask for a password: not uninstalled", filepath.Base(path))
		}
	}
	return fmt.Errorf("cask %s is no longer installed with its app gone: not uninstalled", filepath.Base(path))
}

func (c *caskRecords) load(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return
	}
	c.done = true
	c.plain, c.sudo, c.err = c.list(ctx)
}

func (c *caskRecords) walker() *walker {
	if c.w != nil {
		return c.w
	}
	return defaultWalker
}

func (c *caskRecords) brew(ctx context.Context, args ...string) ([]byte, bool, error) {
	if c.Brew != nil {
		return c.Brew(ctx, args...)
	}
	path, err := exec.LookPath("brew")
	if err != nil {
		return nil, false, nil
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.WaitDelay = toolWaitDelay
	cmd.Env = append(os.Environ(), "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ENV_HINTS=1")
	out, err := cmd.Output()
	if err != nil {
		return nil, false, fmt.Errorf("brew %s: %w", strings.Join(args, " "), err)
	}
	return out, true, nil
}

func (c *caskRecords) home() string {
	if c.Home != "" {
		return c.Home
	}
	home, _ := os.UserHomeDir()
	return home
}

// installedCask is the part of `brew info --cask --json=v2` read here.
type installedCask struct {
	Token     string                       `json:"token"`
	Installed string                       `json:"installed"`
	Artifacts []map[string]json.RawMessage `json:"artifacts"`
}

func (c *caskRecords) list(ctx context.Context) (plain, sudo []engine.Item, err error) {
	out, ok, err := c.brew(ctx, "info", "--cask", "--json=v2", "--installed")
	if err != nil || !ok {
		return nil, nil, err
	}
	var info struct {
		Casks []installedCask `json:"casks"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, nil, fmt.Errorf("brew info --cask --json=v2: %w", err)
	}
	out, ok, err = c.brew(ctx, "--caskroom")
	if err != nil || !ok {
		return nil, nil, err
	}
	caskroom := strings.TrimSpace(string(out))
	if !filepath.IsAbs(caskroom) {
		return nil, nil, fmt.Errorf("brew --caskroom printed %q, not a folder", caskroom)
	}
	for _, cask := range info.Casks {
		item, needsPassword, ok, err := c.judge(ctx, caskroom, cask)
		switch {
		case err != nil:
			return nil, nil, err
		case !ok:
		case needsPassword:
			sudo = append(sudo, item)
		default:
			plain = append(plain, item)
		}
	}
	return plain, sudo, nil
}

// judge decides one cask: ok when every app it installed is gone, and
// whether its uninstall asks for a password. A cask that installs no
// app, or whose record or app folder cannot be read, is not ok. Its
// only error is a cask brew described in a shape not read here.
func (c *caskRecords) judge(ctx context.Context, caskroom string, cask installedCask) (engine.Item, bool, bool, error) {
	if cask.Token == "" || strings.ContainsAny(cask.Token, "/ ") || cask.Token[0] == '-' {
		return engine.Item{}, false, false, fmt.Errorf("brew described a cask named %q", cask.Token)
	}
	var apps []string
	needsPassword := false
	for _, art := range cask.Artifacts {
		for kind, raw := range art {
			if !plainArtifacts[kind] {
				needsPassword = true
			}
			switch kind {
			case "app":
				target, err := appTarget(raw)
				if err != nil {
					return engine.Item{}, false, false, fmt.Errorf("cask %s: %w", cask.Token, err)
				}
				apps = append(apps, target)
			case "uninstall":
				var stanzas []map[string]json.RawMessage
				if err := json.Unmarshal(raw, &stanzas); err != nil {
					return engine.Item{}, false, false, fmt.Errorf("cask %s: its uninstall steps: %w", cask.Token, err)
				}
				for _, st := range stanzas {
					for step := range st {
						needsPassword = needsPassword || step != "quit"
					}
				}
			}
		}
	}
	if len(apps) == 0 {
		return engine.Item{}, false, false, nil
	}
	record := filepath.Join(caskroom, cask.Token)
	appdir, ok := c.appdir(ctx, record)
	if !ok {
		return engine.Item{}, false, false, nil
	}
	w := c.walker()
	home := c.home()
	for _, app := range apps {
		if rest, ok := strings.CutPrefix(app, "~/"); ok {
			app = filepath.Join(home, rest)
		}
		places := []string{app}
		if !filepath.IsAbs(app) {
			places = []string{filepath.Join(appdir, app), filepath.Join("/Applications", app), filepath.Join(home, "Applications", app)}
		}
		for _, p := range places {
			if _, err := w.lstat(ctx, p); !absent(err) {
				// There, or it cannot be told.
				return engine.Item{}, false, false, nil
			}
		}
	}
	u, found, err := w.usage(ctx, record)
	if err != nil || !found {
		return engine.Item{}, false, false, err
	}
	names := make([]string, len(apps))
	for i, a := range apps {
		names[i] = filepath.Base(a)
	}
	label := fmt.Sprintf("%s %s: %s gone", cask.Token, cask.Installed, strings.Join(names, ", "))
	return engine.Item{Path: record, Arg: cask.Token, Label: label, Bytes: u.Bytes, LastUsed: u.Newest, Partial: u.Partial}, needsPassword, true, nil
}

// appTarget is where an app artifact puts the app, relative to the
// app folder unless absolute or under ~: its target, or its own name.
func appTarget(raw json.RawMessage) (string, error) {
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil || len(parts) == 0 {
		return "", errors.New("an app artifact regrow cannot read")
	}
	var name string
	if err := json.Unmarshal(parts[0], &name); err != nil || name == "" {
		return "", errors.New("an app artifact with no name")
	}
	for _, p := range parts[1:] {
		var opts struct {
			Target string `json:"target"`
		}
		if json.Unmarshal(p, &opts) == nil && opts.Target != "" {
			name = opts.Target
		}
	}
	if strings.Contains(name, "..") {
		return "", fmt.Errorf("an app target that climbs out of its folder: %q", name)
	}
	return filepath.Clean(name), nil
}

// appdir is the app folder brew installed the cask's apps into, from
// the config it recorded at install: the explicit choice, then the
// environment's, then the default. ok is false when the record cannot
// be read or the folder is not there (a disk not mounted).
func (c *caskRecords) appdir(ctx context.Context, record string) (string, bool) {
	path := filepath.Join(record, ".metadata", "config.json")
	data, err := bounded(ctx, c.walker(), path, func() ([]byte, error) { return readLimited(path, 1<<20) })
	dir := "/Applications"
	switch {
	case absent(err):
	case err != nil:
		return "", false
	default:
		var cfg map[string]*struct {
			Appdir string `json:"appdir"`
		}
		if json.Unmarshal(data, &cfg) != nil {
			return "", false
		}
		for _, k := range []string{"explicit", "env", "default"} {
			if v := cfg[k]; v != nil && v.Appdir != "" {
				dir = v.Appdir
				break
			}
		}
	}
	if rest, ok := strings.CutPrefix(dir, "~/"); ok {
		dir = filepath.Join(c.home(), rest)
	}
	if !filepath.IsAbs(dir) || !c.walker().isDir(ctx, dir) {
		return "", false
	}
	return dir, true
}
