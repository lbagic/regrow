package scanner

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lbagic/regrow/internal/engine"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverNameAndMarker(t *testing.T) {
	home := t.TempDir()
	// Real hit: target/ with CACHEDIR.TAG.
	touch(t, filepath.Join(home, "workspace", "proj-a", "target", "CACHEDIR.TAG"))
	// Decoy: target/ without the marker (a non-cargo dir named target).
	touch(t, filepath.Join(home, "workspace", "proj-b", "target", "somefile"))
	// Decoy: marker in a dir not named target.
	touch(t, filepath.Join(home, "workspace", "proj-c", "cache", "CACHEDIR.TAG"))
	// Inside builtin-excluded tree: never found.
	touch(t, filepath.Join(home, "workspace", "node_modules", "dep", "target", "CACHEDIR.TAG"))

	host := engine.Host{OS: "darwin", Home: home}
	spec := engine.Discover{
		Roots:   []string{"~/workspace", "~/does-not-exist"},
		Name:    "target",
		Markers: []string{"CACHEDIR.TAG"},
	}
	got := discover(host, spec)
	want := []string{filepath.Join(home, "workspace", "proj-a", "target")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("discover = %v, want %v", got, want)
	}
}

func TestDiscoverByNameMatchesInsideBuiltinExcludeList(t *testing.T) {
	// node_modules is builtin-excluded for descent, but a rule that
	// *targets* node_modules by name must still find it.
	home := t.TempDir()
	touch(t, filepath.Join(home, "app", "node_modules", "left-pad", "index.js"))

	host := engine.Host{OS: "darwin", Home: home}
	got := discover(host, engine.Discover{Roots: []string{"~"}, Name: "node_modules"})
	want := []string{filepath.Join(home, "app", "node_modules")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("discover = %v, want %v", got, want)
	}
}

func TestDiscoverMaxDepth(t *testing.T) {
	home := t.TempDir()
	touch(t, filepath.Join(home, "a", "b", "c", "target", "CACHEDIR.TAG"))

	host := engine.Host{OS: "darwin", Home: home}
	spec := engine.Discover{Roots: []string{"~"}, Name: "target", Markers: []string{"CACHEDIR.TAG"}, MaxDepth: 2}
	if got := discover(host, spec); len(got) != 0 {
		t.Errorf("depth-limited discover found %v", got)
	}

	spec.MaxDepth = 4
	if got := discover(host, spec); len(got) != 1 {
		t.Errorf("discover at sufficient depth found %v", got)
	}
}

func TestDiscoverRuleExcludeWins(t *testing.T) {
	home := t.TempDir()
	touch(t, filepath.Join(home, "vendor", "target", "CACHEDIR.TAG"))
	touch(t, filepath.Join(home, "src", "target", "CACHEDIR.TAG"))

	host := engine.Host{OS: "darwin", Home: home}
	spec := engine.Discover{Roots: []string{"~"}, Name: "target", Markers: []string{"CACHEDIR.TAG"}, Exclude: []string{"vendor"}}
	got := discover(host, spec)
	want := []string{filepath.Join(home, "src", "target")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("discover = %v, want %v", got, want)
	}
}
