package gateway

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// #1778 (Vigilans): a Factory account, its 5 hours unused, 32% into its
// week (renewing in 3 days) and 16% into its month (in 27), beside a Devin
// account whose week renews in 5 days. The Routing page said "32% used ·
// renews in 27 days": the week's share with the month's reset. The trace
// now says when the window the share is of renews, the week, while Smart
// still goes by the biggest window: what is left of Factory's week isn't
// lost at its reset while its month, which binds it, renews later, so
// Devin, whose week is all it has, goes first.
func TestUsedRenewsWithItsOwnWindow(t *testing.T) {
	now := time.Now()
	five, week, month := now.Add(4*time.Hour), now.Add(3*24*time.Hour), now.Add(27*24*time.Hour)
	devinWeek := now.Add(5 * 24 * time.Hour)
	factory := provider.Allowance{
		{Name: "5 hours", Used: 0, Resets: five, Span: 5 * time.Hour},
		{Name: "7 days", Used: 32, Resets: week, Span: 7 * 24 * time.Hour},
		{Name: "30 days", Used: 16, Resets: month, Span: 30 * 24 * time.Hour},
	}
	devin := provider.Allowance{{Name: "Weekly", Used: 40, Resets: devinWeek, Span: 7 * 24 * time.Hour}}
	old := allowances
	t.Cleanup(func() { allowances = old })
	allowances = func(agent string) map[string]provider.Allowance {
		switch agent {
		case "factory":
			return map[string]provider.Allowance{"f@example.com": factory}
		case "devin":
			return map[string]provider.Allowance{"d@example.com": devin}
		}
		return nil
	}
	fp := provider.Provider{ID: "factory", Account: &provider.Account{Agent: "factory", User: "f@example.com"}}
	dp := provider.Provider{ID: "devin", Account: &provider.Account{Agent: "devin", User: "d@example.com"}}
	cs := []candidate{
		{p: fp, model: "claude-opus-5-5", rest: "factory"},
		{p: dp, model: "claude-opus-5-5", rest: "devin"},
	}
	group := provider.Provider{ID: "g"}
	got, wg := weigh(group, cs, "claude-opus-5-5", provider.Anthropic)
	if restsOf(got) != restsOf([]candidate{cs[1], cs[0]}) {
		t.Fatalf("Smart: %s, want Devin first (Factory's month renews later)", restsOf(got))
	}
	w := weighed(got[1], fp, wg, false, provider.Anthropic)
	if !w.Known || w.Used != 32 {
		t.Fatalf("Factory's trace: %+v", w)
	}
	if w.UsedRenews == nil || !w.UsedRenews.Equal(week) {
		t.Fatalf("Factory's 32%% renews %v, want with its week %v (not its month %v)", w.UsedRenews, week, month)
	}
	if len(w.Renews) != 3 || !w.Renews[0].Equal(month) {
		t.Fatalf("its windows' renewals, the biggest first: %v", w.Renews)
	}
	if d := weighed(got[0], dp, wg, false, provider.Anthropic); d.UsedRenews == nil || !d.UsedRenews.Equal(devinWeek) {
		t.Fatalf("Devin's: %+v", d)
	}
	// the window the share is of, named, as more than one counts the
	// model; Devin's one window needs no name (Wing8Aqua on X)
	if w.UsedWindow != "7 days" {
		t.Fatalf("Factory's 32%% is of %q, want its week", w.UsedWindow)
	}
	if d := weighed(got[0], dp, wg, false, provider.Anthropic); d.UsedWindow != "" {
		t.Fatalf("Devin's one window named: %q", d.UsedWindow)
	}
}

// Wing8Aqua on X: two Claude Code accounts In turn, each at 0% of its
// five hours and some of its week. The share the row shows is the week's,
// the fullest, which sets an account aside under every routing; the trace
// names it so the row says "7d 41% used", not a bare 41% read as the
// five hours'.
func TestUsedWindowNamesTheWeekInTurn(t *testing.T) {
	now := time.Now()
	five, week := now.Add(5*time.Hour), now.Add(2*24*time.Hour)
	claude := provider.Allowance{
		{Name: "5 hours", Used: 0, Resets: five, Span: 5 * time.Hour},
		{Name: "7 days", Used: 41, Resets: week, Span: 7 * 24 * time.Hour},
	}
	old := allowances
	t.Cleanup(func() { allowances = old })
	allowances = func(agent string) map[string]provider.Allowance {
		if agent == "claude" {
			return map[string]provider.Allowance{"a@example.com": claude, "b@example.com": claude}
		}
		return nil
	}
	a := provider.Provider{ID: "claude", Routing: provider.Rotate, Account: &provider.Account{Agent: "claude", User: "a@example.com"}}
	b := provider.Provider{ID: "claude", Routing: provider.Rotate, Account: &provider.Account{Agent: "claude", User: "b@example.com"}}
	cs := []candidate{{p: a, model: "claude-opus-5-5", rest: "claude@a"}, {p: b, model: "claude-opus-5-5", rest: "claude@b"}}
	got, wg := weigh(provider.Provider{ID: "claude", Routing: provider.Rotate}, cs, "claude-opus-5-5", provider.Anthropic)
	for _, c := range got {
		w := weighed(c, c.p, wg, false, provider.Anthropic)
		if !w.Known || w.Used != 41 || w.UsedWindow != "7 days" || w.UsedRenews == nil || !w.UsedRenews.Equal(week) {
			t.Fatalf("%s: used %v of %q renewing %v, want 41 of its week", c.rest, w.Used, w.UsedWindow, w.UsedRenews)
		}
	}
}
