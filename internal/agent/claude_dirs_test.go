package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// BieJian21872 on X: a second Claude Code (CLAUDE_CONFIG_DIR=~/.claude-work)
// was never found; only ~/.claude was. Each extra folder holding Claude
// Code's files is a row of its own, connected in its own settings.json.
func TestClaudeCodeInMoreThanOneFolder(t *testing.T) {
	home, _ := claudeDottedHome(t, provider.Provider{ID: "relay", Name: "Relay", Anthropic: "https://example.test", Key: "k",
		Models: []string{"claude-sonnet-4.5", "glm-4.6"}}, `{}`)
	work := filepath.Join(home, ".claude-work")
	writeFile(t, filepath.Join(work, "settings.json"), `{"theme": "dark"}`)
	writeFile(t, filepath.Join(home, ".claude2", "projects", "p", "s.jsonl"), `{}`)
	// not Claude Code's: another tool's folder, a file, a backup, the launch file's name
	writeFile(t, filepath.Join(home, ".claude-code-router", "config.json"), `{}`)
	writeFile(t, filepath.Join(home, ".claude.json"), `{}`)
	writeFile(t, filepath.Join(home, ".claude.json.backup"), `{}`)
	writeFile(t, filepath.Join(home, ".claude-launch", "settings.json"), `{}`)
	t.Setenv("PATH", t.TempDir())

	got := map[string]*Agent{}
	for _, a := range claudeDirAgents(home) {
		got[a.ID] = a
	}
	if len(got) != 2 || got["claude#work"] == nil || got["claude#2"] == nil {
		t.Fatalf("rows %v, want claude#work and claude#2 only", got)
	}
	// listed on the Agents page with the others
	listed := false
	for _, b := range onMachine(home, filepath.Join(home, ".config")) {
		listed = listed || b.ID == "claude#work"
	}
	if !listed {
		t.Fatal("claude#work isn't among the agents magpie lists")
	}
	a := got["claude#work"]
	if a.Dir != work || !a.Detected() || a.Name != "Claude Code · work" {
		t.Fatalf("row %q at %s detected %v", a.Name, a.Dir, a.Detected())
	}
	if err := a.Field("model").Set("relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(work, "settings.json")
	if v, _ := edit.GetJSON(path, "env.ANTHROPIC_BASE_URL"); v != gateway.URL() {
		b, _ := os.ReadFile(path)
		t.Fatalf("%s doesn't point at magpie:\n%s", path, b)
	}
	if v, _ := edit.GetJSON(path, "theme"); v != "dark" {
		t.Fatalf("the user's theme is %q", v)
	}
	// the default row is untouched
	if v, _ := edit.GetJSON(filepath.Join(home, ".claude", "settings.json"), "env.ANTHROPIC_BASE_URL"); v != "" {
		t.Fatalf("~/.claude was written: %q", v)
	}
	if !claude(home).Detected() {
		t.Fatal("the default row is gone")
	}

	// with CLAUDE_CONFIG_DIR at one of them, the others (~/.claude too) are rows
	t.Setenv("CLAUDE_CONFIG_DIR", work)
	ids := map[string]bool{}
	for _, a := range claudeDirAgents(home) {
		ids[a.ID] = true
	}
	if ids["claude#work"] || !ids["claude#default"] || !ids["claude#2"] {
		t.Fatalf("rows with CLAUDE_CONFIG_DIR at .claude-work: %v", ids)
	}
}
