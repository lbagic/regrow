package headroom

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Process struct {
	PID  int    `json:"pid"`
	Name string `json:"name"`
	RSS  int64  `json:"rss"` // resident bytes
}

func Processes(ctx context.Context) ([]Process, error) {
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,rss=,comm=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	return parsePS(out), nil
}

// parsePS reads `ps -axo pid=,rss=,comm=`: rss is in KiB, and comm is
// the executable path, which may contain spaces.
func parsePS(out []byte) []Process {
	var ps []Process
	for _, line := range strings.Split(string(out), "\n") {
		pidStr, rest, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		rssStr, comm, ok := strings.Cut(strings.TrimSpace(rest), " ")
		if !ok {
			continue
		}
		pid, err1 := strconv.Atoi(pidStr)
		rss, err2 := strconv.ParseInt(rssStr, 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		ps = append(ps, Process{PID: pid, Name: filepath.Base(strings.TrimSpace(comm)), RSS: rss * 1024})
	}
	return ps
}

// Top is the process with the most resident memory.
func Top(ps []Process) (Process, bool) {
	var top Process
	for _, p := range ps {
		if p.RSS > top.RSS {
			top = p
		}
	}
	return top, top.RSS > 0
}

// Running returns which of the names have a process, sorted.
func Running(ps []Process, names []string) []string {
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range ps {
		if want[p.Name] && !seen[p.Name] {
			seen[p.Name] = true
			out = append(out, p.Name)
		}
	}
	sort.Strings(out)
	return out
}
