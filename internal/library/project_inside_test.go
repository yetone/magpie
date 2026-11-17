package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func symlinkAt(t *testing.T, to, at string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(to, at); err != nil {
		t.Skip("no symlinks here:", err)
	}
}

func problemAbout(r *Result, what string) bool {
	for _, p := range r.Problems {
		if strings.HasPrefix(p.What, what) {
			return true
		}
	}
	return false
}

// A project is often a repository someone else wrote. A link in it that
// leads out of the project (CLAUDE.md to the user's ~/.zshrc) never has
// magpie write the user's instructions where it points, and a link to
// nothing never has magpie make the file it names.
func TestProjectInstructionsStayInTheProject(t *testing.T) {
	h, dir := instrProject(t)
	rc := filepath.Join(h, ".zshrc")
	write(t, rc, "export A=1\n")
	symlinkAt(t, rc, filepath.Join(dir, "CLAUDE.md"))
	nothing := filepath.Join(h, "made-by-magpie")
	symlinkAt(t, nothing, filepath.Join(dir, "AGENTS.md"))
	text := "Run `make` first."
	r, err := ProjectInstructions(dir, ProjectInstr{Text: &text, Agents: &[]string{"claude", "codex"}})
	if err != nil {
		t.Fatal(err)
	}
	if !problemAbout(r, "project:instructions") {
		t.Errorf("problems: %+v", r.Problems)
	}
	if got := read(t, rc); got != "export A=1\n" {
		t.Errorf("~/.zshrc = %q", got)
	}
	gone(t, nothing)
}

// A link inside the project (AGENTS.md to CLAUDE.md) still takes them.
func TestProjectInstructionsThroughALinkInside(t *testing.T) {
	_, dir := instrProject(t)
	write(t, filepath.Join(dir, "docs", "CLAUDE.md"), "Mine.\n")
	symlinkAt(t, filepath.Join("docs", "CLAUDE.md"), filepath.Join(dir, "CLAUDE.md"))
	setInstr(t, dir, "Here.", "claude")
	if got := read(t, filepath.Join(dir, "docs", "CLAUDE.md")); !strings.Contains(got, block("Here.")) {
		t.Errorf("docs/CLAUDE.md = %q", got)
	}
}

// A folder of the project's that is a link out of it (.claude to a folder
// anyone can read) gets no skill and no settings of the user's.
func TestProjectSkillsStayInTheProject(t *testing.T) {
	_, proj := projectLib(t)
	out := filepath.Join(t.TempDir(), "shared")
	symlinkAt(t, out, filepath.Join(proj, ".claude"))
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := ProjectSkill(proj, "pdf", []string{"claude"})
	if err != nil {
		t.Fatal(err)
	}
	if !problemAbout(r, "project:pdf") {
		t.Errorf("problems: %+v", r.Problems)
	}
	if es, _ := os.ReadDir(out); len(es) != 0 {
		t.Errorf("%s has %v", out, es)
	}
}

// A project's .mcp.json that leads out of it gets none of the servers
// (and none of their keys).
func TestProjectServersStayInTheProject(t *testing.T) {
	h, proj := mcpProject(t)
	out := filepath.Join(h, "public.json")
	write(t, out, "{}\n")
	mcp := filepath.Join(proj, ".mcp.json")
	if err := os.Remove(mcp); err != nil {
		t.Fatal(err)
	}
	symlinkAt(t, out, mcp)
	r, err := ProjectServer(proj, "fs", []string{"claude"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Problems) == 0 {
		t.Error("no problem said")
	}
	if got := read(t, out); got != "{}\n" {
		t.Errorf("%s = %q", out, got)
	}
}
