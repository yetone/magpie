package claudecode

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// stubOwn has Own see these Claude Codes, first on PATH first, each of
// the version vers gives ("" for one that can't be read), where it is
// installed and when it is run, and counts the runs.
func stubOwn(t *testing.T, paths []string, vers map[string]string) map[string]int {
	t.Helper()
	was := own
	reads := map[string]int{}
	var mu sync.Mutex
	own.first = func() string {
		if len(paths) == 0 {
			return ""
		}
		return paths[0]
	}
	own.all = func() []string { return paths }
	own.read = func(p string) string { return vers[p] }
	own.version = func(p string) string {
		mu.Lock()
		reads[p]++
		mu.Unlock()
		return vers[p]
	}
	versions.Lock()
	versions.m = map[string]versionEntry{}
	versions.Unlock()
	t.Cleanup(func() {
		own = was
		versions.Lock()
		versions.m = map[string]versionEntry{}
		versions.Unlock()
	})
	return reads
}

func programs(t *testing.T, names ...string) []string {
	t.Helper()
	d := t.TempDir()
	var out []string
	for _, n := range names {
		p := filepath.Join(d, n, "claude")
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// Of two Claude Codes, the newer runs, whichever comes first on PATH
// (zi_xiang5806 on X: 2.1.38 first, 2.1.325 after it); a version that
// can't be read is unknown, never taken for old or new.
func TestOwnIsTheNewest(t *testing.T) {
	p := programs(t, "a", "b", "c")
	cases := []struct {
		name string
		vers map[string]string
		want string
	}{
		{"older first", map[string]string{p[0]: "2.1.38 (Claude Code)", p[1]: "2.1.325 (Claude Code)"}, p[1]},
		{"newer first", map[string]string{p[0]: "2.1.325 (Claude Code)", p[1]: "2.1.38 (Claude Code)"}, p[0]},
		{"newest last", map[string]string{p[0]: "2.1.38", p[1]: "2.1.100", p[2]: "2.1.325"}, p[2]},
		{"same version", map[string]string{p[0]: "2.1.325", p[1]: "2.1.325"}, p[0]},
		{"first unknown", map[string]string{p[1]: "2.1.325"}, p[0]},
		{"other unknown", map[string]string{p[0]: "2.1.38", p[1]: "error: something"}, p[0]},
		{"none known", map[string]string{}, p[0]},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubOwn(t, p, c.vers)
			if got := Own(); got != c.want {
				t.Errorf("Own() = %q, want %q", got, c.want)
			}
		})
	}
}

// One Claude Code is run without asking its version, and none is "".
func TestOwnAsksNothingOfOne(t *testing.T) {
	p := programs(t, "a")
	reads := stubOwn(t, p, map[string]string{p[0]: "2.1.38"})
	if got := Own(); got != p[0] {
		t.Errorf("Own() = %q", got)
	}
	if len(reads) != 0 {
		t.Errorf("versions read for one Claude Code: %v", reads)
	}
	stubOwn(t, nil, nil)
	if got := Own(); got != "" {
		t.Errorf("Own() with none = %q", got)
	}
}

// A version is read once while the binary is the same file, and again
// when it changes (an update); one that couldn't be read is asked again
// after a minute, not on every look.
func TestOwnVersionIsKept(t *testing.T) {
	p := programs(t, "a", "b")
	vers := map[string]string{p[0]: "2.1.38", p[1]: ""}
	reads := stubOwn(t, p, vers)
	// the first's version is where it is installed; the second is run
	own.read = func(path string) string {
		if path == p[0] {
			return "2.1.38"
		}
		return ""
	}
	for range 5 {
		Own()
	}
	if reads[p[0]] != 0 || reads[p[1]] != 1 {
		t.Fatalf("reads after 5 looks: %v", reads)
	}
	vers[p[1]] = "2.1.325"
	if got := Own(); got != p[0] {
		t.Fatalf("an unknown read again before its minute: Own() = %q", got)
	}
	// the second is updated: a new file
	os.WriteFile(p[1], []byte("#!/bin/sh\n# 2.1.325\n"), 0o755)
	later := time.Now().Add(time.Second)
	os.Chtimes(p[1], later, later)
	if got := Own(); got != p[1] {
		t.Fatalf("after the update Own() = %q, want %q", got, p[1])
	}
	if reads[p[0]] != 0 || reads[p[1]] != 2 {
		t.Fatalf("reads: %v", reads)
	}
}

// When no Claude Code's version is known where it is installed, none is
// run to ask it: there is nothing to compare it with, and the first on
// PATH runs (a test's stand-in or fake is never run for its version).
func TestOwnRunsNoneWhenNoneIsKnown(t *testing.T) {
	p := programs(t, "a", "b")
	reads := stubOwn(t, p, map[string]string{p[0]: "2.1.38", p[1]: "2.1.325"})
	own.read = func(string) string { return "" }
	if got := Own(); got != p[0] {
		t.Errorf("Own() = %q, want the first", got)
	}
	if len(reads) != 0 {
		t.Errorf("run for their versions: %v", reads)
	}
}

// Where each installer puts Claude Code says its version: npm's
// package.json, the native installer's versions/<version>, Homebrew's
// Caskroom/claude-code/<version>; a script elsewhere says none.
func TestInstalledVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	root := t.TempDir()
	write := func(rel, body string) string {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	link := func(target, rel string) string {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cli := write("npm/lib/node_modules/@anthropic-ai/claude-code/cli.js", "#!/usr/bin/env node\n")
	write("npm/lib/node_modules/@anthropic-ai/claude-code/package.json", `{"name":"@anthropic-ai/claude-code","version":"2.1.38"}`)
	native := write("home/.local/share/claude/versions/2.1.316", "binary")
	cask := write("brew/Caskroom/claude-code/2.1.325/claude", "binary")
	cases := map[string]string{
		link(cli, "npm/bin/claude"):                  "2.1.38",
		link(native, "home/.local/bin/claude"):       "2.1.316",
		link(cask, "brew/bin/claude"):                "2.1.325",
		write("other/claude", "#!/bin/sh\nexit 1\n"): "",
	}
	for p, want := range cases {
		if got := installedVersion(p); got != want {
			t.Errorf("installedVersion(%s) = %q, want %q", p, got, want)
		}
	}
}
