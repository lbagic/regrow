// Package trash owns the trash-not-rm mechanism (ARCHITECTURE.md
// invariant 2). Today that is the path guard every destructive target
// must pass, plus the preview of the exact Finder-move command the
// planner shows. Phase 2 adds the execution half behind the same seam:
// the actual move, a staging-directory fallback when the Trash API is
// unavailable, and receipts for the oplog and undo.
package trash
