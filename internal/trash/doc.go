// Package trash moves paths to the system Trash (never rm), with a
// staging-directory fallback when the Trash API is unavailable.
// Every destructive action passes a path guard first: never empty,
// "/", $HOME, or mount roots.
package trash
