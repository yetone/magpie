package gui

import (
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/sessions"
)

// linkaww on Discord (11-06): Usage › Context put Claude Code's cache at
// 1 hour, and the way back to 5 minutes was gone. The card is the one
// place that setting is changed, and its row, Undo with it, came only
// with 50 calls of the range to advise on: Today, or a quiet week, had
// none. A setting tune put in stays on the card, marked few, with too few
// calls or none at all; one the user wrote themselves, and one put back,
// leave it.
func TestTuneKeepsWhatItPutIn(t *testing.T) {
	sandboxHome(t)
	home := t.TempDir()
	kept := func(cs []sessions.Call) []tuneAdvice {
		t.Helper()
		var out []tuneAdvice
		for _, a := range tuneOf(cs, 1, home).Agents {
			if a.Agent != "claude" {
				continue
			}
			for _, ad := range a.Advice {
				if ad.Few {
					out = append(out, ad)
				}
			}
		}
		return out
	}
	few := tuneCalls("claude", "claude-opus-4-5", 5)
	if got := kept(few); len(got) != 0 {
		t.Fatalf("nothing put in, yet kept rows: %+v", got)
	}
	if err := agent.TuneApply(home, "claude", agent.TuneCache, "", "1h"); err != nil {
		t.Fatal(err)
	}
	if err := agent.TuneApply(home, "claude", agent.TuneCompact, "claude-opus-4-5", "300000"); err != nil {
		t.Fatal(err)
	}
	for name, cs := range map[string][]sessions.Call{"few calls": few, "no calls": nil} {
		got := kept(cs)
		if len(got) != 2 {
			t.Fatalf("%s: kept %+v; want the cache and the window", name, got)
		}
		if c := got[0]; c.Knob != agent.TuneCache || c.Setting.Value != "1h" || !c.Setting.Ours || c.TTL != nil {
			t.Errorf("%s: cache row %+v; want cache_ttl at 1h, magpie's", name, c)
		}
		if w := got[1]; w.Knob != agent.TuneCompact || w.Model != "claude-opus-4-5" || w.Setting.Value != "300000" || !w.Setting.Ours {
			t.Errorf("%s: window row %+v; want claude-opus-4-5's 300000, magpie's", name, w)
		}
	}
	// with enough calls the cache has its advice, and no second row
	for _, a := range tuneOf(tuneCalls("claude", "claude-opus-4-5", 80), 1, home).Agents {
		n := 0
		for _, ad := range a.Advice {
			if ad.Knob == agent.TuneCache {
				n++
				if ad.Few || ad.TTL == nil {
					t.Errorf("80 calls: the cache row %+v; want its advice", ad)
				}
			}
		}
		if n != 1 {
			t.Errorf("80 calls: %d cache rows; want 1", n)
		}
	}

	if err := agent.TuneUndo(home, "claude", agent.TuneCache, ""); err != nil {
		t.Fatal(err)
	}
	if err := agent.TuneUndo(home, "claude", agent.TuneCompact, "claude-opus-4-5"); err != nil {
		t.Fatal(err)
	}
	// put back, Claude Code's cache is its default again, still told
	if got := kept(nil); len(got) != 1 || got[0].Knob != agent.TuneCache || got[0].Setting.Value != "" || got[0].Setting.Ours {
		t.Fatalf("put back: kept %+v; want the cache at Claude Code's default alone", got)
	}
}

// linkaww on Discord (11-06), again: Claude Code here, through a Remote
// magpie, with too few calls read on this computer, and "这里什么都没有".
// The cache this computer's Claude Code keeps is on the card whoever set
// it, with no calls at all: the user's own 1h too, which Use 5 min puts
// at 5m and Undo puts back. No Claude Code folder here, no card.
func TestTuneTellsTheCacheHere(t *testing.T) {
	sandboxHome(t)
	home := t.TempDir()
	rows := func() []tuneAdvice {
		t.Helper()
		for _, a := range tuneOf(nil, 1, home).Agents {
			if a.Agent == "claude" {
				return a.Advice
			}
		}
		return nil
	}
	if st := tuneOf(nil, 1, home); len(st.Agents) != 0 {
		t.Fatalf("no Claude Code here, yet agents: %+v", st.Agents)
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := edit.SetJSON(settingsPath, edit.KV{Path: "promptCacheTtl", Value: "1h"}); err != nil {
		t.Fatal(err)
	}
	got := rows()
	if len(got) != 1 || got[0].Knob != agent.TuneCache || !got[0].Few || got[0].Setting.Value != "1h" || got[0].Setting.Ours || got[0].Setting.Source != "settings" {
		t.Fatalf("the user's own 1h, no calls: rows %+v; want the main cache at 1h, theirs", got)
	}
	// Use 5 min, then Undo: their 1h back
	if err := agent.TuneApply(home, "claude", agent.TuneCache, "", "5m"); err != nil {
		t.Fatal(err)
	}
	if got := rows(); len(got) != 1 || got[0].Setting.Value != "5m" || !got[0].Setting.Ours {
		t.Fatalf("after 5m: rows %+v; want 5m, magpie's", got)
	}
	if err := agent.TuneUndo(home, "claude", agent.TuneCache, ""); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(settingsPath, "promptCacheTtl"); v != `"1h"` && v != "1h" {
		t.Fatalf("undone: promptCacheTtl %q; want their 1h back", v)
	}
	// the subagents' cache is told when something sets it
	if err := edit.SetJSON(settingsPath, edit.KV{Path: "subagentPromptCacheTtl", Value: "1h"}); err != nil {
		t.Fatal(err)
	}
	if got := rows(); len(got) != 2 || got[1].Knob != agent.TuneSubCache || got[1].Setting.Value != "1h" {
		t.Fatalf("subagents at 1h: rows %+v; want both caches", got)
	}
	// a variable before the setting is told, locked
	t.Setenv("CLAUDE_CODE_PROMPT_CACHE_TTL", "1h")
	if got := rows(); len(got) == 0 || got[0].Setting.Locked != "CLAUDE_CODE_PROMPT_CACHE_TTL" {
		t.Fatalf("env 1h: rows %+v; want the main cache locked by it", got)
	}
}
