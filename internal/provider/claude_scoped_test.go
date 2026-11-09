package provider

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// Claude Code's /usage tells a week's allowance per model (#139).
func TestClaudeWindowsModelScoped(t *testing.T) {
	now := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	ws, err := parseClaudeUsage(`Current session: 2% used · resets Jul 20 at 3am (UTC)
Current week (all models): 88% used · resets Jul 24 at 8pm (UTC)
Current week (Opus): 12% used · resets Jul 24 at 8pm (UTC)
Current week (Fable): 64% used · resets Jul 24 at 8pm (UTC)`, now)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, w := range ws {
		names = append(names, w.Name)
	}
	if len(ws) != 4 || ws[2].Name != "7 days · Opus" || ws[3].Name != "7 days · Fable" {
		t.Fatalf("windows: %q", names)
	}
	f := ws[3]
	reset := time.Date(2026, 7, 24, 20, 0, 0, 0, time.UTC)
	if f.Used != 64 || f.Model != "fable" || f.Span != 7*24*time.Hour || f.ResetsAt == nil || !f.ResetsAt.Equal(reset) {
		t.Fatalf("Fable: %+v", f)
	}

	// Fable used up stops Fable, not the other models
	ws[3].Used = 100
	a := allowanceOf(ws, now)
	if used, _ := a.For("claude-fable-5-1", now); used != 100 {
		t.Fatalf("fable: %v", used)
	}
	if used, _ := a.For("claude-opus-5-5", now); used != 88 {
		t.Fatalf("opus: %v", used)
	}
	if a.Full("claude-sonnet-5", 100, now) != (time.Time{}) || !a.Full("claude-fable-5", 100, now).Equal(reset) {
		t.Fatal("Full")
	}
}

// Claude Code 2.1.321's /usage on a Team account the day Fable 5.2
// launched, word for word: its one Fable week holds Fable 5.2 too.
func TestClaudeFable52CountsInTheFableWeek(t *testing.T) {
	now := time.Date(2026, 11, 9, 21, 0, 0, 0, time.UTC)
	ws, err := parseClaudeUsage(`You are currently using your subscription to power your Claude Code usage

Current session: 0% used · resets Nov 10 at 5:40am (Asia/Shanghai)
Current week (all models): 0% used · resets Nov 11 at 6am (Asia/Shanghai)
Current week (Fable): 0% used · resets Nov 11 at 6am (Asia/Shanghai)`, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 3 || ws[2].Name != "7 days · Fable" || ws[2].Model != "fable" {
		t.Fatalf("windows: %+v", ws)
	}
	ws[2].Used = 100
	a := allowanceOf(ws, now)
	for _, m := range []string{"claude-fable-5-2", "claude-fable-5-1"} {
		if used, _ := a.For(m, now); used != 100 {
			t.Fatalf("%s: %v", m, used)
		}
	}
	if used, _ := a.For("claude-opus-5-5", now); used != 0 {
		t.Fatalf("opus: %v", used)
	}
}

func TestClaudeScopeModel(t *testing.T) {
	for in, want := range map[string]string{"Fable": "fable", "Fable 5.1": "fable-5-1", "": ""} {
		if got := claudeScopeModel(in); got != want {
			t.Fatalf("%q: %q", in, got)
		}
	}
}

// Claude Code's seven_day_overage_included is the Fable week, in place of
// the one the usage endpoint told.
func TestNoteClaudeLimitsFable(t *testing.T) {
	var out atomic.Value
	out.Store("")
	fakeClaudeUsage(t, &out, nil)
	claudeUsage.Lock()
	claudeUsage.m = map[string]claudeUsageEntry{"kev@example.com": {at: time.Now().Add(-time.Hour), ws: []QuotaWindow{
		{Name: "5 hours", Used: 5}, {Name: "7 days · Fable", Used: 30, Model: "fable"},
	}}}
	claudeUsage.Unlock()
	defer func() { claudeUsage.Lock(); claudeUsage.m = nil; claudeUsage.Unlock() }()

	NoteClaudeLimits("kev@example.com", []ClaudeLimit{{Kind: "seven_day_overage_included", Used: 1}})
	ws, err := claudeWindows(context.Background(), "kev@example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 2 || ws[1].Name != "7 days · Fable" || ws[1].Used != 100 || ws[1].Model != "fable" || ws[1].Span != 7*24*time.Hour {
		t.Fatalf("windows: %+v", ws)
	}
}

// StringKe (#4532): Claude Code 2.1.316 names the Sonnet week "Current
// week (Sonnet only)", and it was read as a window of models named
// "sonnet-only", which no model is: the Sonnet week used up stopped
// nothing, and it was a row apart from the seven_day_sonnet header's.
// It is the Sonnet week, as the header's.
func TestClaudeUsageSonnetOnly(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	ws, err := parseClaudeUsage(`Current session: 2% used · resets 3am (UTC)
Current week (all models): 30% used · resets Nov 5 at 8pm (UTC)
Current week (Sonnet only): 100% used · resets Nov 5 at 8pm (UTC)`, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 3 || ws[2].Name != "7 days · Sonnet" || ws[2].Model != "sonnet" || ws[2].Used != 100 {
		t.Fatalf("windows: %+v", ws)
	}
	a := allowanceOf(ws, now)
	if used, _ := a.For("claude-sonnet-5", now); used != 100 {
		t.Fatalf("sonnet: %v", used)
	}
	if used, _ := a.For("claude-opus-5-5", now); used != 30 {
		t.Fatalf("opus: %v", used)
	}
	// the header's Sonnet week takes its place, not a row beside it
	var out atomic.Value
	out.Store("")
	fakeClaudeUsage(t, &out, nil)
	claudeUsage.Lock()
	claudeUsage.m = map[string]claudeUsageEntry{"s@example.com": {at: time.Now().Add(-time.Hour), ws: ws}}
	claudeUsage.Unlock()
	defer func() { claudeUsage.Lock(); claudeUsage.m = nil; claudeUsage.Unlock() }()
	NoteClaudeLimits("s@example.com", []ClaudeLimit{{Kind: "seven_day_sonnet", Used: .5}})
	claudeUsage.Lock()
	got := claudeUsage.m["s@example.com"].ws
	claudeUsage.Unlock()
	n := 0
	for _, w := range got {
		if w.Model == "sonnet" || w.Model == "sonnet-only" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("Sonnet weeks: %+v", got)
	}
}
