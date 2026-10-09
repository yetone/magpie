package provider

import (
	"testing"
	"time"
)

// resetUsed forgets the allowances routing last read, now and after t.
func resetUsed(t *testing.T) {
	t.Helper()
	reset := func() {
		usedCache.Lock()
		usedCache.m, usedCache.at, usedCache.loading = map[string]map[string]Allowance{}, map[string]time.Time{}, map[string]chan struct{}{}
		usedCache.stale, usedCache.seen = nil, nil
		usedCache.Unlock()
	}
	reset()
	t.Cleanup(func() {
		for {
			usedCache.Lock()
			done := usedCache.loading["codex"]
			usedCache.Unlock()
			if done == nil {
				break
			}
			<-done
		}
		reset()
	})
}

// A reading the Usage page made 50s ago, which routing takes for its own,
// is 50s old already: routing reads again in 10s, not a minute from when it
// took it. Counted from then, an account near its cap was sent on a
// reading up to two minutes old (#1295).
func TestAllowancesCountFromTheReading(t *testing.T) {
	signIn(t)
	rememberLogins(true) // this home's account, not the last test's
	resetUsed(t)
	read := time.Now().Add(-50 * time.Second)
	five := time.Now().Add(2 * time.Hour)
	loginUsageCache.Lock()
	loginUsageCache.m = map[string]loginUsageEntry{"codex/me@example.com": {at: read,
		q: SubscriptionQuota{Provider: "codex", Windows: []QuotaWindow{{Name: "5 hours", Used: 96, ResetsAt: &five, Span: 5 * time.Hour}}}}}
	loginUsageCache.Unlock()
	if u, _ := Allowances("codex")["me@example.com"].For("gpt-5.5", time.Now()); u != 96 {
		t.Fatalf("read %v%%, want the Usage page's 96%%", u)
	}
	usedCache.Lock()
	at := usedCache.at["codex"]
	usedCache.Unlock()
	if d := at.Sub(read); d < -time.Second || d > time.Second {
		t.Fatalf("routing's reading counted from %s, %s after it was made", at.Format(time.StampMilli), d)
	}
}

// What a Codex reply says the account has used is routing's from the next
// turn on: at 96% as last read, a reply at 99% holds the account at its
// 99% cap at once. It takes the window that runs as long, and leaves the
// other window, the other account, and an account not read yet as they
// are.
func TestNoteCodexLimitsHoldsTheCap(t *testing.T) {
	isolate(t)
	resetUsed(t)
	now := time.Now()
	five, week := now.Add(2*time.Hour), now.Add(4*24*time.Hour)
	usedCache.Lock()
	usedCache.m["codex"] = map[string]Allowance{
		"Me@example.com":    {{Used: 96, Resets: five, Span: 5 * time.Hour}, {Used: 40, Resets: week, Span: 7 * 24 * time.Hour}},
		"spare@example.com": {{Used: 20, Resets: five, Span: 5 * time.Hour}},
	}
	usedCache.at["codex"] = now
	usedCache.Unlock()
	loginUsageCache.Lock()
	loginUsageCache.m = map[string]loginUsageEntry{"codex/me@example.com": {at: now.Add(-30 * time.Second),
		q: SubscriptionQuota{Provider: "codex", Windows: []QuotaWindow{
			{Name: "5 hours", Used: 96, ResetsAt: &five, Span: 5 * time.Hour},
			{Name: "7 days", Used: 40, ResetsAt: &week, Span: 7 * 24 * time.Hour}}}}}
	loginUsageCache.Unlock()
	handed := Allowances("codex") // what routing holds already isn't changed under it
	if h := handed["Me@example.com"].CapHeld("gpt-5.5", WindowCaps{All: 99}, now); h != nil {
		t.Fatal("held at 96%")
	}

	renews := now.Add(90 * time.Minute).Truncate(time.Second)
	NoteCodexLimits("codex", "me@example.com", []CodexLimit{{Used: 99, Span: 5 * time.Hour, Resets: renews}})
	NoteCodexLimits("codex", "new@example.com", []CodexLimit{{Used: 99, Span: 5 * time.Hour}})

	m := Allowances("codex")
	h := m["Me@example.com"].CapHeld("gpt-5.5", WindowCaps{All: 99}, now)
	if h == nil || h.Used != 99 || !h.Back.Equal(renews) {
		t.Fatalf("after the reply: held %+v", h)
	}
	if u := m["Me@example.com"][1].Used; u != 40 {
		t.Fatalf("the week took the five hours' share: %v%%", u)
	}
	if u := m["spare@example.com"][0].Used; u != 20 {
		t.Fatalf("the other account: %v%%", u)
	}
	if _, ok := m["new@example.com"]; ok {
		t.Fatal("an account not read yet was made up from a reply")
	}
	if u := handed["Me@example.com"][0].Used; u != 96 {
		t.Fatalf("the allowance handed out before was changed under its reader: %v%%", u)
	}
	loginUsageCache.Lock()
	e := loginUsageCache.m["codex/me@example.com"]
	loginUsageCache.Unlock()
	if w := e.q.Windows; w[0].Used != 99 || !w[0].ResetsAt.Equal(renews) || w[1].Used != 40 {
		t.Fatalf("the Usage page's reading: %+v", w)
	}
	if time.Since(e.at) < 29*time.Second {
		t.Fatal("a reply was taken for a whole reading")
	}
}
