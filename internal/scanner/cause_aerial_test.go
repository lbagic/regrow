package scanner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

func TestAerialDownloads(t *testing.T) {
	rule := embeddedRule(t, "aerial-wallpapers")
	video := func(t *testing.T, path string, age time.Duration) {
		t.Helper()
		writeFile(t, path, 1<<20)
		at := causeNow.Add(-age)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	// What idleassetsd keeps beside the videos; it is rewritten without
	// any download, so it must never read as one.
	catalogue := func(t *testing.T, customer string) {
		t.Helper()
		writeFile(t, filepath.Join(customer, "entries.json"), 130_000)
		writeFile(t, filepath.Join(customer, "resources.tar"), 2_700_000)
		writeFile(t, filepath.Join(customer, "4KSDR240FPS", "notes.txt"), 10)
	}

	tests := []struct {
		name    string
		plant   func(t *testing.T, customer string)
		want    engine.Verdict
		detail  []string
		without string
	}{
		{
			name: "videos left by an earlier batch",
			plant: func(t *testing.T, customer string) {
				catalogue(t, customer)
				video(t, filepath.Join(customer, "4KSDR240FPS", "a.mov"), 80*time.Hour)
				video(t, filepath.Join(customer, "4KSDR240FPS", "b.MOV"), 72*time.Hour)
			},
			want:    engine.VerdictFlagged,
			detail:  []string{"2 videos, 2.0 MiB", "newest written 3 d ago"},
			without: "downloading",
		},
		{
			name: "a batch coming in",
			plant: func(t *testing.T, customer string) {
				catalogue(t, customer)
				video(t, filepath.Join(customer, "4KSDR240FPS", "a.mov"), 2*time.Hour)
				video(t, filepath.Join(customer, "4KSDR", "b.mov"), 3*time.Minute)
			},
			want:   engine.VerdictFlagged,
			detail: []string{"2 videos, 2.0 MiB", "newest written 3 min ago, so a batch is downloading now"},
		},
		{
			name: "one video",
			plant: func(t *testing.T, customer string) {
				video(t, filepath.Join(customer, "4KSDR240FPS", "a.mov"), time.Hour)
			},
			want:   engine.VerdictFlagged,
			detail: []string{"1 video, 1.0 MiB"},
		},
		{
			name:   "only the catalogue files",
			plant:  catalogue,
			want:   engine.VerdictNormal,
			detail: []string{"no aerial videos on disk"},
		},
		{
			name:   "no folder",
			plant:  func(*testing.T, string) {},
			want:   engine.VerdictNormal,
			detail: []string{"does not apply"},
		},
		{
			name: "a folder that cannot be read, no video seen",
			plant: func(t *testing.T, customer string) {
				catalogue(t, customer)
				video(t, filepath.Join(customer, "4KSDR", "hidden.mov"), time.Hour)
				chmod(t, filepath.Join(customer, "4KSDR"), 0)
			},
			want:   engine.VerdictUnknown,
			detail: []string{"Full Disk Access may be needed"},
		},
		{
			name: "a folder that cannot be read beside a video",
			plant: func(t *testing.T, customer string) {
				video(t, filepath.Join(customer, "4KSDR240FPS", "a.mov"), 80*time.Hour)
				video(t, filepath.Join(customer, "4KSDR", "hidden.mov"), time.Minute)
				chmod(t, filepath.Join(customer, "4KSDR"), 0)
			},
			want:   engine.VerdictFlagged,
			detail: []string{"1 video, ≥ 1.0 MiB"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if os.Geteuid() == 0 && strings.Contains(tt.name, "cannot be read") {
				t.Skip("root reads mode-000 folders")
			}
			c := causeFixture(t)
			tt.plant(t, filepath.Join(c.host.Root, "Library/Application Support/com.apple.idleassetsd/Customer"))
			verdict, detail := c.aerialDownloads(context.Background(), rule)
			wantCause(t, verdict, detail, tt.want, tt.detail...)
			if tt.without != "" && strings.Contains(detail, tt.without) {
				t.Errorf("detail %q must not say %q", detail, tt.without)
			}
		})
	}
}

// The videos are deleted as root, and only while nothing is picked:
// with picks, or on macOS 26 where none were measured, the row is shown
// only, with why, and naming it plans nothing. Offered, it is never
// pre-selected, and naming it plans one measured command for both
// items, rechecked first.
func TestAerialWallpapersHeldWhilePicked(t *testing.T) {
	rule := embeddedRule(t, "aerial-wallpapers")
	for _, tc := range []struct {
		name, version, folder, video string
		picks                        int
		held                         string
	}{
		{"picks on", "15.5", "/Library/Application Support/com.apple.idleassetsd/Customer", "4KSDR240FPS/a.mov", 2, "To clear the picks first: `regrow aerials off`"},
		{"macOS 26", "26.0", "~/Library/Containers/com.apple.wallpaper.agent/Data/Library/Caches", "a.mov", -1, "macOS 26.0"},
		{"nothing picked", "15.5", "/Library/Application Support/com.apple.idleassetsd/Customer", "4KSDR240FPS/a.mov", 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := fixtureHost(t, rule)
			host.Version = tc.version
			if tc.picks >= 0 {
				aerialsFixture(t, host.Root, tc.picks)
			}
			folder := host.ExpandPath(tc.folder)
			writeFile(t, filepath.Join(folder, tc.video), 1<<20)
			// New binds the query to the host it scans.
			findings := New(host).Scan(context.Background(), []engine.Rule{rule})
			f := findings[0]
			if f.Err != "" || len(f.Items) == 0 || f.Items[0].Path != folder {
				t.Fatalf("finding %+v, want the fixture's folder %s found", f, folder)
			}
			if sel := engine.DefaultSelection(findings); len(sel) != 0 {
				t.Errorf("default selection %v, want nothing", sel)
			}
			plan := engine.BuildPlan(host, findings, map[string]bool{rule.ID: true})
			if tc.held != "" {
				if f.Rule.Risk != engine.RiskSurfaceOnly || !strings.Contains(f.Held, tc.held) || f.Rule.Note != f.Held {
					t.Errorf("risk %q, held %q, note %q; want it shown only, saying %q", f.Rule.Risk, f.Held, f.Rule.Note, tc.held)
				}
				if len(plan.Actions) != 0 || len(plan.Skipped) != 1 || plan.Skipped[0].Reason != f.Held {
					t.Errorf("plan by id: actions %+v, skipped %+v; want no action and one skip naming the hold", plan.Actions, plan.Skipped)
				}
				return
			}
			if f.Held != "" || f.Rule.Risk != engine.RiskCaution {
				t.Fatalf("held %q, risk %q; want it offered as caution", f.Held, f.Rule.Risk)
			}
			if len(plan.Actions) != 1 || len(plan.Skipped) != 0 {
				t.Fatalf("plan by id: actions %+v, skipped %+v; want one action", plan.Actions, plan.Skipped)
			}
			a := plan.Actions[0]
			if a.Kind != engine.ActionNative || !a.Sudo || a.Command[0] != "sudo" || a.PreAction != engine.PreActionHoldRecheck || a.Measure != engine.MeasureAerialVideos || a.Bytes != f.TotalBytes() {
				t.Errorf("action %+v, want sudo's measured command over all %d bytes, the hold checked again first", a, f.TotalBytes())
			}
		})
	}
}

// Deleting a video frees its bytes only when no link outside the folder
// holds the file too. The row sizes the folder by what deleting frees,
// and the files held elsewhere apart, so the two add up to the folder.
func TestAerialVideosSplitByLinks(t *testing.T) {
	host := engine.Host{OS: "darwin", Version: "15.7.7", Root: t.TempDir()}
	host.Home = filepath.Join(host.Root, "Users/dev")
	customer := filepath.Join(host.Root, "Library/Application Support/com.apple.idleassetsd/Customer")
	single := filepath.Join(customer, "4KSDR240FPS", "a.mov")
	heldOutside := filepath.Join(customer, "4KSDR240FPS", "b.mov")
	twiceInside := filepath.Join(customer, "4KSDR", "c.mov")
	writeFile(t, single, 1<<20)
	writeFile(t, heldOutside, 3<<20)
	writeFile(t, twiceInside, 2<<20)
	temp := filepath.Join(host.Root, "private/var/folders/zz/x/T/com.apple.idleassetsd")
	if err := os.MkdirAll(temp, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(heldOutside, filepath.Join(temp, "CFNetworkDownload_1.tmp")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(twiceInside, filepath.Join(customer, "4KSDR", "c-copy.mov")); err != nil {
		t.Fatal(err)
	}
	size := func(path string) int64 {
		fi, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		return physicalSize(fi)
	}
	// DirSize counts every link: c twice, b once.
	whole, err := DirSize(context.Background(), customer)
	if err != nil {
		t.Fatal(err)
	}

	items, err := (&aerialVideos{w: testWalker(), host: host}).Items(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items %+v, want the folder and the files held outside it", items)
	}
	folder, held := items[0], items[1]
	if folder.Path != customer || folder.Bytes != whole.Bytes-size(heldOutside)-size(twiceInside) {
		t.Errorf("folder item %+v, want %s with %d bytes", folder, customer, whole.Bytes-size(heldOutside)-size(twiceInside))
	}
	if held.Path != "" || held.Bytes != size(heldOutside) || !strings.HasPrefix(held.Label, "1 file a link outside the folder also holds") {
		t.Errorf("held item %+v, want b.mov's %d bytes with no path", held, size(heldOutside))
	}

	// With no link outside, one item holds it all.
	if err := os.Remove(filepath.Join(temp, "CFNetworkDownload_1.tmp")); err != nil {
		t.Fatal(err)
	}
	items, err = (&aerialVideos{w: testWalker(), host: host}).Items(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Bytes != whole.Bytes-size(twiceInside) {
		t.Errorf("items %+v, want the folder alone with %d bytes", items, whole.Bytes-size(twiceInside))
	}

	// macOS 26 keeps them in the wallpaper agent's container.
	host.Version = "26.0"
	if items, err := (&aerialVideos{w: testWalker(), host: host}).Items(context.Background()); err != nil || len(items) != 0 {
		t.Errorf("on 26 with only the 15 folder: items %+v, err %v; want nothing", items, err)
	}
	writeFile(t, filepath.Join(host.Home, "Library/Containers/com.apple.wallpaper.agent/Data/Library/Caches/a.mov"), 1<<20)
	if items, err := (&aerialVideos{w: testWalker(), host: host}).Items(context.Background()); err != nil || len(items) != 1 ||
		items[0].Path != filepath.Join(host.Home, "Library/Containers/com.apple.wallpaper.agent/Data/Library/Caches") {
		t.Errorf("on 26: items %+v, err %v; want the container's folder", items, err)
	}
}

// aerialConflicts mirrors the catalog's one-owner fence for the aerial
// folders, which live in the aerial-videos query rather than in a
// rule's paths: and so escape the fence. No rule may name one or reach
// inside one, and a rule holding one spells the shared prefix the same.
func aerialConflicts(catalog []engine.Rule) []string {
	var conflicts []string
	for _, r := range catalog {
		for _, e := range r.Paths["darwin"] {
			p := filepath.Clean(e.Path)
			lp := strings.ToLower(p)
			for _, a := range aerialPathEntries {
				lf := strings.ToLower(a.Path)
				matches := lp == lf || strings.HasPrefix(lp, lf+"/") ||
					strings.HasPrefix(lf, lp+"/") && !strings.HasPrefix(a.Path, p+"/")
				for dir := lf; !matches && strings.ContainsAny(p, "*?[") && dir != "/" && dir != "."; dir = filepath.Dir(dir) {
					matches, _ = filepath.Match(lp, dir)
				}
				if matches {
					conflicts = append(conflicts, r.ID+" names "+p+", against the aerial folder "+a.Path)
				}
			}
		}
	}
	return conflicts
}

func TestNoRuleNamesAnAerialFolder(t *testing.T) {
	catalog, err := engine.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range aerialConflicts(catalog) {
		t.Error(c)
	}
	rule := func(path string) []engine.Rule {
		return []engine.Rule{{ID: "x", Paths: map[string][]engine.PathEntry{"darwin": {{Path: path}}}}}
	}
	for path, want := range map[string]bool{
		"/Library/Application Support/com.apple.idleassetsd/Customer":             true,
		"/library/application support/com.apple.idleassetsd/customer/4KSDR240FPS": true,
		"~/Library/Containers/com.apple.wallpaper.agent/Data/Library/Caches/x":    true,
		"~/library/containers/com.apple.wallpaper.agent":                          true,
		"~/Library/Containers/*/Data/Library/Caches":                              true,
		"~/Library/Containers/com.apple.wallpaper.agent":                          false,
		"~/Library/Caches": false,
		"/Library/Application Support/com.apple.idleassetsd/snapshots": false,
	} {
		if got := len(aerialConflicts(rule(path))) > 0; got != want {
			t.Errorf("a rule naming %s: conflict %v, want %v", path, got, want)
		}
	}
}

// With part of the folder unreadable, a file whose other link may sit
// in the unread part is not called held outside.
func TestAerialVideosPartialWalk(t *testing.T) {
	host := engine.Host{OS: "darwin", Version: "15.7.7", Root: t.TempDir()}
	customer := filepath.Join(host.Root, "Library/Application Support/com.apple.idleassetsd/Customer")
	video := filepath.Join(customer, "4KSDR240FPS", "a.mov")
	writeFile(t, video, 1<<20)
	locked := filepath.Join(customer, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(video, filepath.Join(locked, "a.mov")); err != nil {
		t.Fatal(err)
	}
	chmod(t, locked, 0)
	items, err := (&aerialVideos{w: testWalker(), host: host}).Items(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !items[0].Partial || items[0].Path != customer {
		t.Fatalf("items %+v, want the folder alone, partial", items)
	}
	fi, err := os.Lstat(video)
	if err != nil {
		t.Fatal(err)
	}
	whole, err := DirSize(context.Background(), customer)
	if err != nil {
		t.Fatal(err)
	}
	if want := whole.Bytes - physicalSize(fi); items[0].Bytes != want {
		t.Errorf("folder bytes %d, want %d: the file whose other link went unread counts nowhere", items[0].Bytes, want)
	}
}

// The row's command deletes the videos in the folder and the
// downloader's temp files in root's temp folders, and nothing else. It
// runs here as the test's user, on fixture folders in place of the two
// it names.
func TestAerialVideosCommandDeletes(t *testing.T) {
	rule := embeddedRule(t, "aerial-wallpapers")
	argv := slices.Clone([]string(rule.NativeCommand))
	const customer, temps = "/Library/Application Support/com.apple.idleassetsd/Customer", "/private/var/folders/zz"
	if len(argv) != 6 || argv[0] != "/bin/sh" || argv[4] != customer || argv[5] != temps {
		t.Fatalf("command %q, want /bin/sh with the script, %s and %s", argv, customer, temps)
	}
	root := t.TempDir()
	argv[4], argv[5] = filepath.Join(root, "Customer"), filepath.Join(root, "zz")
	video := filepath.Join(argv[4], "4KSDR240FPS", "a.mov")
	linked := filepath.Join(argv[4], "4KSDR240FPS", "b.mov")
	own := filepath.Join(argv[5], "x", "T", "com.apple.idleassetsd")
	copyOfLinked := filepath.Join(own, "CFNetworkDownload_1.tmp")
	unfinished := filepath.Join(own, "CFNetworkDownload_2.tmp")
	kept := []string{
		filepath.Join(argv[4], "entries.json"),
		filepath.Join(own, "other.tmp"),
		filepath.Join(own, "sub", "CFNetworkDownload_3.tmp"),
		filepath.Join(argv[5], "y", "T", "com.apple.other", "CFNetworkDownload_4.tmp"),
		filepath.Join(root, "outside", "com.apple.idleassetsd", "CFNetworkDownload_5.tmp"),
	}
	for _, p := range append([]string{video, linked, unfinished}, kept...) {
		writeFile(t, p, 1000)
	}
	if err := os.Link(linked, copyOfLinked); err != nil {
		t.Fatal(err)
	}
	// Another account's T made a link elsewhere: find does not follow it.
	if err := os.MkdirAll(filepath.Join(argv[5], "svc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(argv[5], "svc", "T")); err != nil {
		t.Fatal(err)
	}
	// So may its com.apple.idleassetsd.
	if err := os.MkdirAll(filepath.Join(argv[5], "agent", "T"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside", "com.apple.idleassetsd"), filepath.Join(argv[5], "agent", "T", "com.apple.idleassetsd")); err != nil {
		t.Fatal(err)
	}
	// A folder root cannot list, as a data vault under zz is, stops
	// nothing.
	vault := filepath.Join(argv[5], "y", "0", "com.apple.vault.dv")
	writeFile(t, filepath.Join(vault, "db"), 1000)
	chmod(t, vault, 0)
	run := func() error {
		out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %s", err, out)
		}
		return nil
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{video, linked, copyOfLinked, unfinished} {
		if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s is still there (%v)", p, err)
		}
	}
	for _, p := range kept {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s is gone: %v", p, err)
		}
	}

	// A temp folder root cannot read stops it before any video goes.
	writeFile(t, video, 1000)
	writeFile(t, unfinished, 1000)
	chmod(t, own, 0)
	if err := run(); err == nil {
		t.Error("run with an unreadable temp folder succeeded")
	}
	if _, err := os.Lstat(video); err != nil {
		t.Errorf("a run stopped by an unreadable temp folder deleted %s: %v", video, err)
	}
	chmod(t, own, 0o755)

	// With no folder there, it deletes nothing, temp files included.
	writeFile(t, unfinished, 1000)
	if err := os.RemoveAll(argv[4]); err != nil {
		t.Fatal(err)
	}
	if err := run(); err == nil || !strings.Contains(err.Error(), "is not a folder") {
		t.Errorf("run with no folder: %v, want it refused", err)
	}
	if _, err := os.Lstat(unfinished); err != nil {
		t.Errorf("a refused run deleted %s: %v", unfinished, err)
	}
}

// The count is of what the command deletes in the folder: videos, each
// once however many links it has there.
func TestMeasureAerialVideos(t *testing.T) {
	host := engine.Host{OS: "darwin", Version: "15.7.7", Root: t.TempDir()}
	host.Home = filepath.Join(host.Root, "Users/dev")
	if u, err := MeasureAerialVideos(context.Background(), host); err != nil || u != (engine.CacheUsage{}) {
		t.Errorf("no folder: %+v, %v; want nothing counted", u, err)
	}
	customer := filepath.Join(host.Root, "Library/Application Support/com.apple.idleassetsd/Customer")
	a, b := filepath.Join(customer, "4KSDR", "a.mov"), filepath.Join(customer, "4KSDR240FPS", "b.mov")
	writeFile(t, a, 1<<20)
	writeFile(t, b, 2<<20)
	writeFile(t, filepath.Join(customer, "entries.json"), 1<<20)
	if err := os.Link(b, filepath.Join(customer, "4KSDR", "b-copy.mov")); err != nil {
		t.Fatal(err)
	}
	size := func(p string) int64 {
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		return physicalSize(fi)
	}
	u, err := MeasureAerialVideos(context.Background(), host)
	if want := (engine.CacheUsage{Files: 2, Bytes: size(a) + size(b)}); err != nil || u != want {
		t.Errorf("count = %+v, %v; want %+v", u, err, want)
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads mode-000 folders")
	}
	locked := filepath.Join(customer, "4KSDR")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if _, err := MeasureAerialVideos(context.Background(), host); err == nil {
		t.Error("a folder it cannot read in full counted without an error")
	}
}
