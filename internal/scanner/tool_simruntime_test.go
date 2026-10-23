package scanner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// simctlRuntimeList is `simctl runtime list -j` from CoreSimulator's
// simctl on a machine whose Xcode is gone, cut to the fields read.
const simctlRuntimeList = `{
  "11111111-2222-3333-4444-555555555555" : {
    "build" : "22D8075",
    "deletable" : true,
    "identifier" : "11111111-2222-3333-4444-555555555555",
    "kind" : "Disk Image",
    "lastUsedAt" : "2025-06-24T03:53:46Z",
    "runtimeIdentifier" : "com.apple.CoreSimulator.SimRuntime.iOS-18-3",
    "sizeBytes" : 8708125252,
    "state" : "Ready",
    "version" : "18.3.1"
  }
}`

func TestSimRuntimesNoXcode(t *testing.T) {
	listed := 0
	s := &simRuntimesNoXcode{
		HasSimctl: func(context.Context) bool { return false },
		HasXcode:  func(context.Context) bool { return false },
		List: func(context.Context) ([]byte, bool, error) {
			listed++
			return []byte(simctlRuntimeList), true, nil
		},
		Elsewhere: func(context.Context) bool { return false },
	}
	items, err := s.Runtimes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2025, 6, 24, 3, 53, 46, 0, time.UTC)
	if len(items) != 1 || items[0].Label != "iOS 18.3 (22D8075)" || items[0].Arg != "11111111-2222-3333-4444-555555555555" ||
		items[0].Bytes != 8708125252 || !items[0].LastUsed.Equal(want) {
		t.Errorf("items %+v, want the one runtime with its identifier, size and last use", items)
	}

	// With Xcode back, sim-runtimes lists the same runtimes.
	s.HasSimctl = func(context.Context) bool { return true }
	listed = 0
	if items, err := s.Runtimes(context.Background()); err != nil || len(items) > 0 || listed > 0 {
		t.Errorf("with xcrun finding simctl: items %v, err %v, listed %d times; want nothing", items, err, listed)
	}

	// Xcode installed with xcode-select on the Command Line Tools:
	// xcrun finds no simctl, and Xcode is not gone.
	s.HasSimctl = func(context.Context) bool { return false }
	s.HasXcode = func(context.Context) bool { return true }
	if items, err := s.Runtimes(context.Background()); err != nil || len(items) > 0 || listed > 0 {
		t.Errorf("with an Xcode installed: items %v, err %v, listed %d times; want nothing", items, err, listed)
	}

	// An Xcode only the wider search finds, such as one outside the
	// Applications folders with Spotlight not indexing it.
	s.HasXcode = func(context.Context) bool { return false }
	s.Elsewhere = func(context.Context) bool { return true }
	if items, err := s.Runtimes(context.Background()); err != nil || len(items) > 0 {
		t.Errorf("with an Xcode found elsewhere: items %v, err %v; want nothing", items, err)
	}
}

// The wider search takes seconds, so it runs only once there are
// runtimes to list.
func TestSimRuntimesNoXcodeSearchesFurtherOnlyForRuntimes(t *testing.T) {
	searched := 0
	s := &simRuntimesNoXcode{
		HasSimctl: func(context.Context) bool { return false },
		HasXcode:  func(context.Context) bool { return false },
		List:      func(context.Context) ([]byte, bool, error) { return []byte(`{}`), true, nil },
		Elsewhere: func(context.Context) bool { searched++; return false },
	}
	if items, err := s.Runtimes(context.Background()); err != nil || len(items) > 0 || searched > 0 {
		t.Errorf("no runtimes: items %v, err %v, searched %d times; want nothing and no search", items, err, searched)
	}
	s.List = func(context.Context) ([]byte, bool, error) { return []byte(simctlRuntimeList), true, nil }
	if items, err := s.Runtimes(context.Background()); err != nil || len(items) != 1 || searched != 1 {
		t.Errorf("one runtime: items %v, err %v, searched %d times; want it listed after one search", items, err, searched)
	}
}

func TestSimRuntimesNoXcodeWithoutCoreSimulator(t *testing.T) {
	s := &simRuntimesNoXcode{
		HasSimctl: func(context.Context) bool { return false },
		HasXcode:  func(context.Context) bool { return false },
		List:      func(context.Context) ([]byte, bool, error) { return nil, false, nil },
	}
	if items, err := s.Runtimes(context.Background()); err != nil || len(items) > 0 {
		t.Errorf("no CoreSimulator simctl: items %v, err %v; want nothing and no error", items, err)
	}
}

func TestSimRuntimesNoXcodeListFails(t *testing.T) {
	s := &simRuntimesNoXcode{
		HasSimctl: func(context.Context) bool { return false },
		HasXcode:  func(context.Context) bool { return false },
		List:      func(context.Context) ([]byte, bool, error) { return nil, false, errors.New("exit status 1") },
	}
	if _, err := s.Runtimes(context.Background()); err == nil || !strings.Contains(err.Error(), "exit status 1") {
		t.Errorf("err = %v, want the failed listing", err)
	}
}

// The rule deletes through the simctl the query lists with, as the
// owner, never as root: simdiskimaged does the delete.
func TestSimRuntimesNoXcodeRule(t *testing.T) {
	r := catalogRule(t, "sim-runtimes-no-xcode")
	if r.Risk != engine.RiskCaution || r.Sudo {
		t.Errorf("risk %q, sudo %v; want caution, run as the owner", r.Risk, r.Sudo)
	}
	argv, err := r.NativeCommand.ExpandItem(engine.Item{Arg: "AAAA-1111"})
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) < 3 || argv[0] != "/bin/sh" || argv[len(argv)-2] != coreSimulatorSimctl || argv[len(argv)-1] != "AAAA-1111" {
		t.Errorf("command %q; want /bin/sh running %s on the runtime's identifier", argv, coreSimulatorSimctl)
	}
	if want := coreSimulatorSimctl + " runtime list"; !strings.Contains(r.Note, want) {
		t.Errorf("note %q does not say %q", r.Note, want)
	}
}

// simctl returns from `runtime delete` before the image is gone. The
// rule's command ends only once `runtime list -j` stops naming the
// runtime, so the space is free when the run reports it.
func TestSimRuntimeDeleteWaitsForTheImage(t *testing.T) {
	r := catalogRule(t, "sim-runtimes-no-xcode")
	const id = "AAAA-1111"
	argv, err := r.NativeCommand.ExpandItem(engine.Item{Arg: id})
	if err != nil {
		t.Fatal(err)
	}
	// A fake simctl logs each call and lists the runtime for the first
	// $LISTED listings; a fake sleep returns at once.
	fake := `#!/bin/sh
d=$(dirname "$0")
echo "$1 $2" >> "$d/calls"
case "$1 $2" in
"runtime delete") exit "$DELETE_EXIT";;
"runtime list")
	[ "$LIST_EXIT" = 0 ] || exit "$LIST_EXIT"
	n=$(grep -c '^runtime list' "$d/calls")
	if [ "$n" -le "$LISTED" ]; then printf '{"%s":{"identifier":"%s"}}' "$ID" "$ID"; else echo '{}'; fi
	exit 0;;
esac
exit 2
`
	cases := []struct {
		name                 string
		deleteExit, listExit string
		listed               string
		ok                   bool
		calls                []string
		stderr               string
	}{
		{"gone after two listings", "0", "0", "2", true, []string{"runtime delete", "runtime list", "runtime list", "runtime list"}, ""},
		{"gone at once", "0", "0", "0", true, []string{"runtime delete", "runtime list"}, ""},
		{"the delete fails", "1", "0", "0", false, []string{"runtime delete"}, ""},
		{"the listing fails", "0", "1", "0", false, []string{"runtime delete", "runtime list"}, "runtime list failed"},
		{"still listed", "0", "0", "100000", false, nil, "still listed after 600 listings a second apart"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			simctl := filepath.Join(dir, "simctl")
			writeText(t, simctl, fake)
			bin := filepath.Join(dir, "bin")
			writeText(t, filepath.Join(bin, "sleep"), "#!/bin/sh\nexit 0\n")
			for _, p := range []string{simctl, filepath.Join(bin, "sleep")} {
				if err := os.Chmod(p, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			run := slices.Clone(argv)
			run[len(run)-2] = simctl
			cmd := exec.Command(run[0], run[1:]...)
			cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "ID=" + id, "DELETE_EXIT=" + c.deleteExit, "LIST_EXIT=" + c.listExit, "LISTED=" + c.listed}
			var stderr strings.Builder
			cmd.Stderr = &stderr
			err := cmd.Run()
			if (err == nil) != c.ok {
				t.Fatalf("err = %v (stderr %q), want ok %v", err, stderr.String(), c.ok)
			}
			if c.stderr != "" && !strings.Contains(stderr.String(), c.stderr) {
				t.Errorf("stderr %q does not say %q", stderr.String(), c.stderr)
			}
			if c.calls == nil {
				return
			}
			data, err := os.ReadFile(filepath.Join(dir, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Fields(strings.ReplaceAll(string(data), " ", "_")); !slices.Equal(got, underscored(c.calls)) {
				t.Errorf("calls %q, want %q", got, underscored(c.calls))
			}
		})
	}
}

func underscored(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.ReplaceAll(l, " ", "_")
	}
	return out
}

func TestXcodeInstalled(t *testing.T) {
	app := func(t *testing.T, path, bundleID string) {
		writeText(t, filepath.Join(path, "Contents", "Info.plist"), launchdPlist("", nil, map[string]string{"CFBundleIdentifier": bundleID}))
	}
	cases := []struct {
		name      string
		setup     func(t *testing.T, root, home string) []string // returns Spotlight's answer
		installed bool
	}{
		{"nothing", func(*testing.T, string, string) []string { return nil }, false},
		{"the Xcodes manager", func(t *testing.T, root, _ string) []string {
			app(t, filepath.Join(root, "Applications", "Xcodes.app"), "com.robotsandpencils.XcodesApp")
			return nil
		}, false},
		{"Xcode in /Applications", func(t *testing.T, root, _ string) []string {
			app(t, filepath.Join(root, "Applications", "Xcode.app"), "com.apple.dt.Xcode")
			return nil
		}, true},
		{"a beta in ~/Applications", func(t *testing.T, _, home string) []string {
			app(t, filepath.Join(home, "Applications", "Xcode-beta.app"), "com.apple.dt.Xcode")
			return nil
		}, true},
		{"one folder below", func(t *testing.T, root, _ string) []string {
			app(t, filepath.Join(root, "Applications", "Dev", "Xcode 16.app"), "com.apple.dt.Xcode")
			return nil
		}, true},
		{"an Xcode on a volume not mounted", func(t *testing.T, root, _ string) []string {
			if err := os.Symlink("/Volumes/NotMounted/Applications/Xcode.app", filepath.Join(root, "Applications", "Xcode.app")); err != nil {
				t.Fatal(err)
			}
			return nil
		}, true},
		{"a relative link to an Xcode on a volume not mounted", func(t *testing.T, root, _ string) []string {
			apps := filepath.Join(root, "Applications")
			rel, err := filepath.Rel(apps, "/Volumes/NotMounted/Applications/Xcode.app")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(rel, filepath.Join(apps, "Xcode.app")); err != nil {
				t.Fatal(err)
			}
			return nil
		}, true},
		{"a linked folder in a loop", func(t *testing.T, root, _ string) []string {
			apps := filepath.Join(root, "Applications")
			if err := os.Symlink(filepath.Join(apps, "B"), filepath.Join(apps, "A")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(apps, "A"), filepath.Join(apps, "B")); err != nil {
				t.Fatal(err)
			}
			return nil
		}, true},
		// A link that cannot be followed may hide an install: the rows
		// stay hidden, even for a link left after Xcode was deleted.
		{"a link to an Xcode since deleted", func(t *testing.T, root, _ string) []string {
			if err := os.Symlink(filepath.Join(root, "Applications", "Xcode-16.app"), filepath.Join(root, "Applications", "Xcode.app")); err != nil {
				t.Fatal(err)
			}
			return nil
		}, true},
		{"a link to a link into a disk not mounted", func(t *testing.T, root, _ string) []string {
			links := filepath.Join(root, "Links")
			if err := os.MkdirAll(links, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/Volumes/NotMounted/Xcode.app", filepath.Join(links, "Xcode.app")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(links, "Xcode.app"), filepath.Join(root, "Applications", "Xcode.app")); err != nil {
				t.Fatal(err)
			}
			return nil
		}, true},
		{"~/Applications, a link that cannot be followed", func(t *testing.T, _, home string) []string {
			if err := os.Symlink("/Volumes/NotMounted/Applications", filepath.Join(home, "Applications")); err != nil {
				t.Fatal(err)
			}
			return nil
		}, true},
		{"a link to a file", func(t *testing.T, root, _ string) []string {
			file := filepath.Join(root, "notes.txt")
			writeText(t, file, "x")
			if err := os.Symlink(file, filepath.Join(root, "Applications", "Dev")); err != nil {
				t.Fatal(err)
			}
			return nil
		}, false},
		{"one folder below, through a link", func(t *testing.T, root, _ string) []string {
			ext := filepath.Join(root, "Ext", "Dev")
			app(t, filepath.Join(ext, "Xcode.app"), "com.apple.dt.Xcode")
			if err := os.Symlink(ext, filepath.Join(root, "Applications", "Dev")); err != nil {
				t.Fatal(err)
			}
			return nil
		}, true},
		{"a linked folder on a volume not mounted", func(t *testing.T, root, _ string) []string {
			if err := os.Symlink("/Volumes/NotMounted/Dev", filepath.Join(root, "Applications", "Dev")); err != nil {
				t.Fatal(err)
			}
			return nil
		}, true},
		{"a linked folder since deleted", func(t *testing.T, root, _ string) []string {
			if err := os.Symlink(filepath.Join(root, "Gone"), filepath.Join(root, "Applications", "Dev")); err != nil {
				t.Fatal(err)
			}
			return nil
		}, true},
		{"an Xcode-named folder with no app in it", func(t *testing.T, root, _ string) []string {
			if err := os.MkdirAll(filepath.Join(root, "Applications", "Xcode.app"), 0o755); err != nil {
				t.Fatal(err)
			}
			return nil
		}, false},
		{"an Xcode whose Info.plist cannot be read", func(t *testing.T, root, _ string) []string {
			p := filepath.Join(root, "Applications", "Xcode.app")
			app(t, p, "com.apple.dt.Xcode")
			chmod(t, filepath.Join(p, "Contents"), 0)
			return nil
		}, true},
		{"renamed, known to Spotlight", func(t *testing.T, _, home string) []string {
			p := filepath.Join(home, "Tools", "XC.app")
			app(t, p, "com.apple.dt.Xcode")
			return []string{p}
		}, true},
		{"Spotlight naming a gone app", func(t *testing.T, _, home string) []string { return []string{filepath.Join(home, "Gone", "Xcode.app")} }, false},
		{"Spotlight giving no answer", func(*testing.T, string, string) []string { return []string{noAnswer} }, true},
		{"Spotlight naming an Xcode in a folder that refuses", func(t *testing.T, _, home string) []string {
			p := filepath.Join(home, "Downloads", "Xcode.app")
			app(t, p, "com.apple.dt.Xcode")
			chmod(t, filepath.Dir(p), 0)
			return []string{p}
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, home := t.TempDir(), t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "Applications"), 0o755); err != nil {
				t.Fatal(err)
			}
			answer := tc.setup(t, root, home)
			spotlight := func(context.Context) ([]string, bool) {
				if slices.Equal(answer, []string{noAnswer}) {
					return nil, false
				}
				return answer, true
			}
			got := xcodeInstalled(context.Background(), testWalker(), root, home, spotlight)
			if got != tc.installed {
				t.Errorf("xcodeInstalled = %v, want %v", got, tc.installed)
			}
		})
	}
}

// noAnswer stands for a Spotlight that gave no answer.
const noAnswer = "(no answer)"

// lsBundleEntry is one bundle of `lsregister -dump Bundle` as macOS
// 15.7 prints it, cut to the fields read and a few around them.
func lsBundleEntry(name, path, identifier, mountState string) string {
	return "--------------------------------------------------------------------------------\n" +
		"bundle id:                  " + name + " (0x3714)\n" +
		"class:                      kLSBundleClassApplication (0x2)\n" +
		"container:                  / (0x4)\n" +
		"mount state:                " + mountState + "\n" +
		"path:                       " + path + " (0x7758)\n" +
		"name:                       " + name + "\n" +
		"identifier:                 " + identifier + "\n" +
		"canonical id:               " + strings.ToLower(identifier) + "\n" +
		"version:                    16.2 ({length = 32, bytes = 0x10000000 00000000 02000000 00000000 ... })\n"
}

func TestXcodeRegistered(t *testing.T) {
	safari := lsBundleEntry("Safari", "/Applications/Safari.app", "com.apple.Safari", "mounted")
	cases := []struct {
		name       string
		dump       func(home string) string
		err        error
		registered bool
	}{
		{"no Xcode registered", func(string) string { return safari }, nil, false},
		{"an Xcode outside the Applications folders", func(home string) string {
			return safari + lsBundleEntry("Xcode", filepath.Join(home, "Developer", "Xcode.app"), "com.apple.dt.Xcode", "mounted")
		}, nil, true},
		{"an Xcode on a disk not mounted now", func(string) string {
			return safari + lsBundleEntry("Xcode", "/Volumes/NotMounted/Xcode.app", "com.apple.dt.Xcode", "not mounted")
		}, nil, true},
		// LaunchServices keeps a bundle deleted from a disk still
		// mounted, and Instruments inside it, until it collects them.
		{"an Xcode since deleted", func(home string) string {
			gone := filepath.Join(home, "Gone", "Xcode.app")
			return safari + lsBundleEntry("Xcode", gone, "com.apple.dt.Xcode", "mounted") +
				lsBundleEntry("Instruments", filepath.Join(gone, "Contents", "Applications", "Instruments.app"), "com.apple.dt.Instruments", "mounted")
		}, nil, false},
		{"the identifier in another case", func(home string) string {
			return lsBundleEntry("Xcode", filepath.Join(home, "Developer", "Xcode.app"), "com.apple.dt.xcode", "mounted")
		}, nil, true},
		{"lsregister fails", func(string) string { return "" }, errors.New("exit status 1"), true},
		{"output with no bundle in it", func(string) string { return "lsregister: unknown table\n" }, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeText(t, filepath.Join(home, "Developer", "Xcode.app", "Contents", "Info.plist"), launchdPlist("", nil, map[string]string{"CFBundleIdentifier": "com.apple.dt.Xcode"}))
			dump := func(context.Context) ([]byte, error) { return []byte(tc.dump(home)), tc.err }
			if got := xcodeRegistered(context.Background(), testWalker(), dump); got != tc.registered {
				t.Errorf("xcodeRegistered = %v, want %v", got, tc.registered)
			}
		})
	}
}

// With Spotlight off for the startup disk, mdfind finds nothing, which
// cannot be told from no Xcode.
func TestSpotlightIndexes(t *testing.T) {
	enabled := "/:\n\tIndexing enabled. \n"
	cases := []struct {
		name    string
		answers map[string]string
		err     error
		missing bool
		indexes bool
	}{
		{"both volumes indexed", map[string]string{"/": enabled, "/System/Volumes/Data": "/System/Volumes/Data:\n\tIndexing enabled.\n"}, nil, false, true},
		{"the Data volume not indexed", map[string]string{"/": enabled, "/System/Volumes/Data": "/System/Volumes/Data:\n\tIndexing disabled.\n"}, nil, false, false},
		{"Spotlight's server off", map[string]string{"/": "/:\n\tSpotlight server is disabled.\n", "/System/Volumes/Data": enabled}, nil, false, false},
		{"mdutil fails", nil, errors.New("exit status 1"), false, false},
		{"no mdutil", nil, nil, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := func(_ context.Context, name string, args ...string) ([]byte, bool, error) {
				if name != "mdutil" || len(args) != 2 || args[0] != "-s" {
					t.Fatalf("ran %s %q", name, args)
				}
				if tc.missing || tc.err != nil {
					return nil, false, tc.err
				}
				return []byte(tc.answers[args[1]]), true, nil
			}
			if got := spotlightIndexes(context.Background(), run); got != tc.indexes {
				t.Errorf("spotlightIndexes = %v, want %v", got, tc.indexes)
			}
		})
	}
}

// The default wider search: Spotlight off for the startup disk, or an
// Xcode in the register, keeps the runtimes.
func TestXcodeElsewhere(t *testing.T) {
	enabled := func(context.Context, string, ...string) ([]byte, bool, error) {
		return []byte("Indexing enabled."), true, nil
	}
	disabled := func(context.Context, string, ...string) ([]byte, bool, error) {
		return []byte("Indexing disabled."), true, nil
	}
	home := t.TempDir()
	xcode := filepath.Join(home, "Developer", "Xcode.app")
	writeText(t, filepath.Join(xcode, "Contents", "Info.plist"), launchdPlist("", nil, map[string]string{"CFBundleIdentifier": "com.apple.dt.Xcode"}))
	none := func(context.Context) ([]byte, error) {
		return []byte(lsBundleEntry("Safari", "/Applications/Safari.app", "com.apple.Safari", "mounted")), nil
	}
	registered := func(context.Context) ([]byte, error) {
		return []byte(lsBundleEntry("Xcode", xcode, "com.apple.dt.Xcode", "mounted")), nil
	}
	for _, tc := range []struct {
		name string
		run  func(context.Context, string, ...string) ([]byte, bool, error)
		dump func(context.Context) ([]byte, error)
		want bool
	}{
		{"indexed, nothing registered", enabled, none, false},
		{"Spotlight off", disabled, none, true},
		{"an Xcode registered", enabled, registered, true},
	} {
		if got := xcodeElsewhere(context.Background(), testWalker(), tc.run, tc.dump); got != tc.want {
			t.Errorf("%s: xcodeElsewhere = %v, want %v", tc.name, got, tc.want)
		}
	}
}
