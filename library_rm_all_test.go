package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/library"
)

// magpie library skill rm --all (#449): with nobody at a terminal to ask,
// it takes nothing unless --yes says so; with --yes, every skill goes, a
// folder of the user's only unlinked.
func TestLibrarySkillRmAll(t *testing.T) {
	h := t.TempDir()
	for k, v := range map[string]string{"HOME": h, "USERPROFILE": h, "XDG_CONFIG_HOME": filepath.Join(h, ".config"), "CLAUDE_CONFIG_DIR": "", "CODEX_HOME": "", "APPDATA": "", "LOCALAPPDATA": ""} {
		t.Setenv(k, v)
	}
	write := func(p, s string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(h, ".claude/settings.json"), "")
	src := filepath.Join(h, "src")
	for _, n := range []string{"pdf", "xlsx"} {
		write(filepath.Join(src, n, "SKILL.md"), "---\nname: "+n+"\ndescription: "+n+"\n---\n")
	}
	if _, err := library.InstallSkills(src, []string{"pdf", "xlsx"}, []string{"claude"}); err != nil {
		t.Fatal(err)
	}

	err := libraryCmd([]string{"library", "skill", "rm", "--all"})
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("with no terminal and no --yes: %v", err)
	}
	if v, _ := library.Read(nil); len(v.Skills) != 2 {
		t.Fatalf("taken without asking: %d left", len(v.Skills))
	}
	if err := libraryCmd([]string{"library", "skill", "rm", "--all", "--nope"}); err == nil {
		t.Error("an unknown flag was taken")
	}

	if err := libraryCmd([]string{"library", "skill", "rm", "--all", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if v, _ := library.Read(nil); len(v.Skills) != 0 {
		t.Errorf("left: %d", len(v.Skills))
	}
	for _, n := range []string{"pdf", "xlsx"} {
		if _, err := os.Lstat(filepath.Join(h, ".claude/skills", n)); !os.IsNotExist(err) {
			t.Errorf("claude still has %s", n)
		}
		if _, err := os.Stat(filepath.Join(src, n, "SKILL.md")); err != nil {
			t.Errorf("the user's %s went with it", n)
		}
	}
	if err := libraryCmd([]string{"library", "skill", "rm", "--all", "--yes"}); err != nil {
		t.Errorf("an empty library: %v", err)
	}
}
