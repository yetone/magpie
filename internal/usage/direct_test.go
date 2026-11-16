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
	// a Claude Code call the gateway saw, told by the request id both name
	Append(Record{Time: now.Add(-3 * time.Hour), Agent: "claude-cli/2", Provider: "relay", Model: "m", Input: 9, Output: 9, Status: 200, Session: "g2", RequestID: "req_1"})
	old := LogCalls
	LogCalls = func(time.Time) []sessions.Call {
		return []sessions.Call{
			{Time: now.Add(-time.Hour), Agent: "codex", Session: "c1", Model: "gpt-6", Tokens: sessions.Tokens{Input: 1000, Output: 100}},
			{Time: now.Add(-2 * time.Hour), Agent: "codex", Session: "c1", Model: "gpt-6", Tokens: sessions.Tokens{Input: 3000, Output: 300}},
			{Time: now.Add(-30*time.Minute + 9500*time.Millisecond), Agent: "codex", Session: "h1", Model: "gpt-6", Tokens: sessions.Tokens{Input: 700, Output: 70, CacheRead: 300}},
			{Time: now.Add(-3*time.Hour + time.Minute), Agent: "claude-code", Session: "c2", Model: "m", RequestID: "req_1", Tokens: sessions.Tokens{Input: 9, Output: 9}},
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
	// Direct and Summaries work the same out from the ledger's index
	// without pricing the gateway's calls (Vantiboo1ean, ~750k requests)
	_, both := Summaries(Month)
	for _, got := range []Summary{Direct(Month), both} {
		if got.Totals != d.Totals || len(got.Agents) != 1 || len(got.Models) != 1 || len(got.Sessions) != 1 || got.Sessions[0] != d.Sessions[0] {
			t.Fatalf("Direct %+v, from the ledger %+v", got, d)
		}
	}
	// the gateway's calls are Summarize's alone
	if s := Summarize(Month); s.Calls != 3 || len(s.Agents) != 3 {
		t.Fatalf("summary %+v", s.Totals)
	}
	// a period that starts after the calls has none of them
	if d := direct(Today, now.Add(48*time.Hour), LedgerOf(Month, Filter{}).Rows); d.Calls != 0 {
		t.Fatalf("today two days on: %+v", d.Totals)
	}
}

// Qoder's own calls are under Direct by Qoder (eating_dream on X), counted
// by the credits their replies cost, as Qoder writes its own models' with
// no tokens. Its "auto" routes inside Qoder and is no one model, so it has
// no price, though a catalog lists an "auto" of its own.
func TestDirectQoderCallsByCredits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"openrouter":{"id":"openrouter","models":{"auto":{"id":"auto","cost":{"input":2,"output":10}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	now := holdClock(t, time.Date(2026, 11, 15, 12, 0, 0, 0, time.Local))
	credit := func(v float64) *float64 { return &v }
	old := LogCalls
	LogCalls = func(time.Time) []sessions.Call {
		return []sessions.Call{
			{Time: now.Add(-time.Hour), Agent: "qoder", Session: "q1", Model: "auto", Msg: "msg_q2", Credits: credit(0.18)},
			{Time: now.Add(-time.Hour - time.Minute), Agent: "qoder", Session: "q1", Model: "auto", Msg: "msg_q1", Credits: credit(0.42)},
			{Time: now.Add(-2 * time.Hour), Agent: "qoder-cn", Session: "q2", Model: "auto", Msg: "msg_c1", Credits: credit(1.5)},
			// QODER_EXPOSE_TOKEN_USAGE=1 writes the counts
			{Time: now.Add(-3 * time.Hour), Agent: "qoder", Session: "q3", Model: "auto", Msg: "msg_q4", Credits: credit(0.3), Tokens: sessions.Tokens{Input: 1000, Output: 100}},
		}
	}
	t.Cleanup(func() { LogCalls = old })

	d := Direct(Month)
	if d.Calls != 4 || d.Credited != 4 || d.Credits < 2.3999 || d.Credits > 2.4001 || d.Input != 1000 {
		t.Fatalf("direct totals %+v", d.Totals)
	}
	if d.Cost != 0 || d.Unpriced != 1 {
		t.Fatalf("Qoder's auto was priced: %+v", d.Totals)
	}
	agents := map[string]Group{}
	for _, g := range d.Agents {
		agents[g.ID] = g
	}
	if g := agents["qoder"]; g.Calls != 3 || g.Credited != 3 {
		t.Fatalf("qoder: %+v of %+v", g, d.Agents)
	}
	if g := agents["qoder-cn"]; g.Calls != 1 || g.Credits != 1.5 {
		t.Fatalf("qoder-cn: %+v of %+v", g, d.Agents)
	}
	for _, m := range d.Models {
		if m.Model != "auto" || m.Cost != 0 {
			t.Fatalf("models %+v", d.Models)
		}
	}
}

// Droid's own sessions are under Direct by Droid (_meouwu on X), a
// session's total as one call (sessions' calls_droid.go), priced by its
// model.
func TestDirectDroidSessionTotals(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"anthropic":{"id":"anthropic","models":{"claude-sonnet-4-5-20250929":{"id":"claude-sonnet-4-5-20250929","cost":{"input":3,"output":15}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	now := holdClock(t, time.Date(2026, 11, 15, 12, 0, 0, 0, time.Local))
	old := LogCalls
	LogCalls = func(time.Time) []sessions.Call {
		return []sessions.Call{
			{Time: now.Add(-time.Hour), Agent: "droid", Session: "d1", Model: "claude-sonnet-4-5-20250929", Msg: "droid:d1", Tokens: sessions.Tokens{Input: 30750, Output: 113, CacheRead: 1000}},
			{Time: now.Add(-2 * time.Hour), Agent: "droid", Session: "d1", Model: "claude-sonnet-4-5-20250929", Msg: "droid:d2", Tokens: sessions.Tokens{Input: 5, Output: 2}},
		}
	}
	t.Cleanup(func() { LogCalls = old })

	d := Direct(Month)
	if d.Calls != 2 || d.Input != 30755 || d.Output != 115 || d.CacheRead != 1000 || d.Cost == 0 || d.Unpriced != 0 {
		t.Fatalf("direct totals %+v", d.Totals)
	}
	if len(d.Agents) != 1 || d.Agents[0].ID != "droid" || len(d.Sessions) != 1 || d.Sessions[0].ID != "d1" {
		t.Fatalf("agents %+v sessions %+v", d.Agents, d.Sessions)
	}
}
