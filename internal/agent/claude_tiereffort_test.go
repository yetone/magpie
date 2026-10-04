package agent

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// Claude Code's tiers and its subagents can each run at an effort of their
// own (#536): the model the tier is given is "<model>:<level>" (before its
// [1m] mark), which magpie's gateway sends that model at that level. The
// effort stays with the tier through a model of its own, back to the main
// model, and a new main model; the main model's effort is untouched.
func TestClaudeTierEffort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "v", Name: "V", Chat: "https://example.test/v1", Key: "k",
		Models: []string{"glm", "flash", "big"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("v", "https://example.test/v1", []catalog.Model{
		{ID: "glm", Context: 200000, Efforts: []string{"low", "medium", "high", "max"}},
		{ID: "flash", Context: 128000, Efforts: []string{"low", "high"}},
		{ID: "big", Context: 1000000},
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{}`)
	a := claude(home)
	env := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return v }
	set := func(key, v string) {
		t.Helper()
		if err := a.Field(key).Set(v); err != nil {
			t.Fatalf("%s=%s: %v", key, v, err)
		}
	}
	for _, k := range []string{"opus_effort", "sonnet_effort", "haiku_effort", "fable_effort", "subagent_effort"} {
		if a.Field(k) == nil {
			t.Fatalf("no %s field", k)
		}
	}
	if err := a.Field("haiku_effort").Set("low"); err == nil {
		t.Fatal("a tier's effort before Claude Code is routed should fail")
	}

	set("model", "v/glm")
	set("effort", "high")
	// a tier on a model of its own, at an effort of its own
	set("haiku", "v/flash")
	if got := a.Field("haiku_effort").Options(a.Values()); !slices.Equal(optionValues(got), []string{"low", "high"}) {
		t.Fatalf("haiku's levels: %v", got)
	}
	set("haiku_effort", "low")
	if env("ANTHROPIC_DEFAULT_HAIKU_MODEL") != "v/flash:low" || env("ANTHROPIC_SMALL_FAST_MODEL") != "v/flash:low" {
		t.Fatalf("haiku at low: %v", a.Values())
	}
	if a.Field("haiku").Get() != "v/flash" || a.Field("haiku_effort").Get() != "low" || a.Field("sonnet_effort").Get() != "" {
		t.Fatalf("read back: %v", a.Values())
	}
	if v, _ := edit.GetJSON(path, "model"); v != "v/glm" || env("ANTHROPIC_MODEL") != "" || a.Field("effort").Get() != "high" || env("ANTHROPIC_DEFAULT_OPUS_MODEL") != "v/glm" {
		t.Fatalf("the main model's effort changed: %v", a.Values())
	}
	// a tier following the main model, at another effort
	set("sonnet_effort", "medium")
	if env("ANTHROPIC_DEFAULT_SONNET_MODEL") != "v/glm:medium" || a.Field("sonnet").Get() != "" || a.Field("sonnet_effort").Get() != "medium" {
		t.Fatalf("sonnet at medium: %v", a.Values())
	}
	// a 1M model keeps its mark after the effort, where Claude Code takes it off
	set("opus", "v/big")
	set("opus_effort", "high")
	if env("ANTHROPIC_DEFAULT_OPUS_MODEL") != "v/big:high[1m]" || a.Field("opus").Get() != "v/big[1m]" || a.Field("opus_effort").Get() != "high" {
		t.Fatalf("opus at high: %v", a.Values())
	}
	if env(claudeContextEnv) != "200000" {
		t.Fatalf("window: %s", env(claudeContextEnv))
	}
	// the effort stays with the tier through a new model, and back to the
	// main model's
	set("haiku", "v/glm")
	if env("ANTHROPIC_DEFAULT_HAIKU_MODEL") != "v/glm:low" || a.Field("haiku").Get() != "" {
		t.Fatalf("haiku on the main model: %v", a.Values())
	}
	// a new main model: tiers that followed it follow it at their effort,
	// one of its own keeps its model
	set("model", "v/flash")
	if env("ANTHROPIC_DEFAULT_SONNET_MODEL") != "v/flash:medium" || env("ANTHROPIC_DEFAULT_HAIKU_MODEL") != "v/flash:low" ||
		env("ANTHROPIC_DEFAULT_OPUS_MODEL") != "v/big:high[1m]" || env("ANTHROPIC_DEFAULT_FABLE_MODEL") != "v/flash" {
		t.Fatalf("new main model: %v", a.Values())
	}
	// the tier's effort taken away: its model as before
	set("sonnet_effort", "")
	set("haiku_effort", "")
	if env("ANTHROPIC_DEFAULT_SONNET_MODEL") != "v/flash" || env("ANTHROPIC_DEFAULT_HAIKU_MODEL") != "v/flash" {
		t.Fatalf("efforts off: %v", a.Values())
	}
	if err := a.Field("haiku_effort").Set("ultra"); err == nil {
		t.Fatal("a level that isn't one should fail")
	}

	// subagents: at an effort of their own on the main model, then on one of
	// their own, kept through a new main model
	set("opus", "")
	set("opus_effort", "")
	if env("CLAUDE_CODE_SUBAGENT_MODEL") != "" {
		t.Fatalf("subagents follow: %v", a.Values())
	}
	set("subagent_effort", "low")
	if env("CLAUDE_CODE_SUBAGENT_MODEL") != "v/flash:low" || a.Field("subagent").Get() != "" || a.Field("subagent_effort").Get() != "low" {
		t.Fatalf("subagents at low: %v", a.Values())
	}
	set("model", "v/glm")
	if env("CLAUDE_CODE_SUBAGENT_MODEL") != "v/glm:low" || a.Field("subagent").Get() != "" {
		t.Fatalf("subagents follow the new model at low: %v", a.Values())
	}
	set("subagent", "v/flash")
	if env("CLAUDE_CODE_SUBAGENT_MODEL") != "v/flash:low" || a.Field("subagent").Get() != "v/flash" {
		t.Fatalf("subagents on flash at low: %v", a.Values())
	}
	set("subagent", "")
	set("subagent_effort", "")
	if env("CLAUDE_CODE_SUBAGENT_MODEL") != "" || a.Field("subagent_effort").Get() != "" {
		t.Fatalf("subagents back: %v", a.Values())
	}
	// back to Claude Code as installed: nothing of magpie's left
	set("model", "")
	for _, k := range claudeEnv {
		if env(k) != "" {
			t.Fatalf("%s left: %s", k, env(k))
		}
	}
}

func optionValues(os []Option) []string {
	var out []string
	for _, o := range os {
		out = append(out, o.Value)
	}
	return out
}
