package scanner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

// The aerial videos macOS downloads for the wallpaper and the screen
// saver. Deleting a video frees its bytes only when no other link holds
// the file, and on macOS 15 the downloader's finished downloads in its
// root-only temp folder can be such links. The query sizes the folder's
// videos, the files the row's command deletes there, in two items: what
// deleting them frees, and the videos a link outside it also holds. The
// folder's other files, its catalogue, stay and count nowhere. Link
// counts are read for this rule alone: elsewhere sizes stay path-unique.

// aerialPathEntries are the folders by macOS release: idleassetsd's
// through macOS 15, the wallpaper agent's container from 26.
var aerialPathEntries = []engine.PathEntry{
	{Path: "/Library/Application Support/com.apple.idleassetsd/Customer", OSMax: "25"},
	{Path: "~/Library/Containers/com.apple.wallpaper.agent/Data/Library/Caches", OSMin: "26"},
}

// aerialFolders are the video folders that apply to host.
func aerialFolders(host engine.Host) []string {
	return host.ResolvePaths(engine.Rule{Paths: map[string][]engine.PathEntry{"darwin": aerialPathEntries}})
}

// aerialVideos is the aerial-videos query. A zero host is the running
// machine; tests hand in a fixture host and a walker.
type aerialVideos struct {
	w    *walker
	host engine.Host
}

func (a *aerialVideos) Items(ctx context.Context) ([]engine.Item, error) {
	host, w := a.host, a.w
	if host.OS == "" {
		host = engine.DetectHost()
	}
	if w == nil {
		w = defaultWalker
	}
	var items []engine.Item
	for _, dir := range aerialFolders(host) {
		found, err := aerialFolder(ctx, w, dir)
		if err != nil {
			return nil, err
		}
		items = append(items, found...)
	}
	return items, nil
}

// linkedFile is a file with more than one link, by inode: its size
// once, its link count, and how many of its links the walk met.
type linkedFile struct {
	bytes       int64
	nlink, seen uint64
}

// aerialFolder measures one folder by its videos: an item for what
// deleting them frees, last used when the newest video was written, and
// one for the videos a link outside the folder also holds, when there
// are any and the walk read the whole folder. A
// folder that refuses is an item with Partial set; an absent one, or
// one the walk read in full and found no video in, is nothing.
func aerialFolder(ctx context.Context, w *walker, dir string) ([]engine.Item, error) {
	var (
		mu     sync.Mutex
		closed bool
		videos int
		single int64
		newest time.Time
		linked = map[uint64]*linkedFile{}
	)
	u, found, err := w.usageSeeing(ctx, dir, func(_ string, e fs.DirEntry) {
		if !isAerialVideo(e) {
			return
		}
		fi, err := e.Info()
		if err != nil {
			return
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return
		}
		videos++
		if fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
		if !ok || st.Nlink < 2 {
			single += physicalSize(fi)
			return
		}
		lf := linked[st.Ino]
		if lf == nil {
			lf = &linkedFile{bytes: physicalSize(fi), nlink: uint64(st.Nlink)}
			linked[st.Ino] = lf
		}
		lf.seen++
	})
	mu.Lock()
	defer mu.Unlock()
	closed = true
	switch {
	case err != nil:
		return nil, err
	case !found, videos == 0 && !u.Partial:
		return nil, nil
	}
	// Deleting the videos frees one whose links all lie among them, once,
	// and none of the others. A walk that missed part of the folder
	// cannot tell an unseen link inside from one outside: such files
	// count nowhere, and the folder's figure stays a lower bound.
	frees, held := single, int64(0)
	files := 0
	for _, lf := range linked {
		switch {
		case lf.seen >= lf.nlink:
			frees += lf.bytes
		case !u.Partial:
			held += lf.bytes
			files++
		}
	}
	items := []engine.Item{{Path: dir, Bytes: frees, LastUsed: newest, Partial: u.Partial}}
	if files > 0 {
		label := fmt.Sprintf("%d files a link outside the folder also holds, such as the downloader's temp copies: each frees its space only once that link goes too", files)
		if files == 1 {
			label = "1 file a link outside the folder also holds, such as the downloader's temp copies: it frees its space only once that link goes too"
		}
		items = append(items, engine.Item{Label: label, Bytes: held})
	}
	return items, nil
}

// isAerialVideo is a file the row's command deletes in an aerial folder:
// its find matches -name "*.mov", case and all.
func isAerialVideo(e fs.DirEntry) bool {
	return e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".mov")
}

// MeasureAerialVideos counts the videos in host's aerial folders, the
// files the row's command deletes there, each file once however many
// links it has. A folder that is not there counts nothing; one that
// cannot be read in full is an error, so the command does not run.
func MeasureAerialVideos(ctx context.Context, host engine.Host) (engine.CacheUsage, error) {
	var u engine.CacheUsage
	seen := map[uint64]bool{}
	for _, dir := range aerialFolders(host) {
		err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				if path == dir && errors.Is(err, fs.ErrNotExist) {
					return fs.SkipDir
				}
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !isAerialVideo(e) {
				return nil
			}
			fi, err := e.Info()
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			} else if err != nil {
				return err
			}
			if st, ok := fi.Sys().(*syscall.Stat_t); ok {
				if seen[st.Ino] {
					return nil
				}
				seen[st.Ino] = true
			}
			u.Files++
			u.Bytes += physicalSize(fi)
			return nil
		})
		if err != nil {
			return engine.CacheUsage{}, fmt.Errorf("count the aerial videos in %s: %w", dir, err)
		}
	}
	return u, nil
}
