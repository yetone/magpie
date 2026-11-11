package appdir

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// A Sessions poll looks for the Claude and Codex folders every time, and
// the look told one folder from another through EvalSymlinks, which
// allocated for each part of each name (#2278). A link to a folder is
// still the folder, in a home with more of each.
func TestHomesLookAllocatesLittle(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	for _, p := range []string{".claude/projects", ".claude-a/projects", ".claude-b/projects", ".codex/sessions", ".codex-a/sessions", ".codex-b/sessions"} {
		if err := os.MkdirAll(filepath.Join(home, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(home, ".claude-a"), filepath.Join(home, ".claude-link")); err != nil {
		t.Skip(err)
	}
	os.Symlink(filepath.Join(home, ".codex"), filepath.Join(home, ".codex-link"))
	if got := ClaudeHomesIn(home); len(got) != 3 || slices.Contains(got, filepath.Join(home, ".claude-link")) {
		t.Fatalf("ClaudeHomesIn = %v", got)
	}
	if got := CodexHomesIn(home); len(got) != 3 || slices.Contains(got, filepath.Join(home, ".codex-link")) {
		t.Fatalf("CodexHomesIn = %v", got)
	}
	if runtime.GOOS == "windows" {
		return
	}
	claude := testing.AllocsPerRun(20, func() { ClaudeHomesIn(home) })
	codex := testing.AllocsPerRun(20, func() { CodexHomesIn(home) })
	t.Logf("allocations: claude %.0f, codex %.0f", claude, codex)
	if claude > 120 || codex > 120 {
		t.Fatalf("a look allocated %.0f (claude) and %.0f (codex) times", claude, codex)
	}
}
