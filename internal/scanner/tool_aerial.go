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
// the file, and on macOS 15 the downloader's finished downloads in its
// root-only temp folder can be such links. The query sizes the folder
// in two items, what deleting its files frees and the files a link
// outside it also holds. Link counts are read for this rule alone:
// elsewhere sizes stay path-unique.

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
	var (
		mu     sync.Mutex
		closed bool
		linked = map[uint64]*linkedFile{}
	)
	u, found, err := w.usageSeeing(ctx, dir, func(_ string, e fs.DirEntry) {
		if !e.Type().IsRegular() {
			return
		}
		fi, err := e.Info()
		if err != nil {
			return
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || st.Nlink < 2 {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if closed {
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
	case !found:
		return nil, nil
	}
	// The walk counted a file once for each link it met; deleting the
	// folder frees one whose links all lie inside it, once, and none of
	// the others.
	frees, held := u.Bytes, int64(0)
	files := 0
	for _, lf := range linked {
		frees -= lf.bytes * int64(lf.seen)
		if lf.seen >= lf.nlink {
			frees += lf.bytes
			continue
		}
		held += lf.bytes
		files++
	}
	items := []engine.Item{{Path: dir, Bytes: frees, LastUsed: u.Newest, Partial: u.Partial}}
	if files > 0 {
		label := fmt.Sprintf("%d files a link outside the folder also holds, such as the downloader's temp copies: deleting them here frees nothing", files)
		if files == 1 {
			label = "1 file a link outside the folder also holds, such as the downloader's temp copies: deleting it here frees nothing"
		}
		items = append(items, engine.Item{Label: label, Bytes: held, Partial: u.Partial})
	}
	return items, nil
}
