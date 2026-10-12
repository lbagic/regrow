package scanner

import (
	"context"
	"fmt"
	"io/fs"
	"sync"
	"syscall"

	"github.com/lbagic/regrow/internal/engine"
)

// The aerial videos macOS downloads for the wallpaper and the screen
// saver. Deleting a video frees its bytes only when no other link holds
// the file: on macOS 15.7, 16 of 69 videos were also held by the
// downloader's finished downloads in its root-only temp folder, so
// deleting the folder's videos freed 21.7 of 32.2 GiB. The query sizes
// the folder in two items, what deleting its files frees and the files
// a link outside it also holds. Link counts are read for this rule
// alone: elsewhere sizes stay path-unique (the attack plan leaves
// hard-link-aware sizing out).

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

// aerialFolder measures one folder: an item for the folder holding what
// deleting its files frees, and one for the files a link outside the
// folder also holds, when there are any. A folder that refuses is an
// item with Partial set; an absent one is nothing.
func aerialFolder(ctx context.Context, w *walker, dir string) ([]engine.Item, error) {
	info, err := w.lstat(ctx, dir)
	switch {
	case absent(err):
		return nil, nil
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case err != nil:
		return []engine.Item{{Path: dir, Partial: true}}, nil
	}
	var (
		mu     sync.Mutex
		closed bool
		frees  = physicalSize(info)
		newest = info.ModTime()
		linked = map[uint64]*linkedFile{}
	)
	partial, err := w.walk(ctx, dir, func(d string, batch []fs.DirEntry) ([]string, bool) {
		var subdirs []string
		ok := true
		for _, e := range batch {
			fi, err := e.Info()
			if err != nil {
				ok = ok && !unreadable(err)
				continue
			}
			if e.IsDir() {
				subdirs = append(subdirs, childPath(d, e.Name()))
			}
			size := physicalSize(fi)
			st, isStat := fi.Sys().(*syscall.Stat_t)
			mu.Lock()
			if !closed {
				if fi.ModTime().After(newest) {
					newest = fi.ModTime()
				}
				switch {
				case fi.Mode().IsRegular() && isStat && st.Nlink > 1:
					lf := linked[st.Ino]
					if lf == nil {
						lf = &linkedFile{bytes: size, nlink: uint64(st.Nlink)}
						linked[st.Ino] = lf
					}
					lf.seen++
				default:
					frees += size
				}
			}
			mu.Unlock()
		}
		return subdirs, ok
	})
	mu.Lock()
	defer mu.Unlock()
	closed = true
	if err != nil {
		return nil, err
	}
	var held int64
	files := 0
	for _, lf := range linked {
		if lf.seen >= lf.nlink {
			frees += lf.bytes
			continue
		}
		held += lf.bytes
		files++
	}
	items := []engine.Item{{Path: dir, Bytes: frees, LastUsed: newest, Partial: partial}}
	if files > 0 {
		label := fmt.Sprintf("%d files a link outside the folder also holds, such as the downloader's temp copies: deleting them here frees nothing", files)
		if files == 1 {
			label = "1 file a link outside the folder also holds, such as the downloader's temp copies: deleting it here frees nothing"
		}
		items = append(items, engine.Item{Label: label, Bytes: held, Partial: partial})
	}
	return items, nil
}
