package usageapi

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
	"github.com/yetone/magpie/internal/usage"
)

// usage:day (a plugin page's day statement) is the day asked for, in the
// reader's zone: its totals, each agent's and each agent's models', the
// days before it with calls, and nothing of an account, a key or a
// session. A day after today is refused, as is a date that isn't one.
func TestDayOf(t *testing.T) {
	// a usage log of its own: the package's other tests read the shared one
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	heldClients := Clients
	Clients = func() []Client { return []Client{{ID: "claude", Name: "Claude Code", Icon: "claude-color"}} }
	t.Cleanup(func() { Clients = heldClients })
	now := time.Now()
	held := usage.Clock
	usage.Clock = func() time.Time { return now }
	t.Cleanup(func() { usage.Clock = held })
	y, m, d := now.Date()
	midnight := time.Date(y, m, d, 0, 0, 0, 0, time.Local)
	// between midnight and now: today's, whenever the test runs
	at := midnight.Add(now.Sub(midnight) / 2)
	yesterday := midnight.Add(-12 * time.Hour)
	for _, r := range []usage.Record{
		{Time: at, Agent: "claude", Provider: "anthropic", Model: "claude-x", Input: 100, Output: 10, CacheRead: 300, ProviderAccount: "me@example.com", CallerKeyName: "secret-key"},
		{Time: at, Agent: "claude", Provider: "deepseek", Model: "deepseek-y", Input: 50, Output: 5},
		{Time: at, Agent: "codex", Provider: "openai", Model: "gpt-z", Input: 10, Output: 1},
		{Time: yesterday, Agent: "codex", Provider: "openai", Model: "gpt-z", Input: 7, Output: 1},
	} {
		usage.Append(r)
	}
	today := now.Format(time.DateOnly)

	day, err := DayOf("", time.Local)
	if err != nil {
		t.Fatal(err)
	}
	if day.Date != today || day.Today != today {
		t.Fatalf("date %s, today %s, want %s", day.Date, day.Today, today)
	}
	if day.Calls != 3 || day.Input != 160 || day.Output != 16 || day.CacheRead != 300 {
		t.Errorf("totals %+v", day.DayTotals)
	}
	if len(day.Agents) != 2 || day.Agents[0].ID != "claude" || day.Agents[0].Name != "Claude Code" || day.Agents[1].ID != "codex" || day.Agents[0].Icon != "claude-color" || day.Agents[1].Name != "codex" || day.Agents[1].Icon != "generic" {
		t.Fatalf("agents %+v", day.Agents)
	}
	if a := day.Agents[0]; a.Calls != 2 || len(a.Models) != 2 || a.Models[0].Model != "claude-x" || a.Models[0].Provider != "anthropic" || a.Models[1].Model != "deepseek-y" || a.Models[1].ProviderName != "DeepSeek" {
		t.Errorf("claude's models %+v", a.Models)
	}
	if a := day.Agents[1]; a.Calls != 1 || len(a.Models) != 1 || a.Models[0].Model != "gpt-z" {
		t.Errorf("codex's day %+v", a)
	}
	if len(day.Days) != 2 || day.Days[0].Date != yesterday.Format(time.DateOnly) || day.Days[1].Date != today || day.Days[1].Calls != 3 {
		t.Errorf("days %+v", day.Days)
	}
	b, _ := json.Marshal(day)
	for _, s := range []string{"me@example.com", "secret-key"} {
		if strings.Contains(string(b), s) {
			t.Errorf("the day carries %q: %s", s, b)
		}
	}

	// yesterday: codex alone, and the days up to it only
	day, err = DayOf(yesterday.Format(time.DateOnly), time.Local)
	if err != nil {
		t.Fatal(err)
	}
	if day.Calls != 1 || len(day.Agents) != 1 || day.Agents[0].ID != "codex" || len(day.Days) != 1 {
		t.Errorf("yesterday %+v", day)
	}

	if _, err := DayOf(now.AddDate(0, 0, 1).Format(time.DateOnly), time.Local); err == nil {
		t.Error("tomorrow was read")
	}
	for _, bad := range []string{"today", "2026-13-01", "../x"} {
		if _, err := DayOf(bad, time.Local); err == nil {
			t.Errorf("%q was read", bad)
		}
	}
}

// usage:day with a from is the days from there to the date, a week's, a
// month's or all time's (Day card's period switch, HeheLi29136651 on X):
// one set of totals and agents over all of them, from the first day with
// calls for "all".
func TestSpanOf(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	heldClients := Clients
	Clients = func() []Client { return []Client{{ID: "claude", Name: "Claude Code", Icon: "claude-color"}} }
	t.Cleanup(func() { Clients = heldClients })
	now := time.Now()
	held := usage.Clock
	usage.Clock = func() time.Time { return now }
	t.Cleanup(func() { usage.Clock = held })
	y, m, d := now.Date()
	noon := time.Date(y, m, d, 12, 0, 0, 0, time.Local)
	ago := func(n int) time.Time { return noon.AddDate(0, 0, -n) }
	for _, r := range []usage.Record{
		{Time: ago(300), Agent: "codex", Provider: "openai", Model: "gpt-z", Input: 1000, Output: 100},
		{Time: ago(20), Agent: "claude", Provider: "anthropic", Model: "claude-x", Input: 500, Output: 50},
		{Time: ago(3), Agent: "claude", Provider: "anthropic", Model: "claude-x", Input: 40, Output: 4},
		{Time: ago(3), Agent: "codex", Provider: "openai", Model: "gpt-z", Input: 30, Output: 3},
		{Time: ago(0), Agent: "claude", Provider: "anthropic", Model: "claude-x", Input: 2, Output: 1},
	} {
		usage.Append(r)
	}
	day := func(n int) string { return ago(n).Format(time.DateOnly) }

	// a week: the seven days ending today
	w, err := SpanOf(day(6), "", time.Local)
	if err != nil {
		t.Fatal(err)
	}
	if w.From != day(6) || w.Date != day(0) || w.Calls != 3 || w.Input != 72 || w.Output != 8 || len(w.Agents) != 2 || w.Agents[0].ID != "claude" || w.Agents[0].Calls != 2 || w.Agents[1].Models[0].Model != "gpt-z" {
		t.Errorf("week %+v", w)
	}
	// a month's worth reaches the call 20 days back, not the one 40 back
	mo, err := SpanOf(day(29), "", time.Local)
	if err != nil || mo.Calls != 4 || mo.Input != 572 {
		t.Errorf("month %+v %v", mo, err)
	}
	// all time: from the first day with calls
	all, err := SpanOf(SpanAll, "", time.Local)
	if err != nil {
		t.Fatal(err)
	}
	if all.From != day(300) || all.Date != day(0) || all.Calls != 5 || all.Input != 1572 || len(all.Days) != 4 {
		t.Errorf("all %+v", all)
	}
	// up to an earlier date it stops there
	upTo, err := SpanOf(SpanAll, day(10), time.Local)
	if err != nil || upTo.From != day(300) || upTo.Date != day(10) || upTo.Calls != 2 {
		t.Errorf("all to %s: %+v %v", day(10), upTo, err)
	}
	// no from is the day alone, as DayOf is
	one, err := SpanOf("", day(3), time.Local)
	if err != nil || one.From != day(3) || one.Date != day(3) || one.Calls != 2 {
		t.Errorf("day %+v %v", one, err)
	}
	for _, bad := range [][2]string{{"nope", ""}, {day(0), day(3)}, {day(3), now.AddDate(0, 0, 1).Format(time.DateOnly)}} {
		if _, err := SpanOf(bad[0], bad[1], time.Local); err == nil {
			t.Errorf("from %q to %q was read", bad[0], bad[1])
		}
	}
}
