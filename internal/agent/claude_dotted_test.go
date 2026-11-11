package agent

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

func claudeDottedHome(t *testing.T, p provider.Provider, settings string) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, settings)
	return home, path
}

// misteer_ti. on Discord: Claude Code 2.1.296 reads a relay's
// relay/claude-opus-4.6-us as Claude Opus 4 (retired, budget thinking,
// 32000 output, no effort), and reads relay/claude-opus-4-6-us as Opus 4.6.
// So every model magpie writes into Claude Code's settings (the main model,
// the tiers, the small fast model, the subagents, /model's picker) names a
// dotted Claude version with dashes; magpie reads that spelling back as the
// model it serves, so the page shows the model picked, and another vendor's
// id and the user's own values are as they are.
func TestClaudeDottedVersionsGoInDashed(t *testing.T) {
	home, path := claudeDottedHome(t, provider.Provider{ID: "relay", Name: "Relay", Anthropic: "https://example.test", Key: "k",
		Models: []string{"claude-opus-4.6-us", "claude-sonnet-4.5", "claude-haiku-4.5", "gpt-5.1", "glm-4.6"}},
		`{"env": {"ANTHROPIC_DEFAULT_HAIKU_MODEL": "claude-haiku-4.5"}}`)
	a := claude(home)
	env := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return v }
	model := func() string { return writtenModel(path) }
	set := func(key, v string) {
		t.Helper()
		if err := a.Field(key).Set(v); err != nil {
			t.Fatalf("%s=%s: %v", key, v, err)
		}
	}
	// no model magpie wrote has a dotted Claude version
	noDots := func() {
		t.Helper()
		vals := []string{model()}
		for _, k := range claudeModelEnv {
			vals = append(vals, env(k))
		}
		var picker struct{ Options []struct{ Model string } }
		raw, _ := edit.GetJSON(path, "modelPicker")
		json.Unmarshal([]byte(raw), &picker)
		for _, o := range picker.Options {
			vals = append(vals, o.Model)
		}
		for _, v := range vals {
			if provider.ClaudeDashed(v) != v {
				t.Fatalf("%q written with a dot:\n%s", v, readFile(path))
			}
		}
	}

	// as the page applies it, so it is the model magpie knows it set
	if err := a.Apply("model", "relay/claude-opus-4.6-us"); err != nil {
		t.Fatal(err)
	}
	if model() != "relay/claude-opus-4-6-us" || env("ANTHROPIC_DEFAULT_OPUS_MODEL") != "relay/claude-opus-4-6-us" ||
		env("ANTHROPIC_DEFAULT_SONNET_MODEL") != "relay/claude-opus-4-6-us" {
		t.Fatalf("the main model and its followers:\n%s", readFile(path))
	}
	noDots()
	if v := a.Values(); v["model"] != "relay/claude-opus-4.6-us" {
		t.Fatalf("read back: %v", v)
	}
	if d := a.Drift(); d != nil {
		t.Fatalf("drift after magpie's own write: %v", d)
	}
	// /model lists them as Claude Code takes them, another vendor's as it is
	var picker struct{ Options []struct{ Model string } }
	raw, _ := edit.GetJSON(path, "modelPicker")
	if err := json.Unmarshal([]byte(raw), &picker); err != nil {
		t.Fatalf("modelPicker: %v\n%s", err, readFile(path))
	}
	var listed []string
	for _, o := range picker.Options {
		listed = append(listed, o.Model)
	}
	for _, want := range []string{"relay/claude-opus-4-6-us", "relay/claude-sonnet-4-5", "relay/claude-haiku-4-5", "relay/gpt-5.1", "relay/glm-4.6"} {
		if !slices.Contains(listed, want) {
			t.Fatalf("picker has no %s: %v", want, listed)
		}
	}

	// a tier on a model of its own, at an effort of its own
	set("haiku", "relay/claude-haiku-4.5")
	if env("ANTHROPIC_DEFAULT_HAIKU_MODEL") != "relay/claude-haiku-4-5" || env("ANTHROPIC_SMALL_FAST_MODEL") != "relay/claude-haiku-4-5" ||
		a.Field("haiku").Get() != "relay/claude-haiku-4.5" {
		t.Fatalf("haiku: %v\n%s", a.Values(), readFile(path))
	}
	set("sonnet", "relay/claude-sonnet-4.5")
	set("sonnet_effort", "high")
	if env("ANTHROPIC_DEFAULT_SONNET_MODEL") != "relay/claude-sonnet-4-5:high" || a.Field("sonnet").Get() != "relay/claude-sonnet-4.5" ||
		a.Field("sonnet_effort").Get() != "high" {
		t.Fatalf("sonnet at high: %v\n%s", a.Values(), readFile(path))
	}
	set("subagent", "relay/claude-sonnet-4.5")
	if env("CLAUDE_CODE_SUBAGENT_MODEL") != "relay/claude-sonnet-4-5" || a.Field("subagent").Get() != "relay/claude-sonnet-4.5" {
		t.Fatalf("subagent: %v\n%s", a.Values(), readFile(path))
	}
	noDots()

	// a pick in Claude Code's /model, of the spelling it was given
	if err := edit.SetJSON(path, edit.KV{Path: "model", Value: "relay/claude-haiku-4-5"}); err != nil {
		t.Fatal(err)
	}
	if v := a.Values(); v["model"] != "relay/claude-haiku-4.5" {
		t.Fatalf("the pick read back: %v", v)
	}
	if err := a.Follow(); err != nil {
		t.Fatal(err)
	}
	if model() != "relay/claude-haiku-4-5" || env("ANTHROPIC_DEFAULT_OPUS_MODEL") != "relay/claude-haiku-4-5" {
		t.Fatalf("followers go with the pick:\n%s", readFile(path))
	}
	if got := claudeStandInAt(here(""), path, "some-unknown-model"); got != "relay/claude-haiku-4.5" {
		t.Fatalf("stand-in: %q", got)
	}
	noDots()

	// another vendor's dotted version is as it is
	set("model", "relay/gpt-5.1")
	if model() != "relay/gpt-5.1" || a.Values()["model"] != "relay/gpt-5.1" {
		t.Fatalf("gpt-5.1:\n%s", readFile(path))
	}
	set("model", "relay/claude-opus-4.6-us")

	// what an older magpie wrote dotted goes in dashed at the next look
	if err := edit.SetJSON(path, edit.KV{Path: "model", Value: "relay/claude-opus-4.6-us"},
		edit.KV{Path: "env.ANTHROPIC_DEFAULT_OPUS_MODEL", Value: "relay/claude-opus-4.6-us"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if model() != "relay/claude-opus-4-6-us" || env("ANTHROPIC_DEFAULT_OPUS_MODEL") != "relay/claude-opus-4-6-us" {
		t.Fatalf("an older magpie's dotted ids:\n%s", readFile(path))
	}
	noDots()

	// the user's own model comes back as they wrote it
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if env("ANTHROPIC_DEFAULT_HAIKU_MODEL") != "claude-haiku-4.5" {
		t.Fatalf("the user's own haiku respelled:\n%s", readFile(path))
	}
}

// A provider serving a model by the dashed name too: each is the one it
// names, and neither is respelled.
func TestClaudeDottedServedDashedToo(t *testing.T) {
	home, path := claudeDottedHome(t, provider.Provider{ID: "both", Name: "Both", Anthropic: "https://example.test", Key: "k",
		Models: []string{"claude-opus-4.6", "claude-opus-4-6"}}, `{}`)
	a := claude(home)
	model := func() string { return writtenModel(path) }
	for _, m := range []string{"both/claude-opus-4.6", "both/claude-opus-4-6"} {
		if err := a.Field("model").Set(m); err != nil {
			t.Fatal(err)
		}
		if model() != m || a.Values()["model"] != m {
			t.Fatalf("%s: wrote %q, read %q", m, model(), a.Values()["model"])
		}
	}
}
