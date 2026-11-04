package provider

import (
	"maps"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// A provider's editor sets where Codex and Claude Code compact on its
// models (#876, hisiling): "*" for all of them and model=size for one,
// the model's own first; those it leaves out come off, another
// provider's stay, a model it doesn't serve is refused, and a save that
// changes nothing tells the agents nothing. A group takes the smallest
// its models have.
func TestModelCompactsSetFromTheEditor(t *testing.T) {
	limitsHome(t)
	for _, id := range []string{"deepseek", "other"} {
		if err := Save(Provider{ID: id, Name: id, Chat: "https://relay.example/v1", Key: "k", Models: []string{"deepseek-v4-flash", "deepseek-v4-pro"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := SetModelCompacts("other", map[string]int{"deepseek-v4-pro": 300000}); err != nil {
		t.Fatal(err)
	}
	told := 0
	catalog.Changed = func() { told++ }
	t.Cleanup(func() { catalog.Changed = nil })

	if err := SetModelCompacts("deepseek", map[string]int{"*": 500000, "deepseek-v4-flash": 272000}); err != nil {
		t.Fatal(err)
	}
	if told != 1 {
		t.Errorf("told the agents %d times; want once", told)
	}
	want := map[string]int{"deepseek/*": 500000, "deepseek/deepseek-v4-flash": 272000, "other/deepseek-v4-pro": 300000}
	if got := settings.Load().ModelCompacts; !maps.Equal(got, want) {
		t.Errorf("compacts %v; want %v", got, want)
	}
	if got := CompactsOf("deepseek"); !maps.Equal(got, map[string]int{"*": 500000, "deepseek-v4-flash": 272000}) {
		t.Errorf("CompactsOf: %v", got)
	}
	for id, n := range map[string]int{
		"deepseek/deepseek-v4-flash":      272000,
		"magpie/deepseek/deepseek-v4-pro": 500000,
		"deepseek/deepseek-v4-pro[1m]":    500000,
		"other/deepseek-v4-flash":         0,
		"other/deepseek-v4-pro":           300000,
		"nobody/deepseek-v4-pro":          0,
	} {
		if got := CompactSet(id); got != n {
			t.Errorf("CompactSet(%s) = %d; want %d", id, got, n)
		}
	}
	group := func(string) (Group, []Member, bool) {
		dp, _ := Find("deepseek")
		ot, _ := Find("other")
		return Group{}, []Member{
			{Path: []string{"deepseek/deepseek-v4-pro"}, Provider: *dp, Model: "deepseek-v4-pro"},
			{Path: []string{"other/deepseek-v4-pro"}, Provider: *ot, Model: "wire-name"},
			{Path: []string{"other/deepseek-v4-flash"}, Provider: *ot, Model: "deepseek-v4-flash"},
		}, true
	}
	if got := compactSet(settings.Load(), GroupPrefix+"mix", group); got != 300000 {
		t.Errorf("a group: %d; want its smallest, 300000", got)
	}

	told = 0
	if err := SetModelCompacts("deepseek", map[string]int{"*": 500000, "deepseek-v4-flash": 272000}); err != nil || told != 0 {
		t.Errorf("the same again: %v, told %d", err, told)
	}
	if err := SetModelCompacts("deepseek", map[string]int{"typo": 5}); err == nil {
		t.Error("a model deepseek doesn't serve was accepted")
	}
	if got := settings.Load().ModelCompacts; !maps.Equal(got, want) {
		t.Errorf("a refused save changed them: %v", got)
	}
	if err := SetModelCompacts("deepseek", map[string]int{}); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load().ModelCompacts; !maps.Equal(got, map[string]int{"other/deepseek-v4-pro": 300000}) {
		t.Errorf("emptied: %v", got)
	}
}

// The threshold for every model can be any size, not 272K alone (#876):
// typing one turns Full window off, 272K or nothing is the default again.
func TestSetCompactAt(t *testing.T) {
	limitsHome(t)
	if err := settings.Save(settings.Settings{FullContext: true}); err != nil {
		t.Fatal(err)
	}
	if err := SetCompactAt(500000); err != nil {
		t.Fatal(err)
	}
	if s := settings.Load(); s.FullContext || s.CompactAt != 500000 || s.Compact() != 500000 || s.Working(1000000) != 500000 || s.Working(200000) != 200000 {
		t.Errorf("at 500K: %+v", s)
	}
	if err := SetCompactAt(settings.WorkingWindow); err != nil {
		t.Fatal(err)
	}
	if s := settings.Load(); s.CompactAt != 0 || s.Compact() != settings.WorkingWindow {
		t.Errorf("at 272K: %+v", s)
	}
	if err := SetCompactAt(-1); err == nil {
		t.Error("a negative size was accepted")
	}
	if s := (settings.Settings{CompactAt: 500000, FullContext: true}); s.Compact() != 0 || s.Working(1000000) != 1000000 {
		t.Errorf("full window: %d", s.Compact())
	}
}

// The own-models switch reaches an agent's own config too (Claude Code's
// modelPicker says replaceBuiltInOptions, #4548), so flipping it tells the
// agents, as a hidden model's change does; a flip to where it already is
// tells them nothing.
func TestOwnModelsSwitchTellsTheAgents(t *testing.T) {
	limitsHome(t)
	told := 0
	catalog.Changed = func() { told++ }
	t.Cleanup(func() { catalog.Changed = nil })
	if err := SetOwnModelsHidden("claude", true); err != nil {
		t.Fatal(err)
	}
	if told != 1 || !OwnModelsHidden("claude") {
		t.Fatalf("hidden: told %d times, hidden=%v; want once, true", told, OwnModelsHidden("claude"))
	}
	if err := SetOwnModelsHidden("claude", true); err != nil {
		t.Fatal(err)
	}
	if told != 1 {
		t.Fatalf("told %d times for a flip that changed nothing; want once in all", told)
	}
	if err := SetOwnModelsHidden("claude", false); err != nil {
		t.Fatal(err)
	}
	if told != 2 || OwnModelsHidden("claude") {
		t.Fatalf("shown: told %d times, hidden=%v; want twice, false", told, OwnModelsHidden("claude"))
	}
}

// An agent sets its own Long conversations (molianyin4227 on Discord: one
// size for every agent fits none when their models differ): Codex, Claude
// Code and Grok Build take one over the one every agent has, another
// agent has no window magpie writes and is refused, nil goes back to
// every agent's, and the agent's 272K is kept as magpie's default.
func TestSetAgentCompact(t *testing.T) {
	limitsHome(t)
	told := 0
	catalog.Changed = func() { told++ }
	t.Cleanup(func() { catalog.Changed = nil })
	if err := SetCompactAt(400000); err != nil {
		t.Fatal(err)
	}
	told = 0
	if err := SetAgentCompact("codex", &settings.AgentCompact{At: 600000}); err != nil {
		t.Fatal(err)
	}
	if err := SetAgentCompact("claude", &settings.AgentCompact{Full: true}); err != nil {
		t.Fatal(err)
	}
	s := settings.Load()
	if c, cl, g := s.ForAgent("codex"), s.ForAgent("claude"), s.ForAgent("grok"); c.Compact() != 600000 || cl.Compact() != 0 || g.Compact() != 400000 {
		t.Fatalf("codex %d, claude %d, grok %d; want 600000, 0 (full), 400000", c.Compact(), cl.Compact(), g.Compact())
	}
	if s.CompactAt != 400000 || s.FullContext {
		t.Fatalf("every agent's changed: %d full=%v", s.CompactAt, s.FullContext)
	}
	if told != 2 {
		t.Fatalf("told the agents %d times; want 2", told)
	}
	if err := SetAgentCompact("codex", &settings.AgentCompact{At: 600000}); err != nil || told != 2 {
		t.Fatalf("the same value again: %v, told %d", err, told)
	}
	if err := SetAgentCompact("codex", &settings.AgentCompact{At: settings.WorkingWindow}); err != nil {
		t.Fatal(err)
	}
	if c, ok := settings.Load().AgentCompacts["codex"]; !ok || c.At != 0 || settings.Load().ForAgent("codex").Compact() != settings.WorkingWindow {
		t.Fatalf("codex at 272K: %+v %v", c, ok)
	}
	for _, bad := range []string{"opencode", "", "codex@wsl:Ubuntu"} {
		if err := SetAgentCompact(bad, &settings.AgentCompact{At: 300000}); err == nil {
			t.Errorf("%q took a window", bad)
		}
	}
	if err := SetAgentCompact("grok", &settings.AgentCompact{At: -1}); err == nil {
		t.Error("a negative size was accepted")
	}
	if err := SetAgentCompact("claude", nil); err != nil {
		t.Fatal(err)
	}
	s = settings.Load()
	if _, ok := s.AgentCompacts["claude"]; ok || s.ForAgent("claude").Compact() != 400000 {
		t.Fatalf("claude back to every agent's: %+v", s.AgentCompacts)
	}
	if len(s.AgentCompacts) != 1 {
		t.Fatalf("others left: %+v", s.AgentCompacts)
	}
}
