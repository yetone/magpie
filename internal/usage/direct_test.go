package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/sessions"
)

// Direct sums up only the calls the agents made on their own, as their
// session files tell them — a Codex turn on the user's own ChatGPT sign-in,
// say — by agent, model and session, priced by their model; the gateway's
// calls stay Summarize's (Kumo31 on Discord: Codex used outside magpie was
// on the window's Usage tab but not in magpie usage).
func TestDirectCountsOnlySessionFileCalls(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"openai":{"id":"openai","models":{"gpt-6":{"id":"gpt-6","cost":{"input":2,"output":10}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	Append(Record{Time: now.Add(-10 * time.Minute), Agent: "claude", Provider: "relay", Model: "m", Input: 5, Output: 5, Status: 200, Session: "g1"})
	// a client built on Codex, which the gateway logs under its own name
	// while its Codex session file names Codex: the same call, not one of
	// the agents' own
	Append(Record{Time: now.Add(-30 * time.Minute), Agent: "miyi-helper", Provider: "codex", Model: "gpt-6", Input: 700, Output: 70, CacheRead: 300, Millis: 9000, Status: 200, Session: "h1"})
	old := LogCalls
	LogCalls = func(time.Time) []sessions.Call {
		return []sessions.Call{
			{Time: now.Add(-time.Hour), Agent: "codex", Session: "c1", Model: "gpt-6", Tokens: sessions.Tokens{Input: 1000, Output: 100}},
			{Time: now.Add(-2 * time.Hour), Agent: "codex", Session: "c1", Model: "gpt-6", Tokens: sessions.Tokens{Input: 3000, Output: 300}},
			{Time: now.Add(-30*time.Minute + 9500*time.Millisecond), Agent: "codex", Session: "h1", Model: "gpt-6", Tokens: sessions.Tokens{Input: 700, Output: 70, CacheRead: 300}},
		}
	}
	t.Cleanup(func() { LogCalls = old })

	d := direct(Month, now, LedgerOf(Month, Filter{}).Rows)
	if d.Calls != 2 || d.Input != 4000 || d.Output != 400 {
		t.Fatalf("direct totals %+v", d.Totals)
	}
	if len(d.Agents) != 1 || d.Agents[0].ID != "codex" || d.Agents[0].Calls != 2 {
		t.Fatalf("agents %+v", d.Agents)
	}
	if len(d.Models) != 1 || d.Models[0].Model != "gpt-6" || len(d.Sessions) != 1 || d.Sessions[0].ID != "c1" {
		t.Fatalf("models %+v sessions %+v", d.Models, d.Sessions)
	}
	if d.Cost == 0 || d.Unpriced != 0 {
		t.Fatalf("not priced: %+v", d.Totals)
	}
	// the gateway's calls are Summarize's alone
	if s := Summarize(Month); s.Calls != 2 || len(s.Agents) != 2 {
		t.Fatalf("summary %+v", s.Totals)
	}
	// a period that starts after the calls has none of them
	if d := direct(Today, now.Add(48*time.Hour), LedgerOf(Month, Filter{}).Rows); d.Calls != 0 {
		t.Fatalf("today two days on: %+v", d.Totals)
	}
}
