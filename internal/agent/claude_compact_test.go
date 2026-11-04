package agent

import (
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// TestClaudeCompactWindow: on magpie Claude Code compacts at the working
// window (CLAUDE_CODE_AUTO_COMPACT_WINDOW), another vendor's [1m] model too, so a 1M model's
// conversation isn't run to 1M with every turn sending all of it (X: Chen);
// settings.FullContext takes it away, a Claude model runs to its own, a value the user set is theirs, and
// Claude Code as installed has none.
func TestClaudeCompactWindow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "v", Name: "V", Chat: "https://example.test/v1", Key: "k",
		Models: []string{"big", "claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("v", "https://example.test/v1", []catalog.Model{{ID: "big", Context: 1000000}, {ID: "claude-opus-5-5", Context: 1000000}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{}`)
	a := claude(home)
	compact := func() string { v, _ := edit.GetJSON(path, "env."+claudeCompactEnv); return v }
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	must(a.Field("model").Set("v/big[1m]"))
	if compact() != "272000" {
		t.Fatalf("on magpie: %q", compact())
	}
	must(settings.Save(settings.Settings{FullContext: true}))
	must(a.Sync())
	if compact() != "" {
		t.Fatalf("full context: %q", compact())
	}
	must(settings.Save(settings.Settings{}))
	must(a.Sync())
	if compact() != "272000" {
		t.Fatalf("back to the working window: %q", compact())
	}
	// a Claude model runs to its whole window, as Anthropic runs a [1m]
	// one (Max on Discord: a [1m] model stopped at 272K), and another
	// vendor's is capped again after it
	must(a.Field("model").Set("v/claude-opus-5-5[1m]"))
	if compact() != "" {
		t.Fatalf("a Claude model: %q", compact())
	}
	must(a.Sync())
	if compact() != "" {
		t.Fatalf("a Claude model, synced: %q", compact())
	}
	must(a.Field("model").Set("v/big[1m]"))
	if compact() != "272000" {
		t.Fatalf("after a Claude model: %q", compact())
	}
	must(a.Field("model").Set(""))
	if compact() != "" {
		t.Fatalf("Claude Code as installed: %q", compact())
	}
	// the user's own is left as it is
	must(edit.SetJSON(path, edit.KV{Path: "env." + claudeCompactEnv, Value: "500000"}))
	must(a.Field("model").Set("v/big[1m]"))
	must(a.Sync())
	if compact() != "500000" {
		t.Fatalf("the user's own: %q", compact())
	}
	must(a.Field("model").Set(""))
	if compact() != "500000" {
		t.Fatalf("reset took the user's own: %q", compact())
	}
}

// Claude Code compacts where the user said (#876, hisiling): at the size
// typed for every model, at the main model's provider's own or the
// model's own before that, a Claude model's included, and Full window
// leaves out only the one for every model.
func TestClaudeCompactAt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "v", Name: "V", Chat: "https://example.test/v1", Key: "k",
		Models: []string{"big", "flash", "claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("v", "https://example.test/v1", []catalog.Model{{ID: "big", Context: 1000000}, {ID: "flash", Context: 1000000}, {ID: "claude-opus-5-5", Context: 1000000}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{}`)
	a := claude(home)
	compact := func() string { v, _ := edit.GetJSON(path, "env."+claudeCompactEnv); return v }
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	expect := func(what, want string) {
		t.Helper()
		if got := compact(); got != want {
			t.Fatalf("%s: %q; want %q", what, got, want)
		}
	}

	must(provider.SetCompactAt(400000))
	must(a.Field("model").Set("v/big"))
	expect("every model at 400K", "400000")
	// the Usage page's card reads that as Long conversations, one value
	// with the agent's settings sheet (7eleven); a provider's own is not it
	tuned := func() TuneSetting {
		t.Helper()
		s, err := TuneRead(home, "claude", TuneCompact, "claude-opus-5-5")
		must(err)
		return s
	}
	if s := tuned(); s.Value != "400000" || s.Source != TuneLong || s.Locked != "" {
		t.Fatalf("tune reads magpie's 400K as %+v", s)
	}
	must(provider.SetModelCompacts("v", map[string]int{"*": 600000, "flash": 200000}))
	must(a.Sync())
	expect("the provider's", "600000")
	if s := tuned(); s.Locked != claudeCompactEnv || s.Value != "600000" {
		t.Fatalf("tune reads the provider's 600K as %+v", s)
	}
	must(a.Field("model").Set("v/flash[1m]"))
	expect("the model's own", "200000")
	must(a.Field("model").Set("v/claude-opus-5-5[1m]"))
	expect("a Claude model, its provider's", "600000")
	must(provider.SetModelCompacts("v", map[string]int{"flash": 200000}))
	must(a.Sync())
	expect("a Claude model, none set", "")
	must(provider.SetFullContext(true))
	must(a.Field("model").Set("v/flash"))
	expect("full window, the model's own", "200000")
	must(a.Field("model").Set("v/big"))
	expect("full window", "")
	// Claude Code's own Long conversations (molianyin4227) over every
	// agent's Full window; Codex's own is not Claude Code's
	must(provider.SetAgentCompact("codex", &settings.AgentCompact{At: 700000}))
	must(a.Sync())
	expect("codex's own", "")
	must(provider.SetAgentCompact("claude", &settings.AgentCompact{At: 300000}))
	must(a.Sync())
	expect("claude's own 300K", "300000")
	if s := tuned(); s.Value != "300000" || s.Source != TuneLong {
		t.Fatalf("tune reads claude's own 300K as %+v", s)
	}
	must(a.Field("model").Set("v/flash"))
	expect("claude's own, the model's first", "200000")
	must(a.Field("model").Set("v/big"))
	must(provider.SetAgentCompact("claude", nil))
	must(a.Sync())
	expect("back to every agent's full window", "")
}

// An auto-compact window the user set in Claude Code (autoCompactWindow,
// or modelSettings.<model>.autoCompactWindow as /autocompact saves it) is
// theirs: magpie's default doesn't write the env that would take
// precedence over it; a size typed in magpie, or on the model or its
// provider, still does (#876, hisiling).
func TestClaudeCompactYieldsToOwnSetting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "v", Name: "V", Chat: "https://example.test/v1", Key: "k",
		Models: []string{"big", "flash"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("v", "https://example.test/v1", []catalog.Model{{ID: "big", Context: 1000000}, {ID: "flash", Context: 1000000}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{"autoCompactWindow": 900000}`)
	a := claude(home)
	compact := func() string { v, _ := edit.GetJSON(path, "env."+claudeCompactEnv); return v }
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	expect := func(what, want string) {
		t.Helper()
		if got := compact(); got != want {
			t.Fatalf("%s: %q; want %q", what, got, want)
		}
	}

	must(a.Field("model").Set("v/big[1m]"))
	expect("autoCompactWindow", "")
	if v, _ := edit.GetJSON(path, "autoCompactWindow"); v != "900000" {
		t.Fatalf("autoCompactWindow changed: %q", v)
	}
	must(edit.DelJSON(path, "autoCompactWindow"))
	must(a.Sync())
	expect("none set", "272000")
	// one for this model in modelSettings, not another's
	must(edit.SetJSON(path, edit.KV{Path: "modelSettings", Value: map[string]any{"v/flash": map[string]any{"autoCompactWindow": 800000}}}))
	must(a.Sync())
	expect("another model's", "272000")
	must(a.Field("model").Set("v/flash[1m]"))
	expect("this model's", "")
	// a size typed in magpie, or the model's own there, comes first
	must(provider.SetModelCompacts("v", map[string]int{"flash": 200000}))
	must(a.Sync())
	expect("the model's own in magpie", "200000")
	must(provider.SetModelCompacts("v", nil))
	must(provider.SetCompactAt(400000))
	must(a.Sync())
	expect("typed in magpie", "400000")
}

// A routing group made only of Claude models runs as a Claude model does,
// to its own window: Claude Code isn't handed magpie's default 272000 for
// it (#1741, yanjieee: a group of Claude [1m] models was, its id's last
// part not starting with "claude"), a group in it of Claude models too.
// A group with another vendor's model in it keeps the default, a
// threshold the user set on a member's provider still comes first, and
// the user's own env and autoCompactWindow are left as they are.
func TestClaudeGroupCompactWindow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(provider.Save(provider.Provider{ID: "v", Name: "V", Chat: "https://example.test/v1", Key: "k",
		Models: []string{"big", "claude-opus-5-5"}}))
	must(catalog.SaveLive("v", "https://example.test/v1", []catalog.Model{{ID: "big", Context: 1000000}, {ID: "claude-opus-5-5", Context: 1000000}}))
	must(provider.Save(provider.Provider{ID: "w", Name: "W", Chat: "https://example.test/w/v1", Key: "k",
		Models: []string{"claude-sonnet-5"}}))
	must(catalog.SaveLive("w", "https://example.test/w/v1", []catalog.Model{{ID: "claude-sonnet-5", Context: 1000000}}))
	must(provider.SaveGroup(provider.Group{ID: "auto-opus", Name: "Auto", Members: []string{"v/claude-opus-5-5", "w/claude-sonnet-5"}}))
	must(provider.SaveGroup(provider.Group{ID: "outer", Name: "Outer", Members: []string{"group/auto-opus"}}))
	must(provider.SaveGroup(provider.Group{ID: "mix", Name: "Mix", Members: []string{"v/claude-opus-5-5", "v/big"}}))

	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{}`)
	a := claude(home)
	compact := func() string { v, _ := edit.GetJSON(path, "env."+claudeCompactEnv); return v }
	expect := func(what, want string) {
		t.Helper()
		if got := compact(); got != want {
			t.Fatalf("%s: %q; want %q", what, got, want)
		}
	}

	must(a.Field("model").Set("group/mix[1m]"))
	expect("a group with another vendor's model", "272000")
	must(a.Field("model").Set("group/auto-opus[1m]"))
	expect("a group of Claude models", "")
	must(a.Sync())
	expect("a group of Claude models, synced", "")
	must(a.Field("model").Set("group/outer"))
	expect("a group of a group of Claude models", "")
	must(a.Field("model").Set("group/mix"))
	expect("back on the mixed group", "272000")

	// a threshold set on a member's provider comes first, as it does for
	// a Claude model
	must(provider.SetModelCompacts("w", map[string]int{"*": 600000}))
	must(a.Field("model").Set("group/auto-opus[1m]"))
	expect("a member's provider's", "600000")
	must(provider.SetModelCompacts("w", nil))
	must(a.Sync())
	expect("none set again", "")

	// the user's own env and autoCompactWindow are theirs
	must(edit.SetJSON(path, edit.KV{Path: "env." + claudeCompactEnv, Value: "500000"}))
	must(edit.SetJSON(path, edit.KV{Path: "autoCompactWindow", Value: 900000}))
	must(a.Field("model").Set("group/mix"))
	must(a.Field("model").Set("group/auto-opus[1m]"))
	must(a.Sync())
	expect("the user's own", "500000")
	if v, _ := edit.GetJSON(path, "autoCompactWindow"); v != "900000" {
		t.Fatalf("autoCompactWindow changed: %q", v)
	}
}

// A Claude model runs to its whole window whatever name its provider gives
// it: Amazon Bedrock names Claude Opus us.anthropic.claude-…, which
// magpie's [1m] Claude Opus there was not taken for, so Claude Code was
// held to 272K on a 1M Claude model (划船不靠桨 on Discord), while a group
// of the same model ran to 1M (provider.ClaudeGroup). Another vendor's 1M
// model on the same provider is still compacted at the working window.
func TestClaudeCompactWindowBedrockClaude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	const opus, nova = "us.anthropic.claude-opus-5-5-v1", "us.amazon.nova-premier-v1"
	if err := provider.Save(provider.Provider{ID: "br", Name: "Bedrock", Chat: "https://example.test/v1", Key: "k",
		Models: []string{opus, nova}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("br", "https://example.test/v1", []catalog.Model{{ID: opus, Context: 1000000}, {ID: nova, Context: 1000000}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{}`)
	a := claude(home)
	compact := func() string { v, _ := edit.GetJSON(path, "env."+claudeCompactEnv); return v }
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(a.Field("model").Set("br/" + opus + "[1m]"))
	if compact() != "" {
		t.Fatalf("a 1M Claude model on Bedrock is compacted at %q", compact())
	}
	must(a.Sync())
	if compact() != "" {
		t.Fatalf("a 1M Claude model on Bedrock, synced: %q", compact())
	}
	if top, _ := edit.GetJSON(path, "autoCompactWindow"); top != "" {
		t.Fatalf("a 1M Claude model on Bedrock got autoCompactWindow %q", top)
	}
	must(a.Field("model").Set("br/" + nova + "[1m]"))
	if compact() != "272000" {
		t.Fatalf("another vendor's 1M model on Bedrock: %q", compact())
	}
}
