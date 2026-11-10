package appdir

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The folders Claude Code is run in with CLAUDE_CONFIG_DIR set, one account
// each, are found beside ~/.claude: ~/.claude-work, one folder down in
// ~/.claude-profiles, a saved account's own, and not a folder Claude Code
// kept no session in, nor ~/.claude a second time through a link.
func TestClaudeHomesIn(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	mk := func(p string) string {
		p = filepath.Join(home, p)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	main := mk(".claude/projects")
	work := mk(".claude-work/projects")
	nested := mk(".claude-profiles/b/projects")
	mk(".claude-notes/todo")
	mk("projects")
	saved := mk("cfg/magpie/claude-accounts/abc/projects")
	if err := os.Symlink(filepath.Dir(main), filepath.Join(home, ".claude-link")); err != nil {
		t.Skip(err)
	}
	got := ClaudeHomesIn(home)
	for _, w := range []string{filepath.Dir(work), filepath.Dir(nested), filepath.Dir(saved)} {
		if !slices.Contains(got, w) {
			t.Errorf("ClaudeHomesIn = %v, lacks %s", got, w)
		}
	}
	if len(got) != 4 || got[0] != filepath.Dir(main) {
		t.Fatalf("ClaudeHomesIn = %v", got)
	}
}
