package provider

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// claudeSpares sets up the owner's case of 11-22: Claude Code, kept to
// its sign-in (switch-only), on pro@x.com and spent, the two saved
// accounts on. /usage answers each directory as used says ("" the account
// signed in; a missing one fails). It answers the spares in saved order
// and how often each directory was read.
func claudeSpares(t *testing.T, used map[string]string) (spares []string, ran func(dir string) int) {
	t.Helper()
	claudeThree(t) // pro@x.com signed in; team and max saved
	var text atomic.Value
	text.Store("Current session: 100% used")
	fakeClaudeUsage(t, &text, nil)
	forget := func() {
		lastQuotas.Lock()
		lastQuotas.m, lastQuotas.loaded = nil, false
		lastQuotas.Unlock()
		loginUsageCache.Lock()
		loginUsageCache.m = nil
		loginUsageCache.Unlock()
	}
	forget()
	t.Cleanup(forget)
	if err := SetSwitchOnly("claude", true); err != nil {
		t.Fatal(err)
	}
	for _, l := range Logins("claude") {
		if !l.Active {
			if err := SetLoginOn("claude", l.User, true); err != nil {
				t.Fatal(err)
			}
			spares = append(spares, l.User)
		}
	}
	if len(spares) != 2 {
		t.Fatalf("spares: %v", spares)
	}
	var mu sync.Mutex
	runs := map[string]int{}
	UsageClaudeVia(func(_ context.Context, dir string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		runs[dir]++
		u, ok := used[dir]
		if !ok {
			return "", errors.New("Claude Code: network is unreachable")
		}
		if strings.HasPrefix(u, "Claude Code:") {
			return "", errors.New(u)
		}
		return "Current session: " + u + "% used · resets " + sessionReset(), nil
	})
	return spares, func(dir string) int { mu.Lock(); defer mu.Unlock(); return runs[dir] }
}

// keptReading puts in quotas.json what an earlier magpie read of the
// Claude account user, days ago: 0% of its 5 hours and 10% of its week,
// both windows' resets passed since, as the owner's spares' were (11-10
// to 11-16).
func keptReading(t *testing.T, user string, ago time.Duration) {
	t.Helper()
	at := time.Now().Add(-ago)
	r5, r7 := at.Add(2*time.Hour), at.Add(24*time.Hour)
	c := &lastQuotas
	c.Lock()
	defer c.Unlock()
	c.load()
	if c.m == nil {
		c.m = map[string]lastQuota{}
	}
	c.m["claude/"+strings.ToLower(user)] = lastQuota{At: at,
		Q:     SubscriptionQuota{Provider: "claude", User: user, Windows: []QuotaWindow{{Name: "5 hours", Used: 0, ResetsAt: &r5}, {Name: "7 days", Used: 10, ResetsAt: &r7}}},
		Spans: []lastWindow{{Span: 5 * time.Hour}, {Span: 7 * 24 * time.Hour}}}
}

// A spare whose reading is days old is read again before it is chosen:
// spent now, the next one goes. Kept as read, every reset passed made it
// read as unused, and Claude Code was moved onto an account that was out.
func TestClaudeStaleSpareReadBeforeChosen(t *testing.T) {
	used := map[string]string{"": "100"}
	spares, ran := claudeSpares(t, used)
	used[claudeAccountDir(spares[0])] = "100"
	used[claudeAccountDir(spares[1])] = "10"
	keptReading(t, spares[0], 6*24*time.Hour)
	from, to, back, ok := NextLogin(context.Background(), "claude")
	if !ok || back || to != spares[1] || !strings.HasPrefix(from, "pro@x.com") {
		t.Fatalf("got %q→%q back=%v ok=%v, want on to %q", from, to, back, ok, spares[1])
	}
	if n := ran(claudeAccountDir(spares[0])); n != 1 {
		t.Fatalf("the stale spare's /usage ran %d times, want 1", n)
	}
}

// A spare still too old to go by after its read failed goes after every
// spare read freshly, and is taken only when none of those has room.
func TestClaudeStaleSpareRankedLast(t *testing.T) {
	t.Run("fresh first", func(t *testing.T) {
		used := map[string]string{"": "100"}
		spares, _ := claudeSpares(t, used)
		used[claudeAccountDir(spares[1])] = "97" // past the line, but read now
		keptReading(t, spares[0], 6*24*time.Hour)
		if _, to, _, ok := NextLogin(context.Background(), "claude"); !ok || to != spares[1] {
			t.Fatalf("to %q ok=%v, want %q", to, ok, spares[1])
		}
	})
	t.Run("stale last", func(t *testing.T) {
		used := map[string]string{"": "100"}
		spares, _ := claudeSpares(t, used)
		keptReading(t, spares[0], 6*24*time.Hour) // spares[1] can't be read either
		if _, to, _, ok := NextLogin(context.Background(), "claude"); !ok || to != spares[0] {
			t.Fatalf("to %q ok=%v, want %q", to, ok, spares[0])
		}
	})
}

// A spare Claude Code said hit its limit (the owner's warm-up line of
// 11-22) is passed over till the reset it named, whatever its kept
// reading says, after a restart too: it is kept on disk.
func TestClaudeSaidOutSpareSkipped(t *testing.T) {
	used := map[string]string{"": "100"}
	spares, ran := claudeSpares(t, used)
	used[claudeAccountDir(spares[1])] = "10"
	keptReading(t, spares[0], time.Hour) // fresh enough, and reads unused
	noteClaudeSaid(spares[0], "Claude Code: You've hit your weekly limit · resets Nov 24 at 5am (Asia/Shanghai)", time.Now())
	if _, to, _, ok := NextLogin(context.Background(), "claude"); !ok || to != spares[1] {
		t.Fatalf("to %q ok=%v, want %q", to, ok, spares[1])
	}
	if n := ran(claudeAccountDir(spares[0])); n != 0 {
		t.Fatalf("the spare out till its reset was read %d times", n)
	}
	// its reset come: chosen again by its reading
	m, _ := readClaudeSaid()
	s := m[strings.ToLower(spares[0])]
	s.Until = time.Now().Add(-time.Minute)
	m[strings.ToLower(spares[0])] = s
	writeClaudeSaid(m)
	loginUsageCache.Lock()
	loginUsageCache.m = nil
	loginUsageCache.Unlock()
	if _, to, _, ok := NextLogin(context.Background(), "claude"); !ok || to != spares[0] {
		t.Fatalf("after its reset: to %q ok=%v, want %q", to, ok, spares[0])
	}
}

// A spare whose own /usage read says it hit its limit is out till the
// reset named, and the next spare goes.
func TestClaudeSpareReadSaysOut(t *testing.T) {
	used := map[string]string{"": "100"}
	spares, _ := claudeSpares(t, used)
	used[claudeAccountDir(spares[0])] = "Claude Code: You've hit your session limit · resets 11pm (UTC)"
	used[claudeAccountDir(spares[1])] = "10"
	if _, to, _, ok := NextLogin(context.Background(), "claude"); !ok || to != spares[1] {
		t.Fatalf("to %q ok=%v, want %q", to, ok, spares[1])
	}
	if !claudeSaidOut(spares[0], time.Now()) {
		t.Fatal("the spare's read said it was out, and nothing kept it")
	}
}

func TestClaudeSaidUntil(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 11, 22, 21, 41, 2, 0, sh)
	for _, c := range []struct {
		said string
		want time.Time
	}{
		{"Claude Code: You've hit your weekly limit · resets Nov 24 at 5am (Asia/Shanghai)", time.Date(2026, 11, 24, 5, 0, 0, 0, sh)},
		{"You've hit your session limit · resets 11pm (Asia/Shanghai)", time.Date(2026, 11, 22, 23, 0, 0, 0, sh)},
		{"You've hit your session limit · resets 3:30am (Asia/Shanghai).", time.Date(2026, 11, 23, 3, 30, 0, 0, sh)},
		{"You've hit your Fable limit for this week. Switch models?", now.Add(claudeSaidUnknownFor)},
		// a session limit whose reset reads further than its 5 hours
		{"You've hit your session limit · resets Nov 30 at 5am (Asia/Shanghai)", now.Add(claudeSaidUnknownFor)},
	} {
		if got := claudeSaidUntil(c.said, now); !got.Equal(c.want) {
			t.Errorf("%q: %v, want %v", c.said, got, c.want)
		}
	}
}

func TestClaudeSaidKeepsOnlyLimits(t *testing.T) {
	claudeThree(t)
	now := time.Now()
	for _, s := range []string{"Claude Code: HTTP 401 Unauthorized", "signal: killed", "Claude Code: API Error: 429 (not your usage limit)"} {
		noteClaudeSaid("a@x.com", s, now)
		if claudeSaidOut("a@x.com", now) {
			t.Fatalf("%q kept the account out", s)
		}
	}
	noteClaudeSaid("a@x.com", "You've hit your limit · resets 5pm (UTC)", now)
	if !claudeSaidOut("A@x.com", now) {
		t.Fatal("a limit said isn't kept")
	}
	clearClaudeSaid("a@x.com", nil)
	if claudeSaidOut("a@x.com", now) {
		t.Fatal("a good read since didn't clear it")
	}
}

func TestClaudeStale(t *testing.T) {
	now := time.Now()
	ws := []QuotaWindow{{Span: 7 * 24 * time.Hour}, {Span: 5 * time.Hour}}
	if claudeStale(ws, now.Add(-4*time.Hour), now) || !claudeStale(ws, now.Add(-6*time.Hour), now) {
		t.Fatal("the shortest window decides")
	}
	if claudeStale(nil, now.Add(-4*time.Hour), now) || !claudeStale(nil, now.Add(-6*time.Hour), now) {
		t.Fatal("no spans: 5 hours")
	}
	if claudeStale(ws, time.Time{}, now) {
		t.Fatal("no time read is not stale")
	}
}

// A spare read by this magpie, longer ago than its 5 hours, is read
// again on the switch's next look: once read, it was never read again,
// and its reading only grew older.
func TestClaudeSpareReadAgainWhenOld(t *testing.T) {
	used := map[string]string{"": "100"}
	spares, ran := claudeSpares(t, used)
	used[claudeAccountDir(spares[0])] = "10"
	if _, to, _, ok := NextLogin(context.Background(), "claude"); !ok || to != spares[0] {
		t.Fatalf("to %q ok=%v, want %q", to, ok, spares[0])
	}
	look := func(d time.Duration) {
		loginUsageCache.Lock()
		loginUsageCache.m = nil
		loginUsageCache.Unlock()
		claudeUsage.Lock()
		for k, e := range claudeUsage.m {
			e.tried, e.at = e.tried.Add(-d), e.at.Add(-d)
			claudeUsage.m[k] = e
		}
		claudeUsage.Unlock()
		NextLogin(context.Background(), "claude")
	}
	look(time.Hour)
	if n := ran(claudeAccountDir(spares[0])); n != 1 {
		t.Fatalf("read an hour on: %d runs, want 1", n)
	}
	used[claudeAccountDir(spares[0])] = "100"
	used[claudeAccountDir(spares[1])] = "10"
	look(5 * time.Hour)
	if n := ran(claudeAccountDir(spares[0])); n != 2 {
		t.Fatalf("read six hours on: %d runs, want 2", n)
	}
}
