package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"

	"github.com/lbagic/regrow/internal/engine"
)

// Homebrew cask records whose app is gone: an app deleted by hand, or
// replaced by its own updater under another name, leaves brew listing
// the cask, its Caskroom folder and its shims. One brew call lists the
// installed casks for two rules. A cask whose every app is gone from
// where brew put it, /Applications and ~/Applications, and from one
// folder down in each, is an item: brew-cask-records offers
// `brew uninstall --cask <token>` when its uninstall only quits the
// app and nothing it does may ask for a password, and
// brew-cask-records-sudo shows the rest. Never --zap: that deletes the
// app's data with no Trash. A cask that installs no app is left out,
// and so is one whose install record or app folders cannot be read.
// The cask-record-recheck pre-action asks brew again right before the
// uninstall.
//
// From Homebrew 7, each relocated artifact carries a sibling "target":
// the absolute path brew resolved for it. It is no artifact kind.

// plainUninstallSteps are the uninstall steps regrow runs for the owner.
// brew runs every step even when the app is gone, so a step that
// signals the app or removes its login item would reach an app moved
// where regrow does not look.
var plainUninstallSteps = map[string]bool{"quit": true}

// linkArtifacts are the kinds brew installs as a link or small file in
// a folder; it removes one with sudo when that folder is not the
// owner's to write. With no target set, the folder is brew's own.
var linkArtifacts = map[string]bool{
	"binary": true, "manpage": true, "bash_completion": true, "zsh_completion": true, "fish_completion": true,
	"pwsh_completion": true, "command_wrapper": true,
}

// stepArtifacts hold install steps; Homebrew 7 runs their symlink
// steps marked uninstall again at uninstall. Uninstall steps
// (uninstall_preflight_steps and the like) run whole, so they are
// not here.
var stepArtifacts = map[string]bool{"preflight_steps": true, "postflight_steps": true}

// plainArtifacts are the other kinds brew removes with no password:
// completions it writes into its own folders, and zap, which a plain
// uninstall never runs. Any kind not named here may ask for one.
var plainArtifacts = map[string]bool{"generate_completions_from_executable": true, "zap": true}

// caskRecords lists the installed casks once per scan for both rules.
// Tests hand in brew, the home folder, the app folders and the walker.
type caskRecords struct {
	Brew func(ctx context.Context, args ...string) (out []byte, ok bool, err error)
	Home string
	Apps []string // nil: appRoots
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
// uninstall may ask for a password or does more than quit the app.
func (c *caskRecords) NeedsPassword(ctx context.Context) ([]engine.Item, error) {
	c.load(ctx)
	return c.sudo, c.err
}

// RecheckCaskRecord is the cask-record-recheck pre-action: right before
// `brew uninstall --cask`, it asks brew about the casks again and
// refuses unless the one at path is still installed, its apps still
// gone, and its uninstall still only quits the app and asks for no
// password.
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
			return fmt.Errorf("cask %s's uninstall now may ask for a password, or does more than quit the app: not uninstalled", filepath.Base(path))
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

// appFolders are where an app is looked for besides where brew put it.
func (c *caskRecords) appFolders() []string {
	if c.Apps != nil {
		return c.Apps
	}
	folders := make([]string, len(appRoots))
	for i, r := range appRoots {
		folders[i] = r
		if rest, ok := strings.CutPrefix(r, "~/"); ok {
			folders[i] = filepath.Join(c.home(), rest)
		}
	}
	return folders
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
	scan := &appScan{w: c.walker(), names: map[string]map[string]bool{}}
	for _, cask := range info.Casks {
		item, needsPassword, ok, err := c.judge(ctx, caskroom, cask, scan)
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
// whether its uninstall may ask for a password or does more than quit
// the app. A cask that installs no app, or whose record or app folders
// cannot be read, is not ok. Its only error is a cask brew described
// in a shape not read here.
func (c *caskRecords) judge(ctx context.Context, caskroom string, cask installedCask, scan *appScan) (engine.Item, bool, bool, error) {
	if cask.Token == "" || strings.ContainsAny(cask.Token, "/ ") || cask.Token[0] == '-' {
		return engine.Item{}, false, false, fmt.Errorf("brew described a cask named %q", cask.Token)
	}
	var apps []string
	needsPassword := false
	home := c.home()
	for _, art := range cask.Artifacts {
		resolved, told, err := resolvedTarget(art, home)
		switch {
		case err != nil:
			return engine.Item{}, false, false, fmt.Errorf("cask %s: %w", cask.Token, err)
		case !told:
			return engine.Item{}, false, false, nil
		}
		for kind, raw := range art {
			switch {
			case kind == "target":
			case kind == "app":
				target, err := appTarget(raw, resolved)
				if err != nil {
					return engine.Item{}, false, false, fmt.Errorf("cask %s: %w", cask.Token, err)
				}
				apps = append(apps, target)
			case kind == "uninstall":
				var stanzas []map[string]json.RawMessage
				if err := json.Unmarshal(raw, &stanzas); err != nil {
					return engine.Item{}, false, false, fmt.Errorf("cask %s: its uninstall steps: %w", cask.Token, err)
				}
				for _, st := range stanzas {
					for step := range st {
						needsPassword = needsPassword || !plainUninstallSteps[step]
					}
				}
			case linkArtifacts[kind]:
				needsPassword = needsPassword || c.linkNeedsPassword(ctx, raw, resolved)
			case stepArtifacts[kind]:
				needsPassword = needsPassword || stepsNeedPassword(raw)
			case !plainArtifacts[kind]:
				needsPassword = true
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
	folders := c.appFolders()
	for _, app := range apps {
		if rest, ok := strings.CutPrefix(app, "~/"); ok {
			app = filepath.Join(home, rest)
		}
		if filepath.IsAbs(app) && !w.isDir(ctx, filepath.Dir(app)) {
			// Its folder is not there: a disk not mounted.
			return engine.Item{}, false, false, nil
		}
		places := []string{app}
		name := filepath.Base(app)
		if !filepath.IsAbs(app) {
			places = []string{filepath.Join(appdir, app)}
			name = app
		}
		for _, f := range folders {
			places = append(places, filepath.Join(f, name))
		}
		for _, p := range places {
			if _, err := w.lstat(ctx, p); !absent(err) {
				// There, or it cannot be told.
				return engine.Item{}, false, false, nil
			}
		}
		roots := append([]string{appdir}, folders...)
		if filepath.IsAbs(app) {
			roots = append(roots, filepath.Dir(app))
		}
		for _, r := range roots {
			if found, told := scan.has(ctx, r, filepath.Base(name)); found || !told {
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

// appScan lists each folder's app bundles and those one folder down
// once per brew listing, by folded name; nil for a folder that could
// not all be listed.
type appScan struct {
	w     *walker
	names map[string]map[string]bool
}

// has reports whether an app called name is in folder or one folder
// down; told is false when that cannot be read.
func (s *appScan) has(ctx context.Context, folder, name string) (found, told bool) {
	names, seen := s.names[folder]
	if !seen {
		paths, complete := appBundles(ctx, s.w, folder)
		if complete {
			names = map[string]bool{}
			for _, p := range paths {
				names[foldName(filepath.Base(p))] = true
			}
		}
		s.names[folder] = names
	}
	return names[foldName(name)], names != nil
}

// resolvedTarget is the absolute path Homebrew 7 resolved for an
// artifact, "" when brew printed none. told is false for one with a
// `..` in it, which a link can send anywhere: the cask is left out.
func resolvedTarget(art map[string]json.RawMessage, home string) (target string, told bool, err error) {
	raw, ok := art["target"]
	if !ok {
		return "", true, nil
	}
	if err := json.Unmarshal(raw, &target); err != nil {
		return "", false, errors.New("an artifact target regrow cannot read")
	}
	if rest, ok := strings.CutPrefix(target, "~/"); ok {
		target = filepath.Join(home, rest)
	}
	if !filepath.IsAbs(target) {
		return "", false, fmt.Errorf("an artifact target that is not a whole path: %q", target)
	}
	if climbs(target) {
		return "", false, nil
	}
	return filepath.Clean(target), true, nil
}

// appTarget is where an app artifact puts the app: brew's resolved
// target when it printed one; else the target option, or the source's
// own name, relative to the app folder unless absolute or under ~.
func appTarget(raw json.RawMessage, resolved string) (string, error) {
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil || len(parts) == 0 {
		return "", errors.New("an app artifact regrow cannot read")
	}
	var source string
	if err := json.Unmarshal(parts[0], &source); err != nil || source == "" {
		return "", errors.New("an app artifact with no name")
	}
	if resolved != "" {
		return resolved, nil
	}
	name := filepath.Base(source)
	if target := targetOption(parts); target != "" {
		name = target
	}
	if climbs(name) || name == "." || name == "/" {
		return "", fmt.Errorf("an app target that climbs out of its folder: %q", name)
	}
	return filepath.Clean(name), nil
}

// targetOption is the target an artifact's options name, "" for none.
func targetOption(parts []json.RawMessage) string {
	target := ""
	for _, p := range parts[1:] {
		var opts struct {
			Target string `json:"target"`
		}
		if json.Unmarshal(p, &opts) == nil && opts.Target != "" {
			target = opts.Target
		}
	}
	return target
}

func climbs(path string) bool {
	return slices.Contains(strings.Split(filepath.ToSlash(path), "/"), "..")
}

// linkNeedsPassword reports whether brew would use sudo to remove a
// link artifact: its link is there, in a folder the owner cannot write.
// One whose place cannot be told may.
func (c *caskRecords) linkNeedsPassword(ctx context.Context, raw json.RawMessage, resolved string) bool {
	target := resolved
	if target == "" {
		var parts []json.RawMessage
		if err := json.Unmarshal(raw, &parts); err != nil || len(parts) == 0 {
			return true
		}
		target = targetOption(parts)
		if rest, ok := strings.CutPrefix(target, "~/"); ok {
			target = filepath.Join(c.home(), rest)
		}
		if !filepath.IsAbs(target) {
			return false
		}
	}
	w := c.walker()
	if _, err := w.lstat(ctx, target); absent(err) {
		return false
	} else if err != nil {
		return true
	}
	dir := filepath.Dir(target)
	_, err := bounded(ctx, w, dir, func() (struct{}, error) { return struct{}{}, syscall.Access(dir, accessWrite) })
	return err != nil
}

// accessWrite is access(2)'s W_OK.
const accessWrite = 0x2

// stepsNeedPassword reports whether install steps may use sudo, as
// brew judges them: a step marked sudo, or one that sets ownership or
// deletes a keychain certificate. Steps that cannot be read may.
func stepsNeedPassword(raw json.RawMessage) bool {
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return true
	}
	for _, b := range blocks {
		var steps []struct {
			Sudo json.RawMessage `json:"sudo"`
			Type string          `json:"type"`
		}
		if len(b) != 1 || json.Unmarshal(b["steps"], &steps) != nil || steps == nil {
			return true
		}
		for _, st := range steps {
			sudo := strings.TrimSpace(string(st.Sudo))
			if (sudo != "" && sudo != "false" && sudo != "null") || st.Type == "set_ownership" || st.Type == "delete_keychain_certificate" {
				return true
			}
		}
	}
	return false
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
