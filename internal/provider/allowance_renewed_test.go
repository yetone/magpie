package provider

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// toldRenewed registers an OnRenewed hook for the test alone and gives
// what it was told so far, as agent/user.
func toldRenewed(t *testing.T) func() []string {
	t.Helper()
	renewedHooks.Lock()
	hooks := renewedHooks.fs
	renewedHooks.Unlock()
	t.Cleanup(func() {
		renewedHooks.Lock()
		renewedHooks.fs = hooks
		renewedHooks.Unlock()
	})
	var mu sync.Mutex
	var told []string
	OnRenewed(func(agent, user string) {
		mu.Lock()
		told = append(told, agent+"/"+user)
		mu.Unlock()
	})
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), told...)
	}
}

// startWithNoAllowancesRead starts the test with no allowance read, and leaves
// none it read to the next.
func startWithNoAllowancesRead(t *testing.T) {
	t.Helper()
	clear := func() {
		usedCache.Lock()
		usedCache.m, usedCache.at, usedCache.loading = map[string]map[string]Allowance{}, map[string]time.Time{}, map[string]chan struct{}{}
		usedCache.renewed, usedCache.seen = nil, nil
		usedCache.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

// readAllowancesAgain has Allowances read agent's accounts again, as it does a
// minute after the last reading, and waits for it, its hooks told.
func readAllowancesAgain(t *testing.T, agent string) map[string]Allowance {
	t.Helper()
	usedCache.Lock()
	usedCache.at[agent] = time.Time{}
	usedCache.Unlock()
	Allowances(agent)
	usedCache.Lock()
	done := usedCache.loading[agent]
	usedCache.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("the reading never came back")
		}
	}
	usedCache.Lock()
	defer usedCache.Unlock()
	return usedCache.m[agent]
}

// A Claude account out of a window is told renewed (OnRenewed, as
// "claude", the agent its usage is read under, and the account) once a
// reading routing goes by finds that window started again: a new /usage,
// or its reset passed in what Claude Code last told of it. Not on its
// first reading, nor while the window stays full, nor twice for one
// renewal.
func TestClaudeAccountToldRenewed(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{"oauthAccount": map[string]any{"emailAddress": "a@example.com", "accountUuid": "u-a"}})
	rememberLogins(true)
	var user string
	for _, l := range Logins("claude") {
		if l.Active {
			user = l.User
		}
	}
	if user == "" {
		t.Fatalf("no Claude account signed in: %+v", Logins("claude"))
	}
	key := strings.ToLower(user)
	var out atomic.Value
	runs := fakeClaudeUsage(t, &out, nil)
	startWithNoAllowancesRead(t)
	told := toldRenewed(t)
	tellsSo := func(what string, n int) {
		t.Helper()
		got := told()
		if len(got) != n {
			t.Fatalf("%s: told %v", what, got)
		}
		for _, s := range got {
			if s != "claude/"+user {
				t.Fatalf("%s: told %v", what, got)
			}
		}
	}
	// usage is what Claude Code's /usage prints, run as the user asked
	usage := func(text string) map[string]Allowance {
		t.Helper()
		out.Store(text)
		claudeUsage.Lock()
		if e, ok := claudeUsage.m[key]; ok {
			e.tried = e.tried.Add(-time.Minute) // past the least time between two runs
			claudeUsage.m[key] = e
		}
		claudeUsage.Unlock()
		n := runs.Load()
		AskClaudeUsage()
		m := readAllowancesAgain(t, "claude")
		if runs.Load() != n+1 {
			t.Fatalf("/usage ran %d times", runs.Load()-n)
		}
		return m
	}
	// a session's reset as /usage prints it, within its five hours
	session := func(in time.Duration) string {
		return time.Now().UTC().Add(in).Format("Jan 2 at 3:04pm") + " (UTC)"
	}
	full := "Current session: 100% used · resets " + session(3*time.Hour) + "\nCurrent week (all models): 41% used · resets " + soon(3) + " at 2pm (UTC)\n"
	if m := usage(full); len(m[user]) != 2 || m[user][0].Used != 100 {
		t.Fatalf("routing reads %+v", m)
	}
	tellsSo("its first reading, out of its five hours", 0)
	usage(full)
	tellsSo("its five hours still used up", 0)
	usage(strings.Replace(full, "Current session: 100% used", "Current session: 99% used", 1))
	tellsSo("at 99%, still used up as Smart counts it", 0)
	usage("Current session: 3% used · resets " + session(4*time.Hour+30*time.Minute) + "\nCurrent week (all models): 42% used · resets " + soon(3) + " at 2pm (UTC)\n")
	tellsSo("its five hours started again", 1)
	usage("Current session: 9% used · resets " + session(4*time.Hour+30*time.Minute) + "\nCurrent week (all models): 42% used · resets " + soon(3) + " at 2pm (UTC)\n")
	tellsSo("used a little more", 1)

	// what Claude Code told as it answered: refused, out of its five
	// hours till back; then back is gone by, with nothing more told of it
	kept := func(back time.Time) {
		claudeUsage.Lock()
		e := claudeUsage.m[key]
		e.ws = []QuotaWindow{{Name: "5 hours", Used: 100, Span: 5 * time.Hour, ResetsAt: &back}}
		claudeUsage.m[key] = e
		claudeUsage.Unlock()
		StaleAllowance("claude", user)
	}
	kept(time.Now().Add(time.Hour))
	readAllowancesAgain(t, "claude")
	tellsSo("refused, out of its five hours", 1)
	kept(time.Now().Add(-time.Second)) // as an hour on
	if m := readAllowancesAgain(t, "claude"); len(m[user]) != 1 || m[user][0].Used != 0 {
		t.Fatalf("its reset passed, routing reads %+v", m)
	}
	tellsSo("its reset passed", 2)
	StaleAllowance("claude", user)
	readAllowancesAgain(t, "claude")
	tellsSo("read again", 2)
	if n := runs.Load(); n != 5 {
		t.Fatalf("/usage ran %d times, unasked", n)
	}
}

// A plugin's account is told renewed as a built-in's is, by the agent its
// usage is read under ("plugin:<id>"). A reading that fails is not one
// that finds the window renewed: what was read before stands for the
// next. An account whose reset was spent (renewedNow) was told then, and
// the reading after tells it no more.
func TestPluginAccountToldRenewed(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	startWithNoAllowancesRead(t)
	told := toldRenewed(t)
	const agent = "plugin:fakeco"
	week := time.Now().Add(50 * time.Hour).Truncate(time.Second)
	var mu sync.Mutex
	reading := map[string]SubscriptionQuota{}
	read := func(used map[string]float64, failed ...string) {
		t.Helper()
		mu.Lock()
		reading = map[string]SubscriptionQuota{}
		for u, n := range used {
			reading[u] = SubscriptionQuota{Provider: agent, Windows: []QuotaWindow{
				{Name: "Weekly", Used: n, Span: 7 * 24 * time.Hour, ResetsAt: &week},
				{Name: "Premium", Used: 100, Span: 7 * 24 * time.Hour, ResetsAt: &week, Model: "premium"},
			}}
		}
		for _, u := range failed {
			reading[u] = SubscriptionQuota{Provider: agent, Windows: []QuotaWindow{}, Error: "502 Bad Gateway"}
		}
		mu.Unlock()
		readAllowancesAgain(t, agent)
	}
	LoginUsageVia(func(_ context.Context, a string) map[string]SubscriptionQuota {
		mu.Lock()
		defer mu.Unlock()
		if a != agent {
			return map[string]SubscriptionQuota{}
		}
		return reading
	})
	t.Cleanup(func() { LoginUsageVia(nil) })
	tellsSo := func(what string, want ...string) {
		t.Helper()
		if got := told(); strings.Join(got, " ") != strings.Join(want, " ") {
			t.Fatalf("%s: told %v, not %v", what, got, want)
		}
	}
	read(map[string]float64{"full@fake": 100, "spare@fake": 20})
	tellsSo("the first reading")
	read(map[string]float64{"spare@fake": 25}, "full@fake")
	tellsSo("full@fake's reading failed")
	read(map[string]float64{"full@fake": 100, "spare@fake": 30})
	tellsSo("still out of its week")
	read(map[string]float64{"full@fake": 12, "spare@fake": 30})
	tellsSo("its week started again", agent+"/full@fake")
	read(map[string]float64{"full@fake": 12, "spare@fake": 30})
	tellsSo("read alike", agent+"/full@fake")

	// out again, then a reset spent on it: told by the reset alone
	read(map[string]float64{"full@fake": 100, "spare@fake": 30})
	renewedNow(agent, "Full@Fake")
	read(map[string]float64{"full@fake": 0, "spare@fake": 30})
	tellsSo("a reset spent", agent+"/full@fake", agent+"/Full@Fake")

	// out of its week again, then read with its reset gone by, the vendor
	// still counting it full: told once, and not again read alike
	read(map[string]float64{"full@fake": 100, "spare@fake": 30})
	passed := time.Now().Add(-time.Second).Truncate(time.Second)
	mu.Lock()
	reading = map[string]SubscriptionQuota{
		"full@fake":  {Provider: agent, Windows: []QuotaWindow{{Name: "Weekly", Used: 100, Span: 7 * 24 * time.Hour, ResetsAt: &passed}}},
		"spare@fake": {Provider: agent, Windows: []QuotaWindow{{Name: "Weekly", Used: 30, Span: 7 * 24 * time.Hour, ResetsAt: &week}}},
	}
	mu.Unlock()
	readAllowancesAgain(t, agent)
	tellsSo("its reset gone by, still at 100", agent+"/full@fake", agent+"/Full@Fake", agent+"/full@fake")
	readAllowancesAgain(t, agent)
	tellsSo("read alike after its reset", agent+"/full@fake", agent+"/Full@Fake", agent+"/full@fake")
}

// A reading is renewed from the last as a key's is (keyAllowances' keep):
// the account, or a pool of some models in it, full till sooner. One
// window started again while another over the same models is still full
// is not; nor are windows of one name read alike, one not read this time,
// or one full till later.
func TestRenewedFrom(t *testing.T) {
	now := time.Now()
	h := func(n int) time.Time { return now.Add(time.Duration(n) * time.Hour) }
	five := func(used float64, resets time.Time) QuotaWindow {
		return QuotaWindow{Name: "5 hours", Used: used, Span: 5 * time.Hour, ResetsAt: &resets}
	}
	week := func(used float64, resets time.Time) QuotaWindow {
		return QuotaWindow{Name: "7 days", Used: used, Span: 7 * 24 * time.Hour, ResetsAt: &resets}
	}
	opus := func(used float64, resets time.Time) QuotaWindow {
		w := week(used, resets)
		w.Name, w.Model = "7 days (Opus)", "opus"
		return w
	}
	credits := func(used float64) QuotaWindow { return QuotaWindow{Name: "Credits", Used: used} }
	of := func(ws ...QuotaWindow) Allowance { return allowanceOf(ws, now) }
	for _, c := range []struct {
		what     string
		was, now Allowance
		renewed  bool
	}{
		{"its five hours started again", of(five(100, h(1)), week(40, h(50))), of(five(3, h(6)), week(41, h(50))), true},
		{"still full", of(five(100, h(1)), week(40, h(50))), of(five(100, h(1)), week(40, h(50))), false},
		{"its five hours started again in a week used up", of(five(100, h(1)), week(100, h(50))), of(five(0, h(6)), week(100, h(50))), false},
		{"its week started again", of(five(0, h(6)), week(100, h(50))), of(five(0, h(6)), week(2, h(218))), true},
		{"full till sooner", of(week(100, h(50))), of(week(100, h(20))), true},
		{"full till later", of(week(100, h(20))), of(week(100, h(50))), false},
		{"its Opus week started again", of(week(40, h(50)), opus(100, h(50))), of(week(41, h(50)), opus(0, h(218))), true},
		{"its Opus week started again in a week used up", of(week(100, h(50)), opus(100, h(50))), of(week(100, h(50)), opus(0, h(218))), false},
		{"two of one name, one full, read alike", of(credits(100), credits(10)), of(credits(100), credits(10)), false},
		{"full, its reset not known, then not", of(credits(100)), of(credits(5)), true},
		{"its full window not read this time", of(five(100, h(1)), week(40, h(50))), of(week(41, h(50))), false},
		{"a first reading, read alike", of(five(20, h(1))), of(five(25, h(1))), false},
	} {
		if got := c.now.renewedFrom(c.was, now, 98, now); got != c.renewed {
			t.Errorf("%s: renewed %v", c.what, got)
		}
	}
	// read before its reset, read alike after it: started again then
	was := of(five(100, h(1)))
	if !was.renewedFrom(was, now, 98, h(2)) {
		t.Error("its reset gone by since it was read: not renewed")
	}
	if was.renewedFrom(was, h(2), 98, h(3)) {
		t.Error("its reset gone by before either reading: renewed")
	}
}
