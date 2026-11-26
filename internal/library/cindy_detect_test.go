package library

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

// T_t__biu on X (#7872): with no Pi installed, the instructions the
// Library gave Cindy went to Pi's ~/.pi/agent/AGENTS.md, which Cindy's Pi
// reads (#7854), and that file alone then had Pi listed, "CLI not found",
// with an applied.pi of its own in library.json. It stays Cindy's: no Pi
// is detected or given anything, and Cindy keeps its file.
func TestCindyInstructionsListNoPi(t *testing.T) {
	h := sandbox(t)
	os.RemoveAll(filepath.Join(h, ".pi"))
	os.Remove(filepath.Join(h, "bin", "pi"))
	os.Remove(filepath.Join(h, "bin", "pi.exe"))
	cindyHere(t, h)
	shared := "Use tabs."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"cindy"}}))
	file := filepath.Join(h, ".pi", "agent", "AGENTS.md")
	want := blockBegin + "\nUse tabs.\n" + blockEnd + "\n"
	if s := read(t, file); s != want {
		t.Fatalf("Pi's AGENTS.md for Cindy:\n%q", s)
	}
	a, err := agent.Find("pi")
	if err != nil {
		t.Fatal(err)
	}
	if a.Detected() || a.CLIMissing() {
		t.Fatalf("Pi listed for the AGENTS.md written for Cindy: detected %v, CLI missing %v", a.Detected(), a.CLIMissing())
	}
	if tg := targetByID("pi"); tg != nil {
		t.Fatalf("a pi target: %+v", tg)
	}
	// an apply after it changes nothing, and records nothing for Pi
	ok(t)(Sync())
	if s := read(t, file); s != want {
		t.Errorf("Pi's AGENTS.md after an apply:\n%q", s)
	}
	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if p := l.Applied["pi"]; p != nil && p.Instructions {
		t.Errorf("applied.pi recorded: %+v", p)
	}
	v, err := ReadInstructions()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range v.Agents {
		if r.Agent == "pi" {
			t.Errorf("a Pi row on the instructions page: %+v", r)
		}
	}
}

// T_t__biu on X (#7872): Cindy's bundled Claude Code made ~/.claude and
// ~/.claude.json, and Claude Code was listed with none of the user's
// here. It isn't now, while the Library still gives that folder what
// Cindy's copy reads there: CLAUDE.md, the servers in ~/.claude.json and
// the skills.
func TestCindyClaudeKeepsClaudeLibraryRow(t *testing.T) {
	h := sandbox(t)
	os.Remove(filepath.Join(h, "bin", "claude"))
	os.Remove(filepath.Join(h, "bin", "claude.exe"))
	dir := cindyHere(t, h)
	write(t, filepath.Join(dir, "claude-code", "2.1.321", "claude"), "")
	write(t, filepath.Join(dir, "claude-code", "2.1.321", "claude.exe"), "")
	write(t, filepath.Join(h, ".claude.json"), `{"numStartups":1}`)
	a, err := agent.Find("claude")
	if err != nil {
		t.Fatal(err)
	}
	if a.Detected() {
		t.Fatal("Claude Code listed for the ~/.claude Cindy's copy made")
	}
	tg := targetByID("claude")
	if tg == nil {
		t.Fatalf("no claude target: %v", ids(Targets()))
	}
	if tg.Agent.Name != "Claude Code" || tg.Instructions != filepath.Join(h, ".claude", "CLAUDE.md") || tg.MCP == nil || tg.MCP.Path != filepath.Join(h, ".claude.json") {
		t.Errorf("claude target: %+v", tg)
	}
	shared := "Use tabs."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"claude"}}))
	if s := read(t, filepath.Join(h, ".claude", "CLAUDE.md")); s != blockBegin+"\nUse tabs.\n"+blockEnd+"\n" {
		t.Errorf("CLAUDE.md for Cindy's copy:\n%q", s)
	}
}
