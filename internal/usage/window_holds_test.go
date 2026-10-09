package usage

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// holdsHome is a home whose models.dev copy prices a Claude and an OpenAI
// model as their vendors list them: Anthropic's with its cache writes (5
// minutes 1.25× input, an hour 2×), OpenAI's with cached input and no
// cache write price at all.
func holdsHome(t *testing.T) {
	t.Helper()
	sessionHome(t)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0700)
	os.WriteFile(catalog.CachePath(), []byte(`{
"anthropic":{"id":"anthropic","models":{"claude-opus-4-7":{"id":"claude-opus-4-7","cost":{"input":5,"output":25,"cache_read":0.5,"cache_write":6.25}},
  "claude-sonnet-4-6":{"id":"claude-sonnet-4-6","cost":{"input":3,"output":15,"cache_read":0.3,"cache_write":3.75}}}},
"openai":{"id":"openai","models":{"gpt-5.5":{"id":"gpt-5.5","cost":{"input":1.25,"output":10,"cache_read":0.125}}}}}`), 0600)
}

func near(a, b float64) bool { return math.Abs(a-b) <= 1e-9*max(1, math.Abs(b)) }

// Chiao on Discord: each window's whole, reckoned from the calls magpie
// routed through the account in it over the share used, at API prices —
// for Claude's 5 hours, its week and its Opus-only week, Codex's 5 hours,
// and a moved plugin's window told in seconds to its reset; and nothing
// where that wouldn't be honest.
func TestWindowHoldsReckonsWhatMagpieRouted(t *testing.T) {
	holdsHome(t)
	now := holdClock(t, time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC))
	readAt := now.Add(-2 * time.Minute)
	at := func(d time.Duration) *time.Time { x := now.Add(d); return &x }
	claude := Record{Agent: "claude-code", Provider: "claude", ProviderAccount: "Dee@example.com", Status: 200}
	call := func(base Record, ago time.Duration, model string, in, out, read, write, write1h int) Record {
		base.Time, base.Model, base.Input, base.Output, base.CacheRead, base.CacheWrite, base.CacheWrite1h = now.Add(-ago), model, in, out, read, write, write1h
		return base
	}
	codex := Record{Agent: "codex", Provider: "codex", Host: "chatgpt.com as sam@example.com", Status: 200} // an older record: its account in Host
	kiro := Record{Agent: "claude-code", Provider: "kiro", ProviderAccount: "kay@example.com", Status: 200}
	recs := []Record{
		call(claude, 1*time.Hour, "claude-opus-4-7", 1000, 2000, 50000, 4000, 1000),
		call(claude, 2*time.Hour, "claude-sonnet-4-6", 3000, 1000, 20000, 0, 0),
		call(claude, 6*time.Hour, "claude-opus-4-7", 10000, 5000, 0, 0, 0),             // before the 5 hours, in the week
		call(claude, 8*24*time.Hour, "claude-opus-4-7", 99000, 99000, 0, 0, 0),         // before the week: the log's first call
		call(claude, time.Minute, "claude-opus-4-7", 77000, 77000, 0, 0, 0),            // after the share was read
		{Time: now.Add(-time.Hour), Agent: "claude-code", Status: 429, Rejected: true}, // turned away here
		call(Record{Agent: "claude-code", Provider: "claude", ProviderAccount: "bob@example.com", Status: 200}, time.Hour, "claude-opus-4-7", 5000, 5000, 0, 0, 0),
		call(Record{Agent: "claude-code", Provider: "anthropic", ProviderKeyID: "k1", Status: 200}, time.Hour, "claude-opus-4-7", 5000, 5000, 0, 0, 0),
		call(codex, 30*time.Minute, "gpt-5.5", 4000, 1000, 60000, 2000, 0),
		call(kiro, 3*24*time.Hour, "claude-sonnet-4-6", 6000, 2000, 0, 0, 0),
		call(kiro, 24*time.Hour, "kiro-auto", 1000, 1000, 0, 0, 0), // no API price
	}
	for _, r := range recs {
		Append(r)
	}
	qs := []provider.SubscriptionQuota{
		{Provider: "claude", User: "dee@example.com", ReadAt: &readAt, Windows: []provider.QuotaWindow{
			{Name: "5 hours", Used: 20, ResetsAt: at(2 * time.Hour), Span: 5 * time.Hour},
			{Name: "7 days", Used: 40, ResetsAt: at(3 * 24 * time.Hour), Span: 7 * 24 * time.Hour},
			{Name: "7 days · Opus", Used: 50, ResetsAt: at(3 * 24 * time.Hour), Span: 7 * 24 * time.Hour, Model: "opus"},
			{Name: "Extra usage", Used: 30, ResetsAt: at(20 * 24 * time.Hour), Span: 30 * 24 * time.Hour, Aside: true},
		}},
		{Provider: "codex", User: "sam@example.com", Windows: []provider.QuotaWindow{
			{Name: "5 hours", Used: 10, ResetSecs: 3 * 3600, Span: 5 * time.Hour},
			{Name: "7 days", Used: 4, ResetsAt: at(4 * 24 * time.Hour), Span: 7 * 24 * time.Hour}, // too little used to scale
		}},
		{Provider: "kiro", Name: "Kiro", User: "kay@example.com", Windows: []provider.QuotaWindow{
			{Name: "Weekly", Used: 25, ResetSecs: 2 * 24 * 3600}, // its span from its name
			{Name: "Credits", Used: 30},                          // no reset: no start
		}},
		{Provider: "claude", User: "bob@example.com", Error: "sign in again", Windows: []provider.QuotaWindow{{Name: "5 hours", Used: 50, ResetsAt: at(time.Hour), Span: 5 * time.Hour}}},
	}
	got := WithWindowHolds(qs, now)

	opus := func(in, out, read, write, write1h float64) float64 {
		return (in*5 + out*25 + read*0.5 + (write-write1h)*6.25 + write1h*10) / 1e6
	}
	sonnet := func(in, out, read float64) float64 { return (in*3 + out*15 + read*0.3) / 1e6 }
	want := []struct {
		q, w           int
		tokens, routed int64
		cost           float64 // 0: not priced
		calls          int
	}{
		{0, 0, (3000 + 4000) * 5, 7000, (opus(1000, 2000, 50000, 4000, 1000) + sonnet(3000, 1000, 20000)) * 5, 2},
		{0, 1, (3000 + 4000 + 15000) * 100 / 40, 22000, (opus(1000, 2000, 50000, 4000, 1000) + sonnet(3000, 1000, 20000) + opus(10000, 5000, 0, 0, 0)) * 100 / 40, 3},
		{0, 2, (3000 + 15000) * 2, 18000, (opus(1000, 2000, 50000, 4000, 1000) + opus(10000, 5000, 0, 0, 0)) * 2, 2},
		// OpenAI lists no cache write: what was written is priced at nothing, not at input
		{1, 0, 5000 * 10, 5000, (4000*1.25 + 1000*10 + 60000*0.125) / 1e6 * 10, 1},
		{2, 0, (8000 + 2000) * 4, 10000, 0, 2},
	}
	for _, x := range want {
		w := got[x.q].Windows[x.w]
		h := w.Holds
		if h == nil {
			t.Fatalf("%s %s: no whole reckoned", got[x.q].Provider, w.Name)
		}
		if h.Tokens != x.tokens || h.Routed.Tokens != x.routed || h.Routed.Calls != x.calls || h.Used != w.Used {
			t.Errorf("%s %s: %d tokens whole from %d in %d calls at %v%%, want %d from %d in %d", got[x.q].Provider, w.Name, h.Tokens, h.Routed.Tokens, h.Routed.Calls, h.Used, x.tokens, x.routed, x.calls)
		}
		if h.Priced != (x.cost > 0) || !near(h.Cost, x.cost) {
			t.Errorf("%s %s: cost %v (priced %v), want %v", got[x.q].Provider, w.Name, h.Cost, h.Priced, x.cost)
		}
	}
	for _, x := range [][2]int{{0, 3}, {1, 1}, {2, 1}, {3, 0}} {
		if h := got[x[0]].Windows[x[1]].Holds; h != nil {
			t.Errorf("%s %s: reckoned %+v, want none", got[x[0]].Provider, got[x[0]].Windows[x[1]].Name, h)
		}
	}
	// the cards the caller passed, which a cache may share, are as they were
	for _, q := range qs {
		for _, w := range q.Windows {
			if w.Holds != nil {
				t.Fatalf("the caller's %s %s was written to", q.Provider, w.Name)
			}
		}
	}
}

// A window begun before magpie's log has calls magpie never saw: its whole
// isn't reckoned, nor one of an account magpie routed nothing for in it.
func TestWindowHoldsNeedsTheWholeWindowLogged(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	reset := now.Add(3 * 24 * time.Hour)
	qs := []provider.SubscriptionQuota{{Provider: "claude", User: "dee@example.com", Windows: []provider.QuotaWindow{
		{Name: "7 days", Used: 40, ResetsAt: &reset, Span: 7 * 24 * time.Hour}}}}
	recs := []Record{{Time: now.Add(-time.Hour), Provider: "claude", ProviderAccount: "dee@example.com", Model: "claude-opus-4-7", Input: 100, Output: 100, Status: 200}}
	read := func(fn func(Record)) {
		for _, r := range recs {
			fn(r)
		}
	}
	priceOf := func(Record) *catalog.Price { return nil }
	if got := windowHolds(qs, now, now.Add(-2*24*time.Hour), priceOf, nil, read); got[0].Windows[0].Holds != nil {
		t.Fatalf("the log began 2 days into the week, and the week was reckoned: %+v", got[0].Windows[0].Holds)
	}
	got := windowHolds(qs, now, now.Add(-30*24*time.Hour), priceOf, nil, read)
	if h := got[0].Windows[0].Holds; h == nil || h.Tokens != 500 || h.Priced {
		t.Fatalf("logged since before the week: %+v, want 500 tokens, unpriced", h)
	}
	if got := windowHolds(qs, now, now.Add(-30*24*time.Hour), priceOf, nil, func(func(Record)) {}); got[0].Windows[0].Holds != nil {
		t.Fatalf("nothing routed, and the week was reckoned: %+v", got[0].Windows[0].Holds)
	}
	// a provider since renamed: its calls under the old id are the account's
	recs[0].Provider = "claude-old"
	if got := windowHolds(qs, now, now.Add(-30*24*time.Hour), priceOf, map[string]string{"claude-old": "claude"}, read); got[0].Windows[0].Holds == nil {
		t.Fatal("a call under the provider's old id wasn't counted")
	}
}

// A month's window began on the same day a month before its reset, not 30
// days before: Kiro's credits reset on the 1st.
func TestWindowBoundsOfAMonth(t *testing.T) {
	reset := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	w := provider.QuotaWindow{Name: "Credits", Used: 30, ResetsAt: &reset, Span: 30 * 24 * time.Hour}
	start, end, ok := w.Bounds(reset.Add(-time.Hour))
	if !ok || !start.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) || !end.Equal(reset) {
		t.Fatalf("bounds %v – %v (%v), want Oct 1 – Nov 1", start, end, ok)
	}
}
