package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDevToolsPresent(t *testing.T) {
	ctx := context.Background()
	const clt = "/Library/Developer/CommandLineTools"
	const xcode = "/Applications/Xcode.app/Contents/Developer"
	file := func(t *testing.T, root, p string) {
		t.Helper()
		p = filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tools := func(t *testing.T, root, dir string) { t.Helper(); file(t, root, dir+"/usr/lib/libxcrun.dylib") }
	// link makes a select link; an absolute target is taken inside root.
	link := func(t *testing.T, root, name, target string) {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if filepath.IsAbs(target) {
			target = filepath.Join(root, target)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	const dbLink, selectLink = "/var/db/xcode_select_link", "/var/select/developer_dir"
	cases := []struct {
		name   string
		setup  func(t *testing.T, root string)
		devDir string
		want   bool
	}{
		{"nothing installed", func(*testing.T, string) {}, "", false},
		{"Command Line Tools", func(t *testing.T, root string) { tools(t, root, clt) }, "", true},
		{"Command Line Tools, older layout", func(t *testing.T, root string) { file(t, root, clt+"/usr/bin/xcrun") }, "", true},
		{"Xcode", func(t *testing.T, root string) { tools(t, root, xcode) }, "", true},
		{"a beta Xcode alone", func(t *testing.T, root string) {
			tools(t, root, "/Applications/Xcode-beta.app/Contents/Developer")
		}, "", true},
		{"Command Line Tools partly removed", func(t *testing.T, root string) {
			file(t, root, clt+"/usr/bin/git")
		}, "", false},
		{"link to the Command Line Tools", func(t *testing.T, root string) {
			tools(t, root, clt)
			link(t, root, dbLink, clt)
		}, "", true},
		{"link to an Xcode app folder", func(t *testing.T, root string) {
			tools(t, root, "/Applications/Xcode-16.app/Contents/Developer")
			link(t, root, dbLink, "/Applications/Xcode-16.app")
		}, "", true},
		{"link to removed tools, with Xcode left", func(t *testing.T, root string) {
			tools(t, root, xcode)
			link(t, root, dbLink, clt)
		}, "", false},
		{"the first select link wins", func(t *testing.T, root string) {
			tools(t, root, clt)
			link(t, root, selectLink, clt)
			link(t, root, dbLink, "/nowhere")
		}, "", true},
		{"DEVELOPER_DIR names an Xcode app", func(t *testing.T, root string) {
			tools(t, root, "/opt/Xcode.app/Contents/Developer")
		}, "/opt/Xcode.app", true},
		{"DEVELOPER_DIR names a missing folder", func(t *testing.T, root string) { tools(t, root, clt) }, "/nowhere", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.setup(t, root)
			d := devTools{w: defaultWalker, root: root, getenv: func(name string) string {
				if name == "DEVELOPER_DIR" {
					return tc.devDir
				}
				return ""
			}}
			if got := d.present(ctx); got != tc.want {
				t.Fatalf("present = %v, want %v", got, tc.want)
			}
		})
	}
}

// fakeTools puts an xcrun and a git on PATH that record each run.
func fakeTools(t *testing.T) (ran func() bool) {
	t.Helper()
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	for _, name := range []string{"xcrun", "git"} {
		script := "#!/bin/sh\n/usr/bin/touch '" + marker + "'\nexit 1\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	return func() bool {
		_, err := os.Stat(marker)
		return err == nil
	}
}

func withDeveloperTools(t *testing.T, present bool) {
	t.Helper()
	real := developerTools
	developerTools = func(context.Context) bool { return present }
	t.Cleanup(func() { developerTools = real })
}

// Without developer tools, macOS's xcrun shim asks to install them, so
// the simulator rules must find nothing without running it.
func TestNoDeveloperToolsRunsNoXcrun(t *testing.T) {
	ctx := context.Background()
	for _, present := range []bool{false, true} {
		withDeveloperTools(t, present)
		ran := fakeTools(t)
		devices, err1 := querySimctlDevices(ctx)
		unavailable, err2 := querySimctlDevicesUnavailable(ctx)
		runtimes, err3 := querySimctlRuntimes(ctx)
		found := xcrunFindsSimctl(ctx)
		if len(devices)+len(unavailable)+len(runtimes) != 0 || err1 != nil || err2 != nil || err3 != nil || found {
			t.Fatalf("tools %v: found %v %v %v %v, errors %v %v %v; want nothing", present, devices, unavailable, runtimes, found, err1, err2, err3)
		}
		if ran() != present {
			t.Fatalf("tools %v: xcrun ran = %v", present, ran())
		}
	}
}

func TestGitRunsUnlessItIsTheShimWithoutTools(t *testing.T) {
	ctx := context.Background()
	withDeveloperTools(t, false)
	fakeTools(t)
	if !gitRuns(ctx) {
		t.Fatal("a git outside /usr/bin must run without developer tools")
	}
	if _, err := os.Stat("/usr/bin/git"); err != nil {
		t.Skip("no /usr/bin/git on this machine")
	}
	t.Setenv("PATH", "/usr/bin")
	if gitRuns(ctx) {
		t.Fatal("/usr/bin/git without developer tools must not run")
	}
	withDeveloperTools(t, true)
	if !gitRuns(ctx) {
		t.Fatal("/usr/bin/git with developer tools must run")
	}
	withDeveloperTools(t, false)
	bin := t.TempDir()
	if err := os.Symlink("/usr/bin/git", filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if gitRuns(ctx) {
		t.Fatal("a link to /usr/bin/git without developer tools must not run")
	}
}

// The git rules' queries ask gitRuns before they look for anything.
func TestGitQueriesRunNoShimWithoutTools(t *testing.T) {
	ctx := context.Background()
	withDeveloperTools(t, false)
	ran := fakeTools(t)
	real := gitShim
	gitShim = filepath.Join(filepath.SplitList(os.Getenv("PATH"))[0], "git")
	t.Cleanup(func() { gitShim = real })
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"workspace/repo/.git", "workspace/repo/dist"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	worktrees, err1 := queryGitWorktrees(ctx)
	builds, err2 := queryBuildOutput(ctx)
	if len(worktrees)+len(builds) != 0 || err1 != nil || err2 != nil || ran() {
		t.Fatalf("found %v %v, errors %v %v, git ran %v; want nothing run", worktrees, builds, err1, err2, ran())
	}

	// With tools, the same fixture runs git: the fixture reaches it.
	withDeveloperTools(t, true)
	t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+"/usr/sbin:/bin")
	if _, err := queryBuildOutput(ctx); err != nil || !ran() {
		t.Fatalf("with tools: error %v, git ran %v; want git run", err, ran())
	}
}
