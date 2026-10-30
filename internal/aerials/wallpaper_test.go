package aerials

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// storeXML is a wallpaper store in the shape macOS 15.7 writes, cut to
// what the walk reads: one display shows an aerial as its wallpaper,
// every other display an image folder, and the screen saver is a
// screen saver module.
const storeXML = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>AllSpacesAndDisplays</key>
	<dict>
		<key>Desktop</key>
		<dict>
			<key>Content</key>
			<dict>
				<key>Choices</key>
				<array>
					<dict>
						<key>Configuration</key>
						<data>YnBsaXN0MDA=</data>
						<key>Files</key>
						<array><dict><key>relative</key><string>file:///Users/someone/Pictures/</string></dict></array>
						<key>Provider</key>
						<string>com.apple.wallpaper.choice.image-folder</string>
					</dict>
				</array>
				<key>Shuffle</key>
				<string>$null</string>
			</dict>
			<key>LastSet</key>
			<date>2026-10-15T18:09:57Z</date>
		</dict>
		<key>Idle</key>
		<dict>
			<key>Content</key>
			<dict>
				<key>Choices</key>
				<array>
					<dict>
						<key>Provider</key>
						<string>com.apple.wallpaper.choice.screen-saver</string>
					</dict>
				</array>
			</dict>
		</dict>
		<key>Type</key>
		<string>individual</string>
	</dict>
	<key>Displays</key>
	<dict>
		<key>00000000-0000-0000-0000-00000000000A</key>
		<dict>
			<key>Desktop</key>
			<dict>
				<key>Content</key>
				<dict>
					<key>Choices</key>
					<array>
						<dict>
							<key>Provider</key>
							<string>com.apple.wallpaper.choice.aerials</string>
						</dict>
					</array>
				</dict>
			</dict>
		</dict>
	</dict>
	<key>Spaces</key>
	<dict/>
</dict>
</plist>
`

func TestAerialSurfacesNamesTheWallpaper(t *testing.T) {
	dir := t.TempDir()
	xml := filepath.Join(dir, "Index.xml")
	store := filepath.Join(dir, "Index.plist")
	if err := os.WriteFile(xml, []byte(storeXML), 0o600); err != nil {
		t.Fatal(err)
	}
	// The store on disk is a binary property list.
	if out, err := exec.Command("/usr/bin/plutil", "-convert", "binary1", "-o", store, xml).CombinedOutput(); err != nil {
		t.Fatalf("plutil: %v: %s", err, out)
	}
	got, err := aerialSurfaces(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{SurfaceWallpaper}) {
		t.Errorf("surfaces = %q, want the wallpaper only", got)
	}
	none, err := aerialSurfaces(context.Background(), filepath.Join(dir, "absent.plist"))
	if err != nil || none != nil {
		t.Errorf("no store: %q, %v; want nothing", none, err)
	}
	if err := os.WriteFile(store, []byte("not a plist"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := aerialSurfaces(context.Background(), store); err == nil {
		t.Error("a broken store read as fine")
	}
}

func TestProviderPathsFindsEachSurface(t *testing.T) {
	data := []byte(`<plist><dict>
<key>SystemDefault</key><dict>
  <key>Idle</key><dict><key>Content</key><dict><key>Choices</key><array>
    <dict><key>Provider</key><string>com.apple.wallpaper.choice.aerials</string></dict>
  </array></dict></dict>
  <key>Desktop</key><dict><key>Content</key><dict><key>Choices</key><array>
    <dict><key>Provider</key><string>com.apple.wallpaper.choice.aerials</string></dict>
  </array></dict></dict>
</dict>
<key>Provider</key><string>com.apple.wallpaper.choice.image</string>
</dict></plist>`)
	paths, err := providerPaths(data, aerialChoice)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || !slices.Contains(paths[0], "Idle") || !slices.Contains(paths[1], "Desktop") {
		t.Errorf("paths = %q", paths)
	}
}

func TestStatusNamesASurfaceShowingAnAerial(t *testing.T) {
	f := newFixture(t)
	f.a.Store = filepath.Join(f.dir, "Index.plist")
	if err := os.WriteFile(f.a.Store, []byte(storeXML), 0o600); err != nil {
		t.Fatal(err)
	}
	st := f.a.Status(context.Background())
	if !slices.Equal(st.SetFor, []string{SurfaceWallpaper}) || st.SetForError != "" {
		t.Errorf("status = %+v, want the wallpaper named", st)
	}
}
