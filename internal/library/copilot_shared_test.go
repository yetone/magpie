package library

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

// GitHub Copilot reads ~/.copilot/copilot-instructions.md as the user's own
// instructions in all three of its surfaces: Copilot CLI, VS Code (its
// promptsServiceImpl reads the Copilot harness's user folder) and the
// JetBrains plugin (getCopilotCliHomePath()). The Library's Instructions
// had a row for it only with Copilot CLI installed, named Copilot CLI
// (@codealc on X: 资源库 › 指令 has no GitHub Copilot).

// noCopilotCLI takes Copilot CLI away: no settings.json, no command.
func noCopilotCLI(t *testing.T, h string) {
	t.Helper()
	os.RemoveAll(filepath.Join(h, ".copilot"))
	os.Remove(filepath.Join(h, "bin", "copilot"))
	os.Remove(filepath.Join(h, "bin", "copilot.exe"))
}

// vscodeUser makes VS Code's User folder, as VS Code does on its first start.
func vscodeUser(t *testing.T, h string) {
	t.Helper()
	var d string
	switch runtime.GOOS {
	case "darwin":
		d = filepath.Join(h, "Library", "Application Support", "Code", "User")
	case "windows":
		d = filepath.Join(h, "AppData", "Roaming", "Code", "User")
	default:
		d = filepath.Join(h, ".config", "Code", "User")
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
}

// jetBrainsCopilot installs GitHub Copilot's JetBrains plugin in an IDE.
func jetBrainsCopilot(t *testing.T, h string) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", "")
	var d string
	switch runtime.GOOS {
	case "darwin":
		d = filepath.Join(h, "Library", "Application Support", "JetBrains", "IntelliJIdea2026.2", "plugins")
	case "windows":
		d = filepath.Join(h, "AppData", "Roaming", "JetBrains", "IntelliJIdea2026.2", "plugins")
	default:
		d = filepath.Join(h, ".local", "share", "JetBrains", "IntelliJIdea2026.2")
	}
	if err := os.MkdirAll(filepath.Join(d, "github-copilot-intellij"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func copilotRow(t *testing.T, h string) {
	t.Helper()
	c := targetByID("copilot")
	if c == nil {
		t.Fatalf("no copilot target: %v", ids(Targets()))
	}
	if c.Agent.Name != "GitHub Copilot" {
		t.Errorf("the Library's row is %q, want GitHub Copilot", c.Agent.Name)
	}
	if want := filepath.Join(h, ".copilot", "copilot-instructions.md"); c.Instructions != want {
		t.Errorf("instructions go to %q, want %q", c.Instructions, want)
	}
}

func TestCopilotLibraryRowIsGitHubCopilot(t *testing.T) {
	h := sandbox(t)
	copilotRow(t, h)
	a, err := agent.Find("copilot")
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "Copilot CLI" {
		t.Errorf("the agent is %q after the Library named its row, want Copilot CLI", a.Name)
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, av := range v.Agents {
		if av.ID == "copilot" && av.Name != "GitHub Copilot" {
			t.Errorf("the Library's view names it %q", av.Name)
		}
	}
}

// VS Code alone, with no ~/.copilot yet: the row is there, and saving
// instructions writes the file VS Code reads.
func TestCopilotVSCodeAloneGetsInstructions(t *testing.T) {
	h := sandbox(t)
	noCopilotCLI(t, h)
	if c := targetByID("copilot"); c != nil {
		t.Fatalf("a copilot target with no Copilot here: %+v", c)
	}
	vscodeUser(t, h)
	copilotRow(t, h)
	shared := "Answer in Chinese."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"copilot"}}))
	p := filepath.Join(h, ".copilot", "copilot-instructions.md")
	if s := read(t, p); s != blockBegin+"\nAnswer in Chinese.\n"+blockEnd+"\n" {
		t.Errorf("%s:\n%q", p, s)
	}
	a, err := agent.Find("copilot")
	if err != nil {
		t.Fatal(err)
	}
	if a.Detected() {
		t.Error("the instructions magpie wrote in ~/.copilot made Copilot CLI detected")
	}
}

func TestCopilotJetBrainsAloneGetsInstructions(t *testing.T) {
	h := sandbox(t)
	noCopilotCLI(t, h)
	jetBrainsCopilot(t, h)
	copilotRow(t, h)
}

// The user's own copilot-instructions.md is kept, with magpie's block
// beside it, and comes back whole when magpie's instructions are off.
func TestCopilotInstructionsKeepTheUsersOwn(t *testing.T) {
	h := sandbox(t)
	noCopilotCLI(t, h)
	vscodeUser(t, h)
	p := filepath.Join(h, ".copilot", "copilot-instructions.md")
	mine := "# Mine\n\nUse tabs.\n"
	write(t, p, mine)
	shared := "Answer in Chinese."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"copilot"}}))
	s := read(t, p)
	if !strings.Contains(s, "Use tabs.") || !strings.Contains(s, "Answer in Chinese.") {
		t.Errorf("%s:\n%s", p, s)
	}
	empty := ""
	ok(t)(SaveInstructions(InstructionsChange{Shared: &empty, Agents: []string{"copilot"}}))
	if s := read(t, p); strings.TrimSpace(s) != strings.TrimSpace(mine) {
		t.Errorf("after magpie's instructions are gone:\n%q", s)
	}
}

// Copilot CLI uninstalled, its settings.json left behind: the Agents page
// calls it gone, but VS Code still reads ~/.copilot, so the row stays; with
// no app reading it, it goes.
func TestCopilotCLIGoneVSCodeKeepsTheRow(t *testing.T) {
	h := sandbox(t)
	os.Remove(filepath.Join(h, "bin", "copilot"))
	os.Remove(filepath.Join(h, "bin", "copilot.exe"))
	if c := targetByID("copilot"); c != nil {
		t.Fatalf("a leftover ~/.copilot with nothing reading it is a target: %+v", c)
	}
	vscodeUser(t, h)
	copilotRow(t, h)
}

func TestCopilotInstructionsFollowCopilotHome(t *testing.T) {
	h := sandbox(t)
	noCopilotCLI(t, h)
	vscodeUser(t, h)
	d := filepath.Join(h, "ch")
	t.Setenv("COPILOT_HOME", d)
	c := targetByID("copilot")
	if c == nil {
		t.Fatalf("no copilot target: %v", ids(Targets()))
	}
	if want := filepath.Join(d, "copilot-instructions.md"); c.Instructions != want {
		t.Errorf("instructions go to %q, want %q", c.Instructions, want)
	}
}
