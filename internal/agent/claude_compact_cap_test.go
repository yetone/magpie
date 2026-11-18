package agent

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// TestClaudeCompactHeldToClaudeMax (ZeroX2017 on X, 0.1.4404): a
// compaction threshold of 1048576 typed for Ollama Cloud's 1M DeepSeek
// went into Claude Code's settings.json as it was, and Claude Code
// (2.1.316, Claude Desktop 2.7032's schema too) takes 100000 to 1000000
// there and reads a larger one as none: a new Claude App session ran at
// the 272K for every model. What magpie writes for Claude is held to
// 1000000, per model, top-level and in CLAUDE_CODE_AUTO_COMPACT_WINDOW;
// the threshold in magpie stays 1048576, and Codex still runs to it.
func TestClaudeCompactHeldToClaudeMax(t *testing.T) {
	home, path := desktopWindowSandbox(t)
	if err := provider.Save(provider.Provider{ID: "ollama", Name: "Ollama Cloud", Chat: "http://127.0.0.1:1/v1", Key: "k",
		Models: []string{"deepseek-v4.1-flash"}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("ollama", "http://127.0.0.1:1/v1", []catalog.Model{{ID: "deepseek-v4.1-flash", Context: 1048576}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelCompacts("ollama", map[string]int{"deepseek-v4.1-flash": 1048576}); err != nil {
		t.Fatal(err)
	}
	// Desktop is shown the DeepSeek and GPT 272K
	if err := provider.SetHiddenModels("claude-desktop", []string{"v/grok", "v/glm", "v/plain", "v/claude-opus-4-6"}); err != nil {
		t.Fatal(err)
	}
	a := claude(home)
	if err := a.Field("model").Set("v/plain"); err != nil {
		t.Fatal(err)
	}
	if err := claudeDesktop(home).Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, e := range provider.Catalog() {
		ids[e.ID] = gateway.DesktopID(e)
	}
	if ids["ollama/deepseek-v4.1-flash"] == "" {
		t.Fatalf("Desktop doesn't list the DeepSeek: %v", ids)
	}
	ds, _ := edit.GetJSON(path, budgetPath(ids["ollama/deepseek-v4.1-flash"]))
	top, _ := edit.GetJSON(path, "autoCompactWindow")
	if ds != "1000000" || top != "272000" {
		t.Fatalf("the Claude App's DeepSeek: entry %q, top-level %q; want 1000000 and 272000: %s", ds, top, readFile(path))
	}
	// a sync again changes nothing: the held value is magpie's own
	before := readFile(path)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if after := readFile(path); after != before {
		t.Fatalf("a second sync rewrote the file:\n%s\n%s", before, after)
	}
	if n := provider.CompactSet("ollama/deepseek-v4.1-flash"); n != 1048576 {
		t.Fatalf("the threshold in magpie became %d", n)
	}

	// Claude Code alone, on the DeepSeek: the variable is held too
	if err := claudeDesktop(home).Field("provider").Set(""); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("ollama/deepseek-v4.1-flash"); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(path, "env."+claudeCompactEnv); v != "1000000" {
		t.Fatalf("CLAUDE_CODE_AUTO_COMPACT_WINDOW: %q; want 1000000: %s", v, readFile(path))
	}

	// Codex is not held: a threshold at the model's window runs it to its
	// whole 1048576, which Codex is told
	if err := codex(home).Fields[0].Set("ollama/deepseek-v4.1-flash"); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Models []struct {
			Slug    string `json:"slug"`
			Window  int    `json:"context_window"`
			Compact int    `json:"auto_compact_token_limit"`
		} `json:"models"`
	}
	raw := readFile(filepath.Join(home, ".codex", "magpie-models.json"))
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	found := false
	for _, m := range got.Models {
		if m.Slug == "ollama/deepseek-v4.1-flash" {
			found = true
			if m.Window != 1048576 || m.Compact != 0 {
				t.Fatalf("Codex: context_window %d, auto_compact_token_limit %d; want 1048576 and none", m.Window, m.Compact)
			}
		}
	}
	if !found {
		t.Fatalf("the DeepSeek is not in Codex's catalog: %s", raw)
	}
}

// TestClaudePlanHeldToClaudeMax: an entry claudePlan writes is no more
// than Claude Code takes, and one held to the window a model runs at
// without it is left out.
func TestClaudePlanHeldToClaudeMax(t *testing.T) {
	top, entries := claudePlan([]claudeBudget{{id: "ds", window: 1048576, compact: 1048576}, {id: "g", window: 272000, compact: 272000}}, 272000, nil)
	if top != 1048576 || entries["ds"] != 1000000 || len(entries) != 1 {
		t.Fatalf("1048576 typed: %d %v", top, entries)
	}
	// under a top-level 1000000 the held value is the one it runs at
	if _, entries = claudePlan([]claudeBudget{{id: "ds", window: 1048576, compact: 1048576}}, 1000000, nil); len(entries) != 0 {
		t.Fatalf("under a 1000000 top-level: %v", entries)
	}
}

// TestClaudeTopCompactHeldToClaudeMax: a threshold of 1048576 for every
// model goes into the top-level autoCompactWindow as 1000000 too, and the
// Usage page's card still reads it as Long conversations.
func TestClaudeTopCompactHeldToClaudeMax(t *testing.T) {
	home, path := desktopWindowSandbox(t)
	if err := provider.SetCompactAt(1048576); err != nil {
		t.Fatal(err)
	}
	a := claude(home)
	if err := a.Field("model").Set("v/plain"); err != nil {
		t.Fatal(err)
	}
	if err := claudeDesktop(home).Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if top, _ := edit.GetJSON(path, "autoCompactWindow"); top != "1000000" {
		t.Fatalf("top-level autoCompactWindow: %q; want 1000000: %s", top, readFile(path))
	}
	s, err := TuneRead(home, "claude", TuneCompact, "claude-opus-5-5")
	if err != nil || s.Source != TuneLong || s.Value != "1048576" {
		t.Fatalf("tune reads %+v, %v", s, err)
	}
}
