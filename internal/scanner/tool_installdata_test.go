package scanner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lbagic/regrow/internal/engine"
)

// installDataPlist is the part of an UpdateBundle's Info.plist the
// query reads.
func installDataPlist(osVersion string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>OSVersion</key>
	<string>` + osVersion + `</string>
</dict>
</plist>
`
}

func TestMacOSInstallData(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		setup   func(t *testing.T, dir string)
		want    string // the item's label; "" lists nothing
		wantErr string
	}{
		{"an older bundle", "15.7.7", func(t *testing.T, dir string) {
			writeText(t, filepath.Join(dir, "UpdateBundle", "Info.plist"), installDataPlist("13.5"))
			writeFile(t, filepath.Join(dir, "UpdateBundle", "payload.dmg"), 4096)
		}, "macOS 13.5 update, never installed", ""},
		{"a pending upgrade", "15.7.7", func(t *testing.T, dir string) {
			writeText(t, filepath.Join(dir, "UpdateBundle", "Info.plist"), installDataPlist("26.0"))
		}, "", ""},
		// Versions compare as numbers, part by part.
		{"a pending upgrade to a two-digit minor", "15.7", func(t *testing.T, dir string) {
			writeText(t, filepath.Join(dir, "UpdateBundle", "Info.plist"), installDataPlist("15.10"))
		}, "", ""},
		{"an older bundle of a one-digit major", "10.15.7", func(t *testing.T, dir string) {
			writeText(t, filepath.Join(dir, "UpdateBundle", "Info.plist"), installDataPlist("9.3"))
		}, "macOS 9.3 update, never installed", ""},
		{"a pending upgrade to a two-digit major", "9.3", func(t *testing.T, dir string) {
			writeText(t, filepath.Join(dir, "UpdateBundle", "Info.plist"), installDataPlist("10.15"))
		}, "", ""},
		{"this Mac's own version", "15.7.7", func(t *testing.T, dir string) {
			writeText(t, filepath.Join(dir, "UpdateBundle", "Info.plist"), installDataPlist("15.7.7"))
		}, "", ""},
		{"the host version unknown", "", func(t *testing.T, dir string) {
			writeText(t, filepath.Join(dir, "UpdateBundle", "Info.plist"), installDataPlist("13.5"))
		}, "", ""},
		{"no folder", "15.7.7", func(*testing.T, string) {}, "", ""},
		{"only the locked files", "15.7.7", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "Locked Files", "x"), 10)
		}, "", ""},
		{"a bundle still being written", "15.7.7", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "UpdateBundle", "payload.dmg"), 4096)
		}, "", ""},
		{"a version that is not a number", "15.7.7", func(t *testing.T, dir string) {
			writeText(t, filepath.Join(dir, "UpdateBundle", "Info.plist"), installDataPlist("13.5 (22G74)"))
		}, "", ""},
		{"no OSVersion", "15.7.7", func(t *testing.T, dir string) {
			writeText(t, filepath.Join(dir, "UpdateBundle", "Info.plist"), strings.ReplaceAll(installDataPlist("13.5"), "OSVersion", "Other"))
		}, "", ""},
		{"a plist that does not parse", "15.7.7", func(t *testing.T, dir string) {
			writeText(t, filepath.Join(dir, "UpdateBundle", "Info.plist"), "<plist><dict><key>OSVersion")
		}, "", "Info.plist"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "macOS Install Data")
			c.setup(t, dir)
			q := &macOSInstallData{w: testWalker(), dir: dir, version: c.host, plist: plutilXML}
			items, err := q.scan(context.Background())
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want one naming %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.want == "" {
				if len(items) > 0 {
					t.Errorf("items %+v, want none", items)
				}
				return
			}
			if len(items) != 1 || items[0].Label != c.want || items[0].Path != filepath.Join(dir, "UpdateBundle") || items[0].Bytes <= 0 {
				t.Errorf("items %+v, want the bundle labelled %q with its size", items, c.want)
			}
		})
	}
}

// An Info.plist the owner cannot read is an error, never "nothing
// there": the bundle may be an abandoned 10 GB one.
func TestMacOSInstallDataUnreadablePlist(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	dir := filepath.Join(t.TempDir(), "macOS Install Data")
	plist := filepath.Join(dir, "UpdateBundle", "Info.plist")
	writeText(t, plist, installDataPlist("13.5"))
	if err := os.Chmod(plist, 0); err != nil {
		t.Fatal(err)
	}
	q := &macOSInstallData{w: testWalker(), dir: dir, version: "15.7.7", plist: plutilXML}
	if items, err := q.scan(context.Background()); err == nil {
		t.Errorf("items %+v, no error; want the unreadable Info.plist named", items)
	}
}

// The row exists to give the command, which needs a password: it must
// name each path it removes and say it runs in a terminal with sudo.
func TestMacOSInstallDataRule(t *testing.T) {
	r := catalogRule(t, "macos-install-data")
	if r.Risk != engine.RiskSurfaceOnly || !r.Beta || r.Sudo || len(r.NativeCommand) > 0 {
		t.Errorf("risk %q, beta %v, sudo %v, command %q; want a beta surface-only row that runs nothing", r.Risk, r.Beta, r.Sudo, r.NativeCommand)
	}
	want := "`sudo rm -rf " + `"/System/Volumes/Data/macOS Install Data/UpdateBundle" "/System/Volumes/Data/macOS Install Data/index.sproduct" "/System/Volumes/Data/macOS Install Data/InstallAssistant.pkg.integrityDataV1"` + "`"
	if !strings.Contains(r.Note, want) {
		t.Errorf("note %q does not give %s", r.Note, want)
	}
	if !strings.Contains(r.Note, "password") {
		t.Errorf("note %q does not say the command asks for a password", r.Note)
	}
	if macOSInstallDataDir != "/System/Volumes/Data/macOS Install Data" {
		t.Errorf("the query reads %q, the note's command another folder", macOSInstallDataDir)
	}
}

// The query reads the folder under the scanner's host, so a fixture
// root is read, not the running machine's.
func TestMacOSInstallDataReadsHostRoot(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "System/Volumes/Data/macOS Install Data")
	writeText(t, filepath.Join(dir, "UpdateBundle", "Info.plist"), installDataPlist("13.5"))
	writeFile(t, filepath.Join(dir, "UpdateBundle", "payload.dmg"), 4096)
	host := engine.Host{OS: "darwin", Version: "15.7.7", Root: root}
	for name, q := range map[string]ToolQuery{"the query": macOSInstallDataQuery(host), "DefaultQueries": DefaultQueries(host)["macos-install-data"]} {
		items, err := q(context.Background())
		if err != nil || len(items) != 1 || !strings.HasPrefix(items[0].Path, root) {
			t.Errorf("%s: items %+v, err %v; want the fixture's bundle under %s", name, items, err, root)
		}
	}
}
