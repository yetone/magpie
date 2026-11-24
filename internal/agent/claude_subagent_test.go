package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// #7176: the gateway moves Claude Code's Explore and Plan to the model set
// for its subagents, read here from the settings Claude Code runs on: the
// model the Subagents field wrote, at its effort, while they send Claude
// Code through magpie; nothing once it is cleared, for a value of the
// user's own that isn't magpie's, or for settings not routed here.
func TestClaudeSubagentReadsTheSetModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	a := claude(home)
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if got := ClaudeSubagent("claude"); got != "" {
		t.Fatalf("no subagent model set, read %q", got)
	}
	if err := a.Field("subagent").Set("deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if got := ClaudeSubagent("claude"); got != "deepseek/flash" {
		t.Fatalf("read %q, want deepseek/flash", got)
	}
	if got := ClaudeSubagent("claude-desktop"); got != "" {
		t.Fatalf("Desktop read Claude Code's subagent model %q", got)
	}
	if err := a.Field("subagent_effort").Set("high"); err != nil {
		t.Fatal(err)
	}
	if got := ClaudeSubagent("claude"); got != "deepseek/flash:high" {
		t.Fatalf("read %q, want deepseek/flash:high", got)
	}
	// the user's own "inherit" is Claude Code's to read
	own, _ := edit.GetJSON(path, "env.CLAUDE_CODE_SUBAGENT_MODEL")
	if err := edit.SetJSON(path, edit.KV{Path: "env.CLAUDE_CODE_SUBAGENT_MODEL", Value: "inherit"}); err != nil {
		t.Fatal(err)
	}
	if got := ClaudeSubagent("claude"); got != "" {
		t.Fatalf("the user's inherit read as %q", got)
	}
	if err := edit.SetJSON(path, edit.KV{Path: "env.CLAUDE_CODE_SUBAGENT_MODEL", Value: own}); err != nil {
		t.Fatal(err)
	}
	// Claude Code sent elsewhere: its settings aren't magpie's to read
	if err := edit.SetJSON(path, edit.KV{Path: "env.ANTHROPIC_BASE_URL", Value: "https://api.example.com"}); err != nil {
		t.Fatal(err)
	}
	if got := ClaudeSubagent("claude"); got != "" {
		t.Fatalf("settings not routed through magpie read %q", got)
	}
	if err := edit.SetJSON(path, edit.KV{Path: "env.ANTHROPIC_BASE_URL", Value: here(home).gw()}); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("subagent").Set(""); err != nil {
		t.Fatal(err)
	}
	if got := ClaudeSubagent("claude"); got != "" {
		t.Fatalf("cleared, read %q", got)
	}
}
