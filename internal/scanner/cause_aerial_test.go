package scanner

import (
	"context"
	"os"
	"path/filepath"
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

// The videos are only shown: removing them is a raw delete as root,
// which regrow never runs (invariants 2 and 4). On both of the rule's
// paths a found video is never pre-selected, and naming the rule by id
// plans nothing for it.
func TestAerialWallpapersAreShownOnly(t *testing.T) {
	rule := embeddedRule(t, "aerial-wallpapers")
	for _, tc := range []struct {
		version, folder, video string
	}{
		{"15.5", "/Library/Application Support/com.apple.idleassetsd/Customer", "4KSDR240FPS/a.mov"},
		{"26.0", "~/Library/Containers/com.apple.wallpaper.agent/Data/Library/Caches", "a.mov"},
	} {
		t.Run(tc.version, func(t *testing.T) {
			host := fixtureHost(t, rule)
			host.Version = tc.version
			folder := host.ExpandPath(tc.folder)
			writeFile(t, filepath.Join(folder, tc.video), 1<<20)
			// New binds the query to the host it scans.
			findings := New(host).Scan(context.Background(), []engine.Rule{rule})
			if f := findings[0]; f.Err != "" || len(f.Items) == 0 || f.Items[0].Path != folder {
				t.Fatalf("finding %+v, want the fixture's folder %s found", f, folder)
			}
			if sel := engine.DefaultSelection(findings); len(sel) != 0 {
				t.Errorf("default selection %v, want nothing", sel)
			}
			plan := engine.BuildPlan(host, findings, map[string]bool{rule.ID: true})
			if len(plan.Actions) != 0 || len(plan.Skipped) != 1 || plan.Skipped[0].Reason != "surface-only: report, never delete" {
				t.Errorf("plan by id: actions %+v, skipped %+v; want no action and one surface-only skip", plan.Actions, plan.Skipped)
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
