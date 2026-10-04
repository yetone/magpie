package agent

import (
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// A model picked in Claude Code's /model holds for its next sessions (the
// owner: 去掉 ANTHROPIC_MODEL，让 /model 的选择对新会话也生效): magpie names the
// main model in settings.json's model alone, which /model saves, and no
// ANTHROPIC_MODEL outranks it. The tiers that followed the main model go
// with the pick (Follow); one with a model of its own keeps it. An older
// magpie's ANTHROPIC_MODEL comes out the same way.
func TestClaudeModelPickedInClaudeCode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash", "lite"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{}`)
	a := claude(home)
	env := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return v }
	set := func(key, v string) {
		t.Helper()
		if err := a.Field(key).Set(v); err != nil {
			t.Fatal(err)
		}
	}

	set("model", "deepseek/pro")
	set("haiku", "deepseek/lite")
	if m, _ := edit.GetJSON(path, "model"); m != "deepseek/pro" || env("ANTHROPIC_MODEL") != "" {
		t.Fatalf("the main model is settings.json's alone:\n%s", readFile(path))
	}

	// /model in Claude Code saves its pick there
	if err := edit.SetJSON(path, edit.KV{Path: "model", Value: "deepseek/flash"}); err != nil {
		t.Fatal(err)
	}
	if v := a.Values(); v["model"] != "deepseek/flash" || v["opus"] != "" || v["haiku"] != "deepseek/lite" {
		t.Fatalf("the pick is the main model, the tiers that followed still follow: %v", v)
	}
	if got := claudeStandInAt(path, "claude-sonnet-5", gateway.URL()); got != "deepseek/pro" {
		t.Fatalf("sonnet's stand-in before Follow: %q", got)
	}
	if err := a.Follow(); err != nil {
		t.Fatal(err)
	}
	if m, _ := edit.GetJSON(path, "model"); m != "deepseek/flash" || env("ANTHROPIC_DEFAULT_OPUS_MODEL") != "deepseek/flash" ||
		env("ANTHROPIC_DEFAULT_SONNET_MODEL") != "deepseek/flash" || env("ANTHROPIC_DEFAULT_HAIKU_MODEL") != "deepseek/lite" {
		t.Fatalf("followers go with the pick, haiku keeps its own:\n%s", readFile(path))
	}
	if got := claudeStandInAt(path, "some-unknown-model", gateway.URL()); got != "deepseek/flash" {
		t.Fatalf("a model of no tier stands in for the pick: %q", got)
	}

	// an alias picked in /model is the model its tier names
	if err := edit.SetJSON(path, edit.KV{Path: "model", Value: "haiku"}); err != nil {
		t.Fatal(err)
	}
	if v := a.Values(); v["model"] != "deepseek/lite" {
		t.Fatalf("haiku picked: %v", v)
	}
	if err := a.Follow(); err != nil {
		t.Fatal(err)
	}

	// an older magpie's ANTHROPIC_MODEL comes out, the model kept
	writeFile(t, path, `{"model": "deepseek/pro", "env": {"ANTHROPIC_BASE_URL": "`+gateway.URL()+`", "ANTHROPIC_AUTH_TOKEN": "magpie", "ANTHROPIC_MODEL": "deepseek/pro", "ANTHROPIC_DEFAULT_OPUS_MODEL": "deepseek/pro", "CLAUDE_CODE_SUBAGENT_MODEL": "deepseek/pro"}}`)
	forget(a.ID + ".main")
	forget("claude.main")
	if err := a.Follow(); err != nil {
		t.Fatal(err)
	}
	if m, _ := edit.GetJSON(path, "model"); m != "deepseek/pro" || env("ANTHROPIC_MODEL") != "" || env("CLAUDE_CODE_SUBAGENT_MODEL") != "" || env("ANTHROPIC_DEFAULT_OPUS_MODEL") != "deepseek/pro" {
		t.Fatalf("legacy ANTHROPIC_MODEL:\n%s", readFile(path))
	}
	// nothing to follow: settings.json isn't written
	before := readFile(path)
	if err := a.Follow(); err != nil {
		t.Fatal(err)
	}
	if readFile(path) != before {
		t.Fatal("Follow wrote with nothing to follow")
	}
}
