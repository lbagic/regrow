package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDirSize(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.bin"), 10_000)
	writeFile(t, filepath.Join(dir, "sub", "b.bin"), 20_000)

	got, err := DirSize(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Physical usage is block-rounded, so assert a sane envelope
	// rather than an exact byte count.
	if got < 30_000 {
		t.Errorf("DirSize = %d, want >= 30000 (physical >= logical here)", got)
	}
	if got > 1_000_000 {
		t.Errorf("DirSize = %d, suspiciously large", got)
	}
}

func TestDirSizeSingleFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "one.bin")
	writeFile(t, f, 5_000)
	got, err := DirSize(f)
	if err != nil {
		t.Fatal(err)
	}
	if got < 5_000 {
		t.Errorf("DirSize(file) = %d, want >= 5000", got)
	}
}

func TestDirSizeDoesNotFollowSymlinks(t *testing.T) {
	real := t.TempDir()
	writeFile(t, filepath.Join(real, "big.bin"), 100_000)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "small.bin"), 1_000)
	if err := os.Symlink(real, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}

	got, err := DirSize(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got >= 100_000 {
		t.Errorf("DirSize = %d, followed a symlink", got)
	}
}

func TestDirSizeMissingPath(t *testing.T) {
	if _, err := DirSize(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("want error for missing path")
	}
}
