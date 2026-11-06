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
	if got := kept(nil); len(got) != 0 {
		t.Fatalf("put back, yet kept: %+v", got)
	}
	if st := tuneOf(nil, 1, home); len(st.Agents) != 0 {
		t.Errorf("no calls and nothing put in, yet agents: %+v", st.Agents)
	}

	// the user's own 1h is theirs to change in their file: no kept row
	if err := edit.SetJSON(filepath.Join(home, ".claude", "settings.json"), edit.KV{Path: "promptCacheTtl", Value: "1h"}); err != nil {
		t.Fatal(err)
	}
	if s, _ := agent.TuneRead(home, "claude", agent.TuneCache, ""); s.Value != "1h" {
		t.Fatalf("the user's own 1h reads %+v", s)
	}
	if got := kept(few); len(got) != 0 {
		t.Errorf("the user's own 1h: kept %+v; want none", got)
	}
}
