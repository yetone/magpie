package agent

import (
	"path/filepath"
	"reflect"
	"testing"
)

// The install commands are the vendors' own (#727): the installer their
// docs give first, then npm's package, the one the update uses; per OS.
func TestInstallCommands(t *testing.T) {
	cmds := func(id, goos string) []string {
		var out []string
		for _, c := range installCommands(id, goos) {
			out = append(out, c.Via+": "+c.Command)
		}
		return out
	}
	for _, c := range []struct {
		id, goos string
		want     []string
	}{
		{"claude", "darwin", []string{"script: curl -fsSL https://claude.ai/install.sh | bash", "npm: npm install -g @anthropic-ai/claude-code"}},
		{"claude", "windows", []string{"powershell: irm https://claude.ai/install.ps1 | iex", "npm: npm install -g @anthropic-ai/claude-code"}},
		{"codex", "darwin", []string{"script: curl -fsSL https://chatgpt.com/codex/install.sh | sh", "brew: brew install --cask codex", "npm: npm install -g @openai/codex"}},
		{"codex", "linux", []string{"script: curl -fsSL https://chatgpt.com/codex/install.sh | sh", "npm: npm install -g @openai/codex"}},
		{"gemini", "linux", []string{"brew: brew install gemini-cli", "npm: npm install -g @google/gemini-cli"}},
		{"gemini", "windows", []string{"npm: npm install -g @google/gemini-cli"}},
		{"opencode", "darwin", []string{"script: curl -fsSL https://opencode.ai/install | bash", "npm: npm install -g opencode-ai"}},
		{"opencode", "windows", []string{"npm: npm install -g opencode-ai"}},
		{"pi", "linux", []string{"npm: npm install -g @earendil-works/pi-coding-agent"}},
		{"cursor", "darwin", nil}, // no command magpie knows
	} {
		if got := cmds(c.id, c.goos); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s on %s: %q, want %q", c.id, c.goos, got, c.want)
		}
	}
}

// Only the agents not on this machine are offered, and not WSL's twins.
func TestInstallsOnlyMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	here := &Agent{ID: "codex", Name: "Codex", Path: filepath.Join(home, "codex.toml"), Dir: home}
	gone := &Agent{ID: "gemini", Name: "Gemini CLI", Icon: "gemini", Path: filepath.Join(home, "nope", "settings.json"), Bin: "gemini"}
	wsl := &Agent{ID: "claude@wsl:Ubuntu", Name: "Claude Code", WSL: "Ubuntu", Path: filepath.Join(home, "nope", "x")}
	none := &Agent{ID: "cursor", Name: "Cursor", Path: filepath.Join(home, "nope", "y")}
	got := installsOf([]*Agent{here, gone, wsl, none}, "darwin")
	if len(got) != 1 || got[0].ID != "gemini" || got[0].Name != "Gemini CLI" || got[0].Icon != "gemini" || len(got[0].Commands) != 2 {
		t.Fatalf("installs: %+v", got)
	}
	if got := installsOf(nil, "linux"); got == nil {
		t.Error("none is [] for the page, not null")
	}
}
