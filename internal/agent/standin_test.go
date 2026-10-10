package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/usage"
)

func TestClaudeStandIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"env":{"ANTHROPIC_BASE_URL":"` + gateway.URL() + `","ANTHROPIC_MODEL":"a/main","ANTHROPIC_DEFAULT_HAIKU_MODEL":"a/small","ANTHROPIC_DEFAULT_OPUS_MODEL":"a/big"}}`)
	for asked, want := range map[string]string{
		"claude-haiku-4-5-20251001": "a/small",
		"claude-opus-5-5":           "a/big",
		"claude-sonnet-5":           "a/main", // a tier without a model follows the main one
		"gpt-5":                     "a/main",
	} {
		if got := claudeStandIn(path, asked); got != want {
			t.Errorf("%s: %q, want %q", asked, got, want)
		}
	}
	// every version of a family is its tier's; the main model, named by
	// its own id, is the main model's
	write(`{"env":{"ANTHROPIC_BASE_URL":"` + gateway.URL() + `","ANTHROPIC_MODEL":"claude-opus-5[1m]","ANTHROPIC_DEFAULT_OPUS_MODEL":"a/big:high[1m]","ANTHROPIC_DEFAULT_SONNET_MODEL":"a/mid","ANTHROPIC_DEFAULT_FABLE_MODEL":"a/fab[1m]"}}`)
	for asked, want := range map[string]string{
		"claude-opus-5":              "claude-opus-5[1m]",
		"claude-opus-5-5":            "a/big:high[1m]",
		"claude-sonnet-5-5-20261001": "a/mid",
		"claude-fable-5-1[1m]":       "a/fab[1m]",
	} {
		if got := claudeStandIn(path, asked); got != want {
			t.Errorf("%s: %q, want %q", asked, got, want)
		}
	}
	// Claude Code on Anthropic's own endpoint: nothing stands in
	write(`{"env":{"ANTHROPIC_MODEL":"a/main"}}`)
	if got := claudeStandIn(path, "claude-haiku-4-5"); got != "" {
		t.Errorf("not routed: %q", got)
	}
	if got := StandIn("gemini", "claude-haiku-4-5"); got != "" {
		t.Errorf("another agent: %q", got)
	}
}

// Codex on magpie as its provider asks for its auto-review by OpenAI's
// name, codex-auto-review (shihao H on X: 404 magpie knows no model
// "codex-auto-review"); it goes to the model Codex is set to. Signed in
// (routed by openai_base_url) or not routed, nothing stands in.
func TestCodexStandIn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(s string) {
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("model = \"deepseek/deepseek-v4\"\nmodel_provider = \"magpie\"\n\n[model_providers.magpie]\nname = \"magpie\"\n")
	if got := StandIn("codex", "codex-auto-review"); got != "deepseek/deepseek-v4" {
		t.Errorf("magpie as provider: %q", got)
	}
	// the gateway knows the request as Codex's by its User-Agent
	for _, ua := range []string{
		"codex_cli_rs/0.160.0 (Mac OS 26.6.0; arm64) Apple_Terminal/455",
		"Codex Desktop/0.160.0",
		"acp-extension-codex/0.160.1 (Mac OS 26.2.0; arm64) unknown (acp-extension-codex; 0.9.0)",
	} {
		if got := usage.AgentOf(ua); got != "codex" {
			t.Errorf("%s: agent %q", ua, got)
		}
	}
	write("model = \"gpt-5.5\"\nopenai_base_url = \"" + gateway.URL() + gateway.CodexPath + "\"\n")
	if got := StandIn("codex", "codex-auto-review"); got != "" {
		t.Errorf("signed in: %q", got)
	}
	write("model = \"gpt-5.5\"\n")
	if got := StandIn("codex", "codex-auto-review"); got != "" {
		t.Errorf("not routed: %q", got)
	}
}
