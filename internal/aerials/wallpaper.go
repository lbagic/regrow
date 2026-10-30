package aerials

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// WallpaperStore is where macOS keeps what each display and space shows
// as the wallpaper (Desktop) and as the screen saver (Idle), under the
// user's home. Its format is Apple's own and unpublished.
const WallpaperStore = "Library/Application Support/com.apple.wallpaper/Store/Index.plist"

// aerialChoice is the provider the store names for an aerial.
const aerialChoice = "com.apple.wallpaper.choice.aerials"

// Surfaces an aerial can be set for. The lock screen shows the
// wallpaper.
const (
	SurfaceWallpaper   = "wallpaper"
	SurfaceScreenSaver = "screen saver"
)

// aerialSurfaces reads the wallpaper store and returns the surfaces an
// aerial is set for, wallpaper first. A store that is not there sets
// nothing.
func aerialSurfaces(ctx context.Context, store string) ([]string, error) {
	if _, err := os.Stat(store); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/plutil", "-convert", "xml1", "-o", "-", store)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("read the wallpaper store: %s", strings.TrimSpace(stderr.String()+" "+err.Error()))
	}
	paths, err := providerPaths(out, aerialChoice)
	if err != nil {
		return nil, fmt.Errorf("read the wallpaper store: %w", err)
	}
	var surfaces []string
	for _, p := range paths {
		switch {
		case slices.Contains(p, "Desktop"):
			surfaces = append(surfaces, SurfaceWallpaper)
		case slices.Contains(p, "Idle"):
			surfaces = append(surfaces, SurfaceScreenSaver)
		}
	}
	slices.SortFunc(surfaces, func(a, b string) int {
		return slices.Index([]string{SurfaceWallpaper, SurfaceScreenSaver}, a) - slices.Index([]string{SurfaceWallpaper, SurfaceScreenSaver}, b)
	})
	return slices.Compact(surfaces), nil
}

// providerPaths walks an XML property list and returns, for each
// "Provider" string equal to provider, the keys of the dictionaries
// that hold it, outermost first.
func providerPaths(data []byte, provider string) ([][]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var (
		path  []string // the key naming each open dict or array; "" inside an array
		key   string   // the last key read in the innermost dict
		found [][]string
	)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return found, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "plist":
			case "dict", "array":
				path = append(path, key)
				key = ""
			case "key", "string":
				var text string
				if err := dec.DecodeElement(&text, &t); err != nil {
					return nil, err
				}
				if t.Name.Local == "key" {
					key = text
					continue
				}
				if key == "Provider" && text == provider {
					found = append(found, slices.Clone(path))
				}
				key = ""
			default:
				if err := dec.Skip(); err != nil {
					return nil, err
				}
				key = ""
			}
		case xml.EndElement:
			if (t.Name.Local == "dict" || t.Name.Local == "array") && len(path) > 0 {
				path = path[:len(path)-1]
				key = ""
			}
		}
	}
}

// SettingsURL opens the System Settings pane that chooses what surface
// shows: `open` takes it, as does NSWorkspace.
func SettingsURL(surface string) string {
	if surface == SurfaceScreenSaver {
		return "x-apple.systempreferences:com.apple.ScreenSaver-Settings.extension"
	}
	return "x-apple.systempreferences:com.apple.Wallpaper-Settings.extension"
}

// WallpaperStorePath is the store under home.
func WallpaperStorePath(home string) string {
	return filepath.Join(home, WallpaperStore)
}
