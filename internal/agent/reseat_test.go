package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// reseatHome is a sandbox HOME with two providers, one of them "cop" in the
// place of Copilot, and Claude Code and Hermes Agent there.
func reseatHome(t *testing.T) string {
	t.Helper()
	home := effortHome(t)
	t.Setenv("PATH", t.TempDir()) // no agent's binary is found, or run
	for _, p := range []provider.Provider{
		{ID: "cop", Name: "Cop", Chat: "https://cop.example/v1", Key: "k", Models: []string{"gpt-5", "claude-sonnet-4.5", "only-here"}},
		{ID: "ds", Name: "DS", Chat: "https://ds.example/v1", Key: "k", Models: []string{"gpt-5", "claude-sonnet-4-5-20250929"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.MkdirAll(filepath.Join(home, ".hermes"), 0o755)
	return home
}

func mustFind(t *testing.T, id string) *Agent {
	t.Helper()
	a, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func mustApply(t *testing.T, a *Agent, key, v string) {
	t.Helper()
	if err := a.Apply(key, v); err != nil {
		t.Fatalf("%s %s=%s: %v", a.ID, key, v, err)
	}
	if got := a.Field(key).Get(); got != v {
		t.Fatalf("%s %s: %q, want %q", a.ID, key, got, v)
	}
}

// #200: a provider switched off takes its models away from the agents on
// them — to the same model from a provider still on, the same model spelt
// another way, else the agent's own default — not left for the next
// request to be refused.
func TestReseatOff(t *testing.T) {
	reseatHome(t)
	c, h := mustFind(t, "claude"), mustFind(t, "hermes")
	mustApply(t, c, "model", "cop/gpt-5")
	mustApply(t, c, "opus", "cop/claude-sonnet-4.5")
	mustApply(t, c, "haiku", "cop/only-here")
	mustApply(t, c, "sonnet", "ds/gpt-5")
	mustApply(t, h, "model", "magpie/cop/only-here")

	moves, err := Reseat(func() error { return provider.SetOff("cop", true) })
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"model": "ds/gpt-5", "opus": "ds/claude-sonnet-4-5-20250929",
		"haiku": "", "sonnet": "", "fable": ""} // haiku follows the main model; sonnet was ds/gpt-5 already
	for k, w := range want {
		if got := c.Field(k).Get(); got != w {
			t.Errorf("claude %s: %q, want %q", k, got, w)
		}
	}
	if got := h.Field("model").Get(); got != "" {
		t.Errorf("hermes: %q, want its own default", got)
	}
	got := map[string]Move{}
	for _, m := range moves {
		got[m.Agent+" "+m.Field] = m
	}
	for k, w := range map[string]Move{
		"Claude Code model":  {Agent: "Claude Code", Field: "model", From: "cop/gpt-5", To: "ds/gpt-5"},
		"Claude Code opus":   {Agent: "Claude Code", Field: "opus", From: "cop/claude-sonnet-4.5", To: "ds/claude-sonnet-4-5-20250929"},
		"Claude Code haiku":  {Agent: "Claude Code", Field: "haiku", From: "cop/only-here"}, // follows the main model
		"Hermes Agent model": {Agent: "Hermes Agent", Field: "model", From: "cop/only-here"},
	} {
		if got[k] != w {
			t.Errorf("%s: %+v, want %+v", k, got[k], w)
		}
	}
	if len(moves) != 4 {
		t.Errorf("moves: %+v", moves)
	}

	// switched on again, nothing is moved back or elsewhere
	moves, err = Reseat(func() error { return provider.SetOff("cop", false) })
	if err != nil || len(moves) != 0 {
		t.Fatalf("on again: %+v %v", moves, err)
	}
}

// A provider removed moves its agents as one switched off does; one no
// agent is on moves nothing.
func TestReseatDelete(t *testing.T) {
	reseatHome(t)
	c := mustFind(t, "claude")
	mustApply(t, c, "model", "cop/only-here")
	moves, err := Reseat(func() error { return provider.Delete("ds") })
	if err != nil || len(moves) != 0 {
		t.Fatalf("an unused provider: %+v %v", moves, err)
	}
	if moves, err = Reseat(func() error { return provider.Delete("cop") }); err != nil || len(moves) != 1 {
		t.Fatalf("delete: %+v %v", moves, err)
	}
	// nothing else serves it: Claude Code as installed
	if got := c.Field("model").Get(); got != "" {
		t.Fatalf("claude: %q", got)
	}
}

func TestModelKey(t *testing.T) {
	for a, b := range map[string]string{
		"claude-sonnet-4.5":         "claude-sonnet-4-5-20250929",
		"anthropic/claude-opus-4.6": "claude-opus-4-6",
		"gpt-5.1-codex":             "GPT-5.1-codex",
		"gemini-2.5-pro-2025-06-17": "gemini-2.5-pro",
	} {
		if modelKey(a) != modelKey(b) {
			t.Errorf("%s ≠ %s", a, b)
		}
	}
	if modelKey("gpt-5") == modelKey("gpt-5-mini") {
		t.Error("gpt-5 = gpt-5-mini")
	}
}

// An agent that can't be moved (its config unwritable) doesn't fail the
// change, which is made already: the others are moved and it is a move with
// its error. An error here kept a removed provider's editor open in the
// panel, the provider still listed, and a second Remove said "no provider".
func TestReseatUnmovable(t *testing.T) {
	home := reseatHome(t)
	c, h := mustFind(t, "claude"), mustFind(t, "hermes")
	mustApply(t, c, "model", "cop/only-here")
	mustApply(t, h, "model", "magpie/cop/only-here")
	dir := filepath.Join(home, ".hermes")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	if f, err := os.Create(filepath.Join(dir, "probe")); err == nil {
		f.Close()
		t.Skip("a read-only folder is writable here (root?)")
	}
	os.Chmod(filepath.Join(dir, "config.yaml"), 0o444)

	moves, err := Reseat(func() error { return provider.Delete("cop") })
	if err != nil {
		t.Fatalf("the provider is removed, but Reseat said: %v", err)
	}
	if _, err := provider.Find("cop"); err == nil {
		t.Fatal("cop still there")
	}
	if got := c.Field("model").Get(); got != "" {
		t.Errorf("claude not moved: %q", got)
	}
	var stuck *Move
	for i, m := range moves {
		if m.Agent == "Hermes Agent" {
			stuck = &moves[i]
		}
	}
	if len(moves) != 2 || stuck == nil || stuck.Error == "" || stuck.To != "" || stuck.From != "cop/only-here" {
		t.Fatalf("moves: %+v", moves)
	}
	if s := stuck.String(); !strings.Contains(s, "not moved") {
		t.Errorf("String: %q", s)
	}
}

// Found groups turned off (蓝猫 on Discord) take every group/auto-… away:
// an agent on one is moved to its model from the first provider still
// serving it, however that one spells it, rather than to its own default.
func TestReseatFoundGroupsOff(t *testing.T) {
	reseatHome(t)
	c, h := mustFind(t, "claude"), mustFind(t, "hermes")
	mustApply(t, c, "model", "group/auto-gpt-5")
	mustApply(t, c, "opus", "group/auto-claude-sonnet-4-5")
	mustApply(t, h, "model", "magpie/group/auto-gpt-5")
	moves, err := Reseat(func() error { return provider.SetAutoGroups(false) })
	if err != nil {
		t.Fatal(err)
	}
	for k, w := range map[string]string{"model": "cop/gpt-5", "opus": "cop/claude-sonnet-4.5"} {
		if got := c.Field(k).Get(); got != w {
			t.Errorf("claude %s: %q, want %q", k, got, w)
		}
	}
	if got := h.Field("model").Get(); got != "magpie/cop/gpt-5" {
		t.Errorf("hermes: %q", got)
	}
	if len(moves) != 3 || moves[0].From != "group/auto-gpt-5" || moves[0].To != "cop/gpt-5" {
		t.Errorf("moves: %+v", moves)
	}
	// on again, nobody is moved back or elsewhere
	if moves, err = Reseat(func() error { return provider.SetAutoGroups(true) }); err != nil || len(moves) != 0 {
		t.Fatalf("on again: %+v %v", moves, err)
	}
}
