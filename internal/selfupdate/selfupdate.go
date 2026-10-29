// Package selfupdate keeps an installed regrow at its latest GitHub
// release. It replaces only the binary the install line puts at
// ~/.local/bin/regrow, only from a release build, and only with an
// archive whose sha256 matches the release's checksums.txt.
package selfupdate

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lbagic/regrow/internal/oplog"
)

const (
	// DefaultLatestURL is GitHub's record of the latest release, which
	// never names a draft or a prerelease.
	DefaultLatestURL = "https://api.github.com/repos/lbagic/regrow/releases/latest"
	// OffEnv set to any non-empty value turns the automatic check off;
	// `regrow update` still runs.
	OffEnv = "REGROW_NO_AUTO_UPDATE"
	// URLEnv replaces DefaultLatestURL, for a local fake release.
	URLEnv = "REGROW_UPDATE_URL"

	// Interval is the least time between two automatic checks.
	Interval = 24 * time.Hour
	// ExitGrace is how long a command that has finished waits for an
	// automatic check still under way.
	ExitGrace = 2 * time.Second

	stateFile     = "update.json"
	checksumsName = "checksums.txt"
	binaryName    = "regrow"

	maxRecord    = 1 << 20
	maxChecksums = 64 << 10
	maxArchive   = 256 << 20

	recordTimeout  = 30 * time.Second
	archiveTimeout = 10 * time.Minute
	runTimeout     = 5 * time.Second
)

// Updater checks for and installs a newer release over one binary.
type Updater struct {
	Current   string // the running build's version, "0.1.0"
	Exe       string // the running binary, as os.Executable gives it
	Home      string
	StateDir  string
	LatestURL string
	OS, Arch  string
	UID       int // the effective user, who must own the binary
	Off       bool
	Client    *http.Client
	Now       func() time.Time
}

// FromEnv is the Updater for this process.
func FromEnv(current string) (Updater, error) {
	exe, err := os.Executable()
	if err != nil {
		return Updater{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Updater{}, err
	}
	state, err := oplog.StateDir()
	if err != nil {
		return Updater{}, err
	}
	latest := DefaultLatestURL
	if v := os.Getenv(URLEnv); v != "" {
		latest = v
	}
	return Updater{
		Current:   current,
		Exe:       exe,
		Home:      home,
		StateDir:  state,
		LatestURL: latest,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		UID:       os.Geteuid(),
		Off:       os.Getenv(OffEnv) != "",
		Client:    &http.Client{},
		Now:       time.Now,
	}, nil
}

// Status is what Update found.
type Status int

const (
	UpToDate Status = iota + 1 // the running version is the latest release
	Ahead                      // the running version is newer than the latest release
	Updated                    // the binary now holds the latest release
)

// Result is what Update did.
type Result struct {
	Status   Status
	From, To string
}

// Owned returns the path Update would replace, or why it replaces
// nothing: a build that is not a release (a `go install` build carries
// 0.0.0-dev too), a binary anywhere but ~/.local/bin/regrow, a symlink
// there, or a file or folder this user cannot replace.
func (u Updater) Owned() (string, error) {
	if _, ok := parseVersion(u.Current); !ok {
		return "", fmt.Errorf("this regrow is a development build (%s); only a release installed with the install line updates itself", u.Current)
	}
	dir, err := filepath.EvalSymlinks(filepath.Join(u.Home, ".local", "bin"))
	if err != nil {
		return "", fmt.Errorf("this regrow (%s) was not installed with the install line, which puts it in ~/.local/bin; update it the way it was installed", u.Exe)
	}
	target := filepath.Join(dir, binaryName)
	exe, err := filepath.EvalSymlinks(u.Exe)
	if err != nil || exe != target {
		return "", fmt.Errorf("this regrow (%s) is not ~/.local/bin/regrow, where the install line puts it; update it the way it was installed", u.Exe)
	}
	info, err := os.Lstat(target)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a plain file; it is left as it is", target)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != u.UID {
		return "", fmt.Errorf("%s belongs to another user; it is left as it is", target)
	}
	if err := unix.Access(dir, unix.W_OK); err != nil {
		return "", fmt.Errorf("%s is not writable, so regrow cannot replace itself there", dir)
	}
	return target, nil
}

// Update replaces the binary with the latest release when that release
// is newer. Any failure leaves the binary as it was.
func (u Updater) Update(ctx context.Context) (Result, error) {
	target, err := u.Owned()
	if err != nil {
		return Result{}, err
	}
	current, _ := parseVersion(u.Current)
	rel, err := u.latest(ctx)
	if err != nil {
		return Result{}, err
	}
	res := Result{From: u.Current, To: rel.version.String()}
	switch c := rel.version.compare(current); {
	case c == 0:
		res.Status = UpToDate
		return res, nil
	case c < 0:
		res.Status = Ahead
		return res, nil
	}

	asset := "regrow_" + u.OS + "_" + u.Arch + ".tar.gz"
	sumsURL, ok := rel.assets[checksumsName]
	if !ok {
		return Result{}, fmt.Errorf("release %s has no %s", rel.tag, checksumsName)
	}
	archiveURL, ok := rel.assets[asset]
	if !ok {
		return Result{}, fmt.Errorf("release %s has no %s", rel.tag, asset)
	}
	sums, err := u.fetch(ctx, sumsURL, maxChecksums, recordTimeout)
	if err != nil {
		return Result{}, err
	}
	want, err := checksumFor(sums, asset)
	if err != nil {
		return Result{}, fmt.Errorf("release %s: %w", rel.tag, err)
	}
	archive, err := u.fetch(ctx, archiveURL, maxArchive, archiveTimeout)
	if err != nil {
		return Result{}, err
	}
	if got := sha256.Sum256(archive); hex.EncodeToString(got[:]) != want {
		return Result{}, fmt.Errorf("%s of release %s does not match its %s; nothing was changed", asset, rel.tag, checksumsName)
	}
	bin, err := binaryFrom(archive)
	if err != nil {
		return Result{}, fmt.Errorf("%s of release %s: %w", asset, rel.tag, err)
	}
	// A caller that gave up before this point gets nothing installed; one
	// that gives up later waits for the install, which is local and short.
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := install(context.WithoutCancel(ctx), target, bin, res.To); err != nil {
		return Result{}, err
	}
	res.Status = Updated
	return res, nil
}

type release struct {
	tag     string
	version version
	assets  map[string]string // name → download URL
}

func (u Updater) latest(ctx context.Context) (release, error) {
	body, err := u.fetch(ctx, u.LatestURL, maxRecord, recordTimeout)
	if err != nil {
		return release{}, err
	}
	var rec struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &rec); err != nil {
		return release{}, fmt.Errorf("the latest release record: %w", err)
	}
	v, ok := parseVersion(strings.TrimPrefix(rec.Tag, "v"))
	if !ok || !strings.HasPrefix(rec.Tag, "v") {
		return release{}, fmt.Errorf("the latest release's tag %q is not a version", rec.Tag)
	}
	rel := release{tag: rec.Tag, version: v, assets: map[string]string{}}
	for _, a := range rec.Assets {
		rel.assets[a.Name] = a.URL
	}
	return rel, nil
}

func (u Updater) fetch(ctx context.Context, url string, limit int64, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "regrow/"+u.Current)
	resp, err := u.Client.Do(req)
	if err != nil {
		var ue *neturl.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("could not reach %s: %w", req.URL.Host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("download of %s: %w", url, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", url, limit)
	}
	return body, nil
}

// checksumFor reads a `sha256sum` listing: "<hex>  <name>" per line.
func checksumFor(sums []byte, name string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && fields[1] == name {
			sum := strings.ToLower(fields[0])
			if b, err := hex.DecodeString(sum); err != nil || len(b) != sha256.Size {
				return "", fmt.Errorf("%s's line for %s is not a sha256", checksumsName, name)
			}
			return sum, nil
		}
	}
	return "", fmt.Errorf("%s has no line for %s", checksumsName, name)
}

func binaryFrom(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("holds no %s", binaryName)
		}
		if err != nil {
			return nil, err
		}
		if path.Clean(h.Name) != binaryName || h.Typeflag != tar.TypeReg {
			continue
		}
		bin, err := io.ReadAll(io.LimitReader(tr, maxArchive+1))
		if err != nil {
			return nil, err
		}
		if len(bin) > maxArchive {
			return nil, fmt.Errorf("its %s is larger than %d bytes", binaryName, maxArchive)
		}
		return bin, nil
	}
}

// install writes bin beside target, checks that it runs and that its
// `version` line starts "regrow <want>", then renames it over target: a
// running regrow keeps the file it started from, and the next run
// starts the new one.
func install(ctx context.Context, target string, bin []byte, want string) (err error) {
	f, err := os.CreateTemp(filepath.Dir(target), ".regrow-update-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(bin); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(0o755); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, tmp, "version")
	cmd.Env = append(os.Environ(), OffEnv+"=1")
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("the downloaded regrow did not answer `version` within %v; nothing was changed", runTimeout)
	}
	if err != nil {
		return fmt.Errorf("the downloaded regrow does not run on this Mac (%v); nothing was changed", err)
	}
	if !SaysVersion(string(out), want) {
		line, _, _ := strings.Cut(string(out), "\n")
		return fmt.Errorf("the downloaded regrow says %q, not version %s; nothing was changed", strings.TrimSpace(line), want)
	}
	return os.Rename(tmp, target)
}

// SaysVersion reports whether out, what `regrow version` printed, names
// version v: the first two words of its first line are "regrow" and v.
// Every installed release checks a new one this way, so a later release
// may add words after these two but never change them.
func SaysVersion(out, v string) bool {
	line, _, _ := strings.Cut(out, "\n")
	words := strings.Fields(line)
	return len(words) >= 2 && words[0] == binaryName && words[1] == v
}

// Run is an automatic check started by Start.
type Run struct {
	cancel context.CancelFunc
	done   chan struct{}
	res    Result
	err    error
}

// Start begins the automatic check in the background, unless it is
// off, Owned refuses the binary, or the last one began less than
// Interval ago. A check counts from the moment it begins, so a network
// that never answers costs at most one ExitGrace a day.
func (u Updater) Start(ctx context.Context) *Run {
	r := &Run{done: make(chan struct{})}
	if u.Off || !u.due() {
		close(r.done)
		return r
	}
	if _, err := u.Owned(); err != nil || u.markChecked() != nil {
		close(r.done)
		return r
	}
	ctx, r.cancel = context.WithCancel(ctx)
	go func() {
		defer close(r.done)
		r.res, r.err = u.Update(ctx)
	}()
	return r
}

// Finish waits up to grace for the check, then stops its downloads; an
// install already under way runs to its end. It writes one line to w
// when the binary was replaced, and nothing in every other case.
func (r *Run) Finish(w io.Writer, grace time.Duration) {
	if r.cancel == nil {
		return
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-r.done:
	case <-timer.C:
	}
	r.cancel()
	<-r.done
	if r.err == nil && r.res.Status == Updated {
		_, _ = fmt.Fprintf(w, "regrow updated to %s; the next run uses it\n", r.res.To)
	}
}

type checkState struct {
	CheckedAt time.Time `json:"checked_at"`
}

func (u Updater) due() bool {
	data, err := os.ReadFile(filepath.Join(u.StateDir, stateFile))
	if err != nil {
		return true
	}
	var s checkState
	if json.Unmarshal(data, &s) != nil {
		return true
	}
	now := u.Now()
	return now.Before(s.CheckedAt) || now.Sub(s.CheckedAt) >= Interval
}

func (u Updater) markChecked() error {
	if err := os.MkdirAll(u.StateDir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(checkState{CheckedAt: u.Now().UTC()})
	if err != nil {
		return err
	}
	p := filepath.Join(u.StateDir, stateFile)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// version is a release's major.minor.patch; a prerelease or build
// suffix is not a release.
type version [3]int

func parseVersion(s string) (version, bool) {
	var v version
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		if p == "" || strings.TrimLeft(p, "0123456789") != "" || (len(p) > 1 && p[0] == '0') {
			return v, false
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

func (v version) compare(o version) int {
	for i := range v {
		if v[i] != o[i] {
			if v[i] < o[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func (v version) String() string { return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2]) }
