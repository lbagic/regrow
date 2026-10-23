package scanner

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"regexp"

	"github.com/lbagic/regrow/internal/engine"
)

// A macOS upgrade stages its payload in "macOS Install Data" on the
// Data volume, and an upgrade that never ran leaves it there. The
// folder is reachable only through /System/Volumes/Data. The
// bundle is listed only when its Info.plist names a macOS older than
// the one running: a newer one is an upgrade still pending, and a
// bundle whose version cannot be told is left out.

// macOSInstallDataDir is where the upgrade stages; the rule's note
// gives the command for the same folder.
const macOSInstallDataDir = "/System/Volumes/Data/macOS Install Data"

var dottedVersionRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*$`)

// macOSInstallData is the macos-install-data query with its seams:
// tests hand in the walker, the folder, the host's version and the
// binary plist reader.
type macOSInstallData struct {
	w       *walker
	dir     string
	version string
	plist   func(ctx context.Context, data []byte) ([]byte, error)
}

// macOSInstallDataQuery is the query for host; a zero host is the
// running machine.
func macOSInstallDataQuery(host engine.Host) ToolQuery {
	return func(ctx context.Context) ([]engine.Item, error) {
		h := host
		if h.OS == "" {
			h = engine.DetectHost()
		}
		q := &macOSInstallData{w: defaultWalker, dir: macOSInstallDataDir, version: h.Version, plist: plutilXML}
		return q.scan(ctx)
	}
}

func (q *macOSInstallData) scan(ctx context.Context) ([]engine.Item, error) {
	if q.version == "" {
		return nil, nil
	}
	bundle := filepath.Join(q.dir, "UpdateBundle")
	plistPath := filepath.Join(bundle, "Info.plist")
	data, err := bounded(ctx, q.w, plistPath, func() ([]byte, error) { return readLimited(plistPath, maxInfoPlist) })
	switch {
	case absent(err):
		// No bundle, or one still being written.
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", plistPath, err)
	}
	if bytes.HasPrefix(data, []byte("bplist")) {
		if data, err = q.plist(ctx, data); err != nil {
			return nil, fmt.Errorf("read %s: %w", plistPath, err)
		}
	}
	s, err := plistStrings(data)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", plistPath, err)
	}
	staged := s["OSVersion"]
	if !dottedVersionRe.MatchString(staged) || engine.CompareVersion(staged, q.version) >= 0 {
		return nil, nil
	}
	u, found, err := q.w.usage(ctx, bundle)
	if err != nil || !found {
		return nil, err
	}
	return []engine.Item{{Path: bundle, Label: "macOS " + staged + " update, never installed", Bytes: u.Bytes, LastUsed: u.Newest, Partial: u.Partial}}, nil
}
