package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoteKey(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/yetone/magpie.git":        "github.com/yetone/magpie",
		"https://GitHub.com/yetone/magpie/":           "github.com/yetone/magpie",
		"git@github.com:yetone/magpie.git":            "github.com/yetone/magpie",
		"ssh://git@github.com:22/yetone/magpie":       "github.com/yetone/magpie",
		"https://user:tok@gitlab.example.com/g/sub/r": "gitlab.example.com/g/sub/r",
		"file:///srv/repos/x.git":                     "",
		"/srv/repos/x.git":                            "",
		`C:\repos\x`:                                  "",
		"":                                            "",
	} {
		if got := RemoteKey(in); got != want {
			t.Errorf("RemoteKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func put(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A clone, its worktree and a folder deep in either are one project.
func TestProjectOfIsTheRepository(t *testing.T) {
	d := t.TempDir()
	main := filepath.Join(d, "magpie")
	put(t, filepath.Join(main, ".git", "config"), "[core]\n\tbare = false\n[remote \"upstream\"]\n\turl = git@github.com:other/fork.git\n[remote \"origin\"]\n\turl = https://github.com/yetone/magpie.git\n")
	wt := filepath.Join(d, "wt", "memory")
	put(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(main, ".git", "worktrees", "memory")+"\n")
	put(t, filepath.Join(main, ".git", "worktrees", "memory", "commondir"), "../..\n")
	deep := filepath.Join(wt, "internal", "memory")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{main, deep} {
		p := ProjectOf(dir)
		if p == nil || p.Key != "github.com/yetone/magpie" || p.Name != "magpie" {
			t.Fatalf("ProjectOf(%s) = %+v", dir, p)
		}
	}
	if p := ProjectOf(deep); p.Roots[0] != wt {
		t.Fatalf("the worktree's root is %v", p.Roots)
	}

	// no remote: the main checkout's folder, from its worktree too
	put(t, filepath.Join(main, ".git", "config"), "[core]\n\tbare = false\n")
	if p := ProjectOf(deep); p.Key != "dir:"+filepath.ToSlash(main) {
		t.Fatalf("with no remote: %+v", p)
	}
	// outside a repository: the folder itself
	plain := filepath.Join(d, "notes")
	if p := ProjectOf(plain); p.Key != "dir:"+filepath.ToSlash(plain) || ScopeOf(p) != "project:dir:"+filepath.ToSlash(plain) {
		t.Fatalf("a plain folder: %+v", p)
	}
	if ProjectOf("") != nil || ProjectOf("relative/x") != nil || ScopeOf(nil) != "" {
		t.Fatal("no folder made a project")
	}
}
