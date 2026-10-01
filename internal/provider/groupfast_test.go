package provider

import (
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
		{ID: "an", Name: "Anthropic", Key: "k", Anthropic: "https://api.anthropic.com", Models: []string{"claude-opus-5-5", "claude-opus-4-8-20260501", "claude-sonnet-5"}},
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
		{Provider{ID: "oc", Chat: "https://api.openai.com/v1"}, "gpt-6.1-sol", false}, // its Chat API is told no tier
		{*an, "claude-opus-5-5", true},
		{*an, "claude-opus-4-8-20260501", true},
		{*an, "claude-sonnet-5", false},
		{*rl, "gpt-6.1-sol", false},
		{*rl, "claude-opus-5-5", false},
	} {
		if got := CanFast(c.p, c.model); got != c.want {
			t.Errorf("%s/%s: %v, want %v", c.p.ID, c.model, got, c.want)
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
	for _, m := range []string{"rl/gpt-6.1-sol:fast", "an/claude-sonnet-5:fast", "group/f:fast"} {
		err := SaveGroup(Group{ID: "g", Members: []string{m, "an/claude-opus-5-5"}})
		if err == nil || !(strings.Contains(err.Error(), "no fast mode") || strings.Contains(err.Error(), "takes no :fast")) {
			t.Errorf("%s: %v", m, err)
		}
	}
}
