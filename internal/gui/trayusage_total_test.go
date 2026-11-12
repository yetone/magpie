package gui

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/usage"
)

// #1984: the menu bar shows magpie's own total for today or the last 7
// days, tokens over cost, beside the allowances or alone, without asking
// any vendor.
func TestTrayTotals(t *testing.T) {
	sums := map[usage.Period]usage.Totals{
		usage.Today: {Calls: 152, Input: 912_000, Output: 318_400, CacheRead: 4_900_000, CacheWrite: 200_000, Cost: 3.4, Unpriced: 2},
		usage.Week:  {Calls: 2310, Input: 41_000_000, Output: 6_200_000, Cost: 128.25},
	}
	var asked []usage.Period
	old := trayTotalSum
	trayTotalSum = func(p usage.Period) usage.Totals { asked = append(asked, p); return sums[p] }
	t.Cleanup(func() { trayTotalSum = old })

	// a period the Usage page has no window for, or no total at all, is
	// left out, as a card no longer there is
	cards := trayUsageCards(context.Background(), "en", []string{"usage:7d", "usage:3d", "usage:", "usage:today"})
	if !slices.Equal(asked, []usage.Period{usage.Week, usage.Today}) {
		t.Fatalf("summed %v, want 7d then today", asked)
	}
	cells, label, tip := trayUsageView(cards, time.Now(), false, false)
	if label != "Σ47M · $128 | Σ6.3M · $3.40+" {
		t.Errorf("label %q", label)
	}
	if len(cells) != 2 {
		t.Fatalf("%d cells: %+v", len(cells), cells)
	}
	for i, want := range []struct {
		id   string
		rows []string
	}{{"usage:7d", []string{"Σ47M", "$128"}}, {"usage:today", []string{"Σ6.3M", "$3.40+"}}} {
		c := cells[i]
		if c.ID != want.id || !slices.Equal(c.Rows, want.rows) {
			t.Errorf("cell %d: %q %q, want %q %q", i, c.ID, c.Rows, want.id, want.rows)
		}
		// the bird beside it is whose it is: no vendor's logo nor letter
		if !c.Plain || c.Icon != nil || c.Letter != "" {
			t.Errorf("cell %d: plain %v icon %d bytes letter %q", i, c.Plain, len(c.Icon), c.Letter)
		}
	}
	if want := "Last 7 days in magpie\n47M tokens (41M in · 6.2M out)\n≈$128\n2310 requests\n\n" +
		"Today in magpie\n6.3M tokens (912K in · 318K out · 5.1M cached)\n≈$3.40+\n152 requests, 2 with no known price"; tip != want {
		t.Errorf("tooltip %q\nwant %q", tip, want)
	}

	// nothing priced: the tokens alone; nothing at all: still a 0, so the
	// menu bar doesn't look broken first thing in the morning
	if q := trayTotalCard("en", "usage:today", usage.Totals{Calls: 3, Input: 950, Unpriced: 3}, "usd", 0); q.Balance != "Σ950" {
		t.Errorf("unpriced: %q", q.Balance)
	}
	if q := trayTotalCard("en", "usage:today", usage.Totals{}, "usd", 0); q.Balance != "Σ0" {
		t.Errorf("none: %q", q.Balance)
	}
	// in the currency Settings says
	if q := trayTotalCard("en", "usage:today", usage.Totals{Input: 1000, Cost: 2}, "cny", 7.1); q.Balance != "Σ1.0K · ¥14.20" {
		t.Errorf("cny: %q", q.Balance)
	}

	// clicked, it opens the Usage page at its period, not a card of a
	// provider named "usage:today"
	if got := quotaView("usage:7d"); got != "usage&tab=requests&period=7d" {
		t.Errorf("quotaView: %q", got)
	}
	if got := panelQuotaJS("usage:today"); got != `panelUseFocus("today")` {
		t.Errorf("panelQuotaJS: %q", got)
	}
	if got := quotaView("usage:3d"); strings.Contains(got, "period") {
		t.Errorf("an unknown period: %q", got)
	}
}

// Totals beside a subscription's card keep its order and its cell.
func TestTrayTotalsBesideCards(t *testing.T) {
	old := trayTotalSum
	trayTotalSum = func(usage.Period) usage.Totals { return usage.Totals{Input: 5000, Output: 1000, Cost: 0.12} }
	t.Cleanup(func() { trayTotalSum = old })
	cards := append(trayCards(), trayTotalCard("en", "usage:today", trayTotalSum(usage.Today), "usd", 0))
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	cells, label, _ := trayUsageView(trayPick(cards, []string{"usage:today", "claude|a@b.c"}, now), now, false, false)
	if label != "Σ6.0K · $0.120 | 42% · 18%" {
		t.Errorf("label %q", label)
	}
	if len(cells) != 2 || !cells[0].Plain || cells[1].Plain || cells[1].Icon == nil {
		t.Errorf("%d cells, plain %v, the second's logo %d bytes", len(cells), len(cells) > 0 && cells[0].Plain, len(cells[len(cells)-1].Icon))
	}
}

func TestTrayCount(t *testing.T) {
	for n, want := range map[int]string{0: "0", 950: "950", 1000: "1.0K", 8_249: "8.2K", 456_400: "456K",
		1_230_000: "1.2M", 133_400_000: "133M", 2_345_000_000: "2.35B"} {
		if got := trayCount("en", n); got != want {
			t.Errorf("trayCount(%d) = %q, want %q", n, got, want)
		}
	}
}

// The menu bar's total is the one the Requests tab and the panel's Usage
// tab show, which a click opens: the calls an agent's own session file
// records count, and so do the cache's tokens. Summed as the Overview
// sums, it left the former out and the latter aside, and its number was
// neither the page's input nor its total (NextCommit on Discord).
func TestTrayTotalIsTheLedgers(t *testing.T) {
	sandboxHome(t)
	now := holdUsageClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	before := usage.LogCalls
	usage.LogCalls = func(time.Time) []sessions.Call {
		return []sessions.Call{{Time: now, Agent: "codex", Session: "s", Model: "gpt-5.5", Tokens: sessions.Tokens{Input: 1000, Output: 200, CacheRead: 8000}}}
	}
	t.Cleanup(func() { usage.LogCalls = before })
	sum := trayTotalSum(usage.Today)
	if sum != ledgerPage(usage.Today, usage.Filter{}, 0, 1).Totals {
		t.Fatalf("menu bar %+v, ledger %+v", sum, ledgerPage(usage.Today, usage.Filter{}, 0, 1).Totals)
	}
	if q := trayTotalCard("en", "usage:today", sum, "usd", 0); !strings.HasPrefix(q.Balance, "Σ9.2K") || !strings.Contains(q.Name, "9.2K tokens (1.0K in · 200 out · 8.0K cached)") {
		t.Fatalf("%q %q", q.Balance, q.Name)
	}
}

// #4366: the menu bar is read again seconds apart, and a total is summed
// at most once a minute, again on another day.
func TestTrayTotalSummedOnceAMinute(t *testing.T) {
	var n int
	old := trayQuerySum
	trayQuerySum = func(p usage.Period) usage.Totals { n++; return usage.Totals{Calls: n} }
	t.Cleanup(func() { trayQuerySum = old })
	var m trayTotalMemo
	at := time.Date(2026, 11, 1, 23, 58, 0, 0, time.Local)
	for i := range 20 {
		if got := m.sum(usage.Today, at.Add(time.Duration(i)*2*time.Second)); got.Calls != 1 {
			t.Fatalf("read %d: summed %d times", i, got.Calls)
		}
	}
	if m.sum(usage.Week, at).Calls != 2 {
		t.Fatalf("7d was given today's sum")
	}
	if m.sum(usage.Today, at.Add(trayTotalFresh)).Calls != 3 {
		t.Fatalf("a minute old and not summed again")
	}
	// past midnight, under a minute after: the new day is summed
	if m.sum(usage.Today, at.Add(trayTotalFresh+90*time.Second)).Calls != 4 {
		t.Fatalf("a new day was given yesterday's total")
	}
	if m.sum(usage.Today, at.Add(trayTotalFresh+100*time.Second)).Calls != 4 {
		t.Fatalf("summed again within the minute")
	}
}
