package trash

import "fmt"

// PreviewCommand is the exact command the executor would run today to
// trash a path: Finder handles the move so the OS "Put Back" works.
// Phase 2 wraps the execution with a staging-dir fallback and the
// oplog; the preview stays the primary mechanism the plan screen and
// --json show.
func PreviewCommand(path string) []string {
	script := fmt.Sprintf("tell application %q to delete POSIX file %q", "Finder", path)
	return []string{"osascript", "-e", script}
}
