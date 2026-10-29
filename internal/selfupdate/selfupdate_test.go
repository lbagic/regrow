package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const asset = "regrow_darwin_arm64.tar.gz"

// script is a stand-in binary: it answers `version` as a release would.
func script(v string) []byte {
	return []byte("#!/bin/sh\necho \"regrow " + v + "\"\n")
}

func archiveOf(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		body []byte
	}{{"README.md", []byte("readme")}, {name, body}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(f.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeRelease serves a latest-release record and its assets.
type fakeRelease struct {
	tag     string
	files   map[string][]byte
	hang    chan struct{} // when set, every request waits for it
	hits    atomic.Int32
	records atomic.Int32
}

func newRelease(t *testing.T, tag string, bin []byte) *fakeRelease {
	t.Helper()
	archive := archiveOf(t, "regrow", bin)
	sum := sha256.Sum256(archive)
	return &fakeRelease{tag: tag, files: map[string][]byte{
		asset:           archive,
		"checksums.txt": []byte(hex.EncodeToString(sum[:]) + "  " + asset + "\n"),
	}}
}

func (f *fakeRelease) serve(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		if f.hang != nil {
			select {
			case <-f.hang:
			case <-r.Context().Done():
			}
			return
		}
		if r.URL.Path == "/latest" {
			f.records.Add(1)
			type a struct {
				Name string `json:"name"`
				URL  string `json:"browser_download_url"`
			}
			rec := struct {
				Tag    string `json:"tag_name"`
				Assets []a    `json:"assets"`
			}{Tag: f.tag}
			for name := range f.files {
				rec.Assets = append(rec.Assets, a{name, srv.URL + "/download/" + f.tag + "/" + name})
			}
			_ = json.NewEncoder(w).Encode(rec)
			return
		}
		body, ok := f.files[strings.TrimPrefix(r.URL.Path, "/download/"+f.tag+"/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// installed lays out a temp HOME with a release build at the install
// line's path and returns an Updater for it.
func installed(t *testing.T, current string, srv *httptest.Server) Updater {
	t.Helper()
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(bin, "regrow")
	if err := os.WriteFile(exe, script(current), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 29, 14, 0, 0, 0, time.UTC)
	return Updater{
		Current:   current,
		Exe:       exe,
		Home:      home,
		StateDir:  filepath.Join(home, ".local", "state", "regrow"),
		LatestURL: srv.URL + "/latest",
		OS:        "darwin",
		Arch:      "arm64",
		Client:    srv.Client(),
		Now:       func() time.Time { return now },
	}
}

func binaryAt(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// onlyBinary checks that the update left nothing beside the binary.
func onlyBinary(t *testing.T, u Updater) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(u.Exe))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "regrow" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("bin dir holds %v, want only regrow", names)
	}
}

func TestUpdateNewer(t *testing.T) {
	rel := newRelease(t, "v0.2.1", script("0.2.1"))
	u := installed(t, "0.1.0", rel.serve(t))
	res, err := u.Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res != (Result{Status: Updated, From: "0.1.0", To: "0.2.1"}) {
		t.Fatalf("result = %+v", res)
	}
	if got := binaryAt(t, u.Exe); got != string(script("0.2.1")) {
		t.Fatalf("binary = %q, want the release's", got)
	}
	info, err := os.Stat(u.Exe)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0755", info.Mode().Perm())
	}
	onlyBinary(t, u)
}

func TestUpdateSameOrOlderDownloadsNothing(t *testing.T) {
	for _, tc := range []struct {
		tag  string
		want Status
	}{{"v0.1.0", UpToDate}, {"v0.0.9", Ahead}} {
		t.Run(tc.tag, func(t *testing.T) {
			rel := newRelease(t, tc.tag, script(strings.TrimPrefix(tc.tag, "v")))
			u := installed(t, "0.1.0", rel.serve(t))
			res, err := u.Update(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != tc.want {
				t.Fatalf("status = %v, want %v", res.Status, tc.want)
			}
			if n := rel.hits.Load(); n != 1 {
				t.Fatalf("%d requests, want the record alone", n)
			}
			if got := binaryAt(t, u.Exe); got != string(script("0.1.0")) {
				t.Fatalf("binary changed to %q", got)
			}
		})
	}
}

func TestUpdateOffline(t *testing.T) {
	rel := newRelease(t, "v0.2.1", script("0.2.1"))
	srv := rel.serve(t)
	u := installed(t, "0.1.0", srv)
	srv.Close()
	if _, err := u.Update(context.Background()); err == nil {
		t.Fatal("an unreachable server must be an error")
	}
	var out bytes.Buffer
	u.Start(context.Background()).Finish(&out, time.Second)
	if out.Len() != 0 {
		t.Fatalf("offline automatic check said %q, want nothing", out.String())
	}
	if got := binaryAt(t, u.Exe); got != string(script("0.1.0")) {
		t.Fatalf("binary changed to %q", got)
	}
}

func TestUpdateRefusesBadChecksum(t *testing.T) {
	rel := newRelease(t, "v0.2.1", script("0.2.1"))
	rel.files["checksums.txt"] = []byte(strings.Repeat("0", 64) + "  " + asset + "\n")
	u := installed(t, "0.1.0", rel.serve(t))
	_, err := u.Update(context.Background())
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err = %v, want a checksum mismatch", err)
	}
	if got := binaryAt(t, u.Exe); got != string(script("0.1.0")) {
		t.Fatalf("binary changed to %q", got)
	}
	onlyBinary(t, u)
}

func TestUpdateRefusesChecksumListingWithoutAsset(t *testing.T) {
	rel := newRelease(t, "v0.2.1", script("0.2.1"))
	rel.files["checksums.txt"] = []byte(strings.Repeat("0", 64) + "  regrow_darwin_amd64.tar.gz\n")
	u := installed(t, "0.1.0", rel.serve(t))
	if _, err := u.Update(context.Background()); err == nil || !strings.Contains(err.Error(), "no line for") {
		t.Fatalf("err = %v, want a missing checksum line", err)
	}
	if got := binaryAt(t, u.Exe); got != string(script("0.1.0")) {
		t.Fatalf("binary changed to %q", got)
	}
}

func TestUpdateRefusesBinaryThatDoesNotRunOrLies(t *testing.T) {
	for name, bin := range map[string][]byte{
		"fails":         []byte("#!/bin/sh\nexit 1\n"),
		"wrong version": script("0.2.0"),
	} {
		t.Run(name, func(t *testing.T) {
			rel := newRelease(t, "v0.2.1", bin)
			u := installed(t, "0.1.0", rel.serve(t))
			if _, err := u.Update(context.Background()); err == nil {
				t.Fatal("want an error")
			}
			if got := binaryAt(t, u.Exe); got != string(script("0.1.0")) {
				t.Fatalf("binary changed to %q", got)
			}
			onlyBinary(t, u)
		})
	}
}

func TestUpdateMissingAsset(t *testing.T) {
	rel := newRelease(t, "v0.2.1", script("0.2.1"))
	u := installed(t, "0.1.0", rel.serve(t))
	u.Arch = "riscv64"
	if _, err := u.Update(context.Background()); err == nil || !strings.Contains(err.Error(), "regrow_darwin_riscv64.tar.gz") {
		t.Fatalf("err = %v, want the missing asset named", err)
	}
}

func TestUpdateSkipsBinariesItDoesNotOwn(t *testing.T) {
	cases := map[string]func(t *testing.T, u *Updater){
		"dev build":      func(t *testing.T, u *Updater) { u.Current = "0.0.0-dev" },
		"snapshot build": func(t *testing.T, u *Updater) { u.Current = "0.0.0-SNAPSHOT-674a881" },
		"go install": func(t *testing.T, u *Updater) {
			gobin := filepath.Join(u.Home, "go", "bin")
			if err := os.MkdirAll(gobin, 0o755); err != nil {
				t.Fatal(err)
			}
			u.Exe = filepath.Join(gobin, "regrow")
			if err := os.WriteFile(u.Exe, script(u.Current), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"symlink": func(t *testing.T, u *Updater) {
			real := filepath.Join(u.Home, "elsewhere")
			if err := os.Rename(u.Exe, real); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, u.Exe); err != nil {
				t.Fatal(err)
			}
			u.Exe = real
		},
		"non-writable folder": func(t *testing.T, u *Updater) {
			if os.Geteuid() == 0 {
				t.Skip("root writes any folder")
			}
			dir := filepath.Dir(u.Exe)
			if err := os.Chmod(dir, 0o555); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			rel := newRelease(t, "v0.2.1", script("0.2.1"))
			u := installed(t, "0.1.0", rel.serve(t))
			setup(t, &u)
			before := binaryAt(t, u.Exe)
			if _, err := u.Update(context.Background()); err == nil {
				t.Fatal("want a refusal")
			}
			var out bytes.Buffer
			u.Start(context.Background()).Finish(&out, time.Second)
			if out.Len() != 0 {
				t.Fatalf("automatic check said %q, want nothing", out.String())
			}
			if n := rel.hits.Load(); n != 0 {
				t.Fatalf("%d requests, want none", n)
			}
			if got := binaryAt(t, u.Exe); got != before {
				t.Fatalf("binary changed to %q", got)
			}
			if _, err := os.Stat(filepath.Join(u.StateDir, stateFile)); err == nil {
				t.Fatal("a skipped binary must not record a check")
			}
		})
	}
}

func TestStartSaysOneLine(t *testing.T) {
	rel := newRelease(t, "v0.2.1", script("0.2.1"))
	u := installed(t, "0.1.0", rel.serve(t))
	var out bytes.Buffer
	u.Start(context.Background()).Finish(&out, 5*time.Second)
	if got, want := out.String(), "regrow updated to 0.2.1; the next run uses it\n"; got != want {
		t.Fatalf("said %q, want %q", got, want)
	}
	if got := binaryAt(t, u.Exe); got != string(script("0.2.1")) {
		t.Fatalf("binary = %q, want the release's", got)
	}
}

func TestStartOncePerInterval(t *testing.T) {
	rel := newRelease(t, "v0.1.0", script("0.1.0"))
	u := installed(t, "0.1.0", rel.serve(t))
	now := u.Now()
	for i, tc := range []struct {
		at      time.Duration
		records int32
	}{
		{0, 1},
		{time.Hour, 1},
		{Interval - time.Second, 1},
		{Interval, 2},
		{-time.Hour, 3}, // a clock set back does not hold the check off
	} {
		u.Now = func() time.Time { return now.Add(tc.at) }
		var out bytes.Buffer
		u.Start(context.Background()).Finish(&out, 5*time.Second)
		if out.Len() != 0 {
			t.Fatalf("run %d said %q", i, out.String())
		}
		if got := rel.records.Load(); got != tc.records {
			t.Fatalf("run %d at %v: %d checks, want %d", i, tc.at, got, tc.records)
		}
	}
}

func TestStartOff(t *testing.T) {
	rel := newRelease(t, "v0.2.1", script("0.2.1"))
	u := installed(t, "0.1.0", rel.serve(t))
	u.Off = true
	u.Start(context.Background()).Finish(&bytes.Buffer{}, time.Second)
	if n := rel.hits.Load(); n != 0 {
		t.Fatalf("%d requests with the check off", n)
	}
}

func TestFinishGivesUpOnASilentNetwork(t *testing.T) {
	rel := newRelease(t, "v0.2.1", script("0.2.1"))
	rel.hang = make(chan struct{})
	u := installed(t, "0.1.0", rel.serve(t))
	t.Cleanup(func() { close(rel.hang) }) // before the server's Close, which waits for handlers
	var out bytes.Buffer
	start := time.Now()
	u.Start(context.Background()).Finish(&out, 50*time.Millisecond)
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("Finish took %v on a silent network", took)
	}
	if out.Len() != 0 {
		t.Fatalf("said %q, want nothing", out.String())
	}
	if got := binaryAt(t, u.Exe); got != string(script("0.1.0")) {
		t.Fatalf("binary changed to %q", got)
	}
	if u.due() {
		t.Fatal("a check given up on still counts for the day")
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv(OffEnv, "1")
	t.Setenv(URLEnv, "http://127.0.0.1:1/latest")
	u, err := FromEnv("0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if !u.Off || u.LatestURL != "http://127.0.0.1:1/latest" {
		t.Fatalf("Off = %v, LatestURL = %q", u.Off, u.LatestURL)
	}
	t.Setenv(OffEnv, "")
	t.Setenv(URLEnv, "")
	if u, err = FromEnv("0.1.0"); err != nil {
		t.Fatal(err)
	}
	if u.Off || u.LatestURL != DefaultLatestURL {
		t.Fatalf("Off = %v, LatestURL = %q", u.Off, u.LatestURL)
	}
}

func TestParseVersion(t *testing.T) {
	for s, want := range map[string]bool{
		"0.1.0": true, "1.20.3": true,
		"0.0.0-dev": false, "0.2.0-rc1": false, "v0.1.0": false, "0.1": false, "0.1.0.1": false, "0.+1.0": false, "": false,
	} {
		if _, ok := parseVersion(s); ok != want {
			t.Errorf("parseVersion(%q) ok = %v, want %v", s, ok, want)
		}
	}
	a, _ := parseVersion("0.10.0")
	b, _ := parseVersion("0.9.9")
	if a.compare(b) != 1 || b.compare(a) != -1 || a.compare(a) != 0 {
		t.Fatal("0.10.0 must sort after 0.9.9")
	}
}
