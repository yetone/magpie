package provider

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fastHome is a machine of its own with an OpenAI key, an Anthropic key, a
// relay and a ChatGPT account.
func fastHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, p := range []Provider{
		{ID: "oa", Name: "OpenAI", Key: "k", Responses: "https://api.openai.com/v1", Models: []string{"gpt-6.1-sol", "text-embedding-3"}},
		{ID: "an", Name: "Anthropic", Key: "k", Anthropic: "https://api.anthropic.com", Models: []string{"claude-opus-5-5", "claude-opus-4-8-20260501", "claude-sonnet-5", "claude-fable-5-2", "claude-fable-5-1"}},
		{ID: "rl", Name: "Relay", Key: "k", Chat: "https://relay.example/v1", Anthropic: "https://relay.example", Models: []string{"gpt-6.1-sol", "claude-opus-5-5", "odd:fast"}},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCanFast(t *testing.T) {
	fastHome(t)
	codex := Provider{ID: "codex", Account: &Account{Agent: "codex"}}
	claude := Provider{ID: "claude", Account: &Account{Agent: "claude"}, Anthropic: "https://api.anthropic.com"}
	oa, _ := Find("oa")
	an, _ := Find("an")
	rl, _ := Find("rl")
	for _, c := range []struct {
		p     Provider
		model string
		want  bool
	}{
		{codex, "gpt-6.1-sol", true},
		{claude, "claude-opus-5-5", false}, // Claude Code's own
		{*oa, "gpt-6.1-sol", true},
		{*oa, "o4-mini", true},
		{*oa, "text-embedding-3", false},
		{*an, "claude-opus-5-5", true},
		{*an, "claude-opus-4-8-20260501", true},
		{*an, "claude-sonnet-5", false},
		{*an, "claude-fable-5-2", true}, // fast mode from its launch
		{*an, "claude-fable-5-1", false},
		{*rl, "claude-opus-5-5", false}, // Claude's speed isn't asked of a relay
	} {
		if got := CanFast(c.p, c.model); got != c.want {
			t.Errorf("%s/%s: %v, want %v", c.p.ID, c.model, got, c.want)
		}
		if OwnFast(c.p, c.model) {
			t.Errorf("%s/%s: the vendor's own fast mode taken for the user's provider's", c.p.ID, c.model)
		}
	}
}

// A GPT model on a provider the user added by its address may be switched
// fast (#1615, iomect: 目前只有官key可以开启快速模式): it is asked for
// priority on its OpenAI API. Not on a preset's, a cloud's, an account's,
// another model's, or one served only on Anthropic's API.
func TestOwnFast(t *testing.T) {
	fastHome(t)
	rl, _ := Find("rl")
	preset := *rl
	preset.ID, preset.Preset = "or", "openrouter"
	resp := Provider{ID: "sub", Responses: "https://sub2api.example/v1", Models: []string{"gpt-6.1-sol"}}
	for _, c := range []struct {
		p     Provider
		model string
		want  bool
	}{
		{*rl, "gpt-6.1-sol", true},
		{resp, "gpt-6.1-sol", true},
		{resp, "openai/GPT-6.1-sol", true},
		{resp, "o4-mini", true},
		{Provider{ID: "oc", Chat: "https://api.openai.com/v1"}, "gpt-6.1-sol", true}, // OpenAI's Chat API added by its address
		{*rl, "odd:fast", false},
		{*rl, "glm-5", false},
		{*rl, "claude-opus-5-5", false},
		{preset, "gpt-6.1-sol", false},
		{Provider{ID: "ra", Anthropic: "https://relay.example", Models: []string{"gpt-6.1-sol"}}, "gpt-6.1-sol", false},
		{Provider{ID: "br", Chat: "https://bedrock-runtime.us-east-1.amazonaws.com/v1"}, "gpt-6.1-sol", false},
		{Provider{ID: "codex", Account: &Account{Agent: "codex"}, Chat: "https://relay.example/v1"}, "gpt-6.1-sol", false},
	} {
		if got := OwnFast(c.p, c.model); got != c.want {
			t.Errorf("OwnFast %s/%s: %v, want %v", c.p.ID, c.model, got, c.want)
		}
		if c.want && !CanFast(c.p, c.model) {
			t.Errorf("CanFast %s/%s: false", c.p.ID, c.model)
		}
	}
}

// A member typed ":fast" is saved as the member, listed in Fast; Fast
// keeps to the group's own members, each once, and follows a renamed
// provider. A model with no fast mode, or a group, is refused it.
func TestSaveGroupFast(t *testing.T) {
	fastHome(t)
	if id, fast := MemberFast("oa/gpt-6.1-sol:high:fast"); id != "oa/gpt-6.1-sol:high" || !fast {
		t.Fatalf("split %q %v", id, fast)
	}
	if err := SaveGroup(Group{ID: "f", Members: []string{"oa/gpt-6.1-sol:high:fast", "an/claude-opus-5-5", "rl/odd:fast"},
		Fast: []string{"an/claude-opus-5-5", "an/claude-opus-5-5", "an/gone"}}); err != nil {
		t.Fatal(err)
	}
	g, ms, _ := FindGroup("group/f")
	if !slices.Equal(g.Members, []string{"oa/gpt-6.1-sol:high", "an/claude-opus-5-5", "rl/odd:fast"}) {
		t.Fatalf("members %v", g.Members)
	}
	if f := slices.Sorted(slices.Values(g.Fast)); !slices.Equal(f, []string{"an/claude-opus-5-5", "oa/gpt-6.1-sol:high"}) {
		t.Fatalf("fast %v", g.Fast)
	}
	if len(ms) != 3 || !ms[0].Fast || ms[0].Effort != "high" || !ms[1].Fast || ms[2].Fast {
		t.Fatalf("resolved %+v", ms)
	}
	g.RenameMember("oa/gpt-6.1-sol:high", "oa/gpt-6.1-sol:low")
	if !g.IsFast("oa/gpt-6.1-sol:low") || g.IsFast("oa/gpt-6.1-sol:high") {
		t.Fatalf("renamed member's fast %v", g.Fast)
	}
	g.SetMemberFast("an/claude-opus-5-5", false)
	g.SetMemberFast("an/not-in-it", true)
	if !slices.Equal(g.Fast, []string{"oa/gpt-6.1-sol:low"}) {
		t.Fatalf("set %v", g.Fast)
	}
	if err := Rename("oa", "openai"); err != nil {
		t.Fatal(err)
	}
	if g, _, _ = FindGroup("group/f"); !slices.Contains(g.Fast, "openai/gpt-6.1-sol:high") {
		t.Fatalf("after the rename %v", g.Fast)
	}
	for _, m := range []string{"rl/claude-opus-5-5:fast", "an/claude-sonnet-5:fast", "group/f:fast"} {
		err := SaveGroup(Group{ID: "g", Members: []string{m, "an/claude-opus-5-5"}})
		if err == nil || !(strings.Contains(err.Error(), "no fast mode") || strings.Contains(err.Error(), "takes no :fast")) {
			t.Errorf("%s: %v", m, err)
		}
	}
}

// A GPT model on a provider the user added by its address can be sent
// Ultrafast where OpenAI offers it on the model (KoiLx0 on X): Codex's own
// entry for it lists the tier, as Codex itself is offered it there, or
// OpenAI's API serves it so (OoopsHh on X: in Docker there is no Codex CLI
// and no entry). Not on a model Codex's entry gives Fast alone, nor one it
// has no entry for, nor on another vendor's model.
func TestCanUltraOwnProvider(t *testing.T) {
	fastHome(t)
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	rl, _ := Find("rl")
	oa, _ := Find("oa")
	if UltraOn(*rl, "gpt-6.1-mini", nil) || UltraOn(*rl, "gpt-6-sol", nil) {
		t.Fatal("Ultrafast with no list saying so")
	}
	// with no list at all: the models OpenAI's API serves at Ultrafast
	if !UltraOn(*rl, "gpt-6.1-sol", nil) || !UltraOn(*oa, "gpt-6.1-sol", nil) {
		t.Error("gpt-6.1-sol: no Ultrafast where OpenAI's API serves it")
	}
	for m, want := range map[string]bool{
		"gpt-6-astra": true, "gpt-6.2-astra": true, "openai/gpt-6.2-sol": true, "GPT-6-Astra-2026-09-14": true, "gpt-6.1-sol-20261001": true,
		"gpt-6-sol": false, "gpt-6.1-terra": false, "gpt-6.2-astra-mini": false, "gpt-6.2": false, "claude-opus-5-5": false,
	} {
		if OpenAIUltra(m) != want {
			t.Errorf("OpenAIUltra(%q) = %v", m, !want)
		}
	}
	cache := `{"etag":"e1","models":[
		{"slug":"gpt-6.1-sol","description":"x","service_tiers":[{"id":"priority","name":"Fast"},{"id":"ultrafast","name":"Ultra Fast"}]},
		{"slug":"gpt-6.1-mini","description":"x","service_tiers":[{"id":"priority","name":"Fast"}]}]}`
	if err := os.WriteFile(filepath.Join(codexHome, "models_cache.json"), []byte(cache), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		p     Provider
		model string
		want  bool
	}{
		{*rl, "gpt-6.1-sol", true},
		{*rl, "openai/GPT-6.1-sol-2026-05-01", true},
		{*rl, "gpt-6.1-mini", false},
		{*rl, "gpt-7", false},
		{*rl, "claude-opus-5-5", false},
		// OpenAI's own API, on the models its Ultrafast mode names
		{*oa, "gpt-6.1-sol", true},
		{*oa, "gpt-6-sol", false},
		{*oa, "gpt-6.1-mini", false},
		{*oa, "claude-opus-5-5", false},
	} {
		if got := UltraOn(c.p, c.model, nil); got != c.want {
			t.Errorf("%s/%s: Ultrafast %v, want %v", c.p.ID, c.model, got, c.want)
		}
	}
	// one of Codex's accounts listing it says so too
	if !UltraOn(*rl, "gpt-7", map[string]bool{"gpt-7": true}) {
		t.Error("an account's Ultrafast not taken")
	}
}
