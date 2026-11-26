package library

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

// T_t__biu on X: Cindy's bundled Codex left ~/.codex behind, and Codex was
// listed with none of the user's here. It isn't now, while the Library
// still gives that folder what Cindy's copy reads there: AGENTS.md, which
// Cindy copies into its codex-home, and the skills it links.
func TestCindyCodexKeepsCodexLibraryRow(t *testing.T) {
	h := sandbox(t)
	os.Remove(filepath.Join(h, "bin", "codex"))
	os.Remove(filepath.Join(h, "bin", "codex.exe"))
	dir := cindyHere(t, h)
	write(t, filepath.Join(dir, "codex-package", "0.130.0", "bin", "codex"), "")
	write(t, filepath.Join(dir, "codex-package", "0.130.0", "bin", "codex.exe"), "")
	write(t, filepath.Join(h, ".codex", "tmp", "arg0-darwin", "codex-arg0Ab12", "apply_patch"), "")
	a, err := agent.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if a.Detected() {
		t.Fatal("Codex listed for the ~/.codex Cindy's copy left")
	}
	tg := targetByID("codex")
	if tg == nil {
		t.Fatalf("no codex target: %v", ids(Targets()))
	}
	if tg.Agent.Name != "Codex" || tg.Instructions != filepath.Join(h, ".codex", "AGENTS.md") || tg.MCP == nil || tg.MCP.Path != filepath.Join(h, ".codex", "config.toml") {
		t.Errorf("codex target: %+v", tg)
	}
	shared := "Use tabs."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"codex"}}))
	if s := read(t, filepath.Join(h, ".codex", "AGENTS.md")); s != blockBegin+"\nUse tabs.\n"+blockEnd+"\n" {
		t.Errorf("AGENTS.md for Cindy's copy:\n%q", s)
	}

	// before Cindy's copy has made ~/.codex, or with only the skills
	// Cindy put there, the row is there too: Cindy's copy reads the
	// folder's AGENTS.md and skills whenever they are there
	if err := os.RemoveAll(filepath.Join(h, ".codex")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(h, ".codex", "skills", "learned", "SKILL.md"), "x")
	if tg := targetByID("codex"); tg == nil || tg.Instructions != filepath.Join(h, ".codex", "AGENTS.md") {
		t.Errorf("no codex target with only Cindy's skills in ~/.codex: %+v", tg)
	}
}
