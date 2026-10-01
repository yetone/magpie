package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClaudeUsage has Claude Code's /usage print out (or fail with err),
// counting its runs, and fails the test on any request magpie makes to
// Anthropic itself.
func fakeClaudeUsage(t *testing.T, out *atomic.Value, fail *atomic.Bool) *atomic.Int32 {
	t.Helper()
	var runs atomic.Int32
	old, oldAsked := claudeCLIUsage, claudeAsked.Load()
	UsageClaudeVia(func(context.Context) (string, error) {
		runs.Add(1)
		if fail != nil && fail.Load() {
			return "", errors.New("Claude Code: offline")
		}
		return out.Load().(string), nil
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("magpie asked Anthropic itself: %s", r.URL)
	}))
	oldBase, oldWait, oldUsed := claudeBase, claudeUsageWait, claudeUsedSince
	claudeBase = srv.URL
	claudeUsageWait = func() time.Duration { return usageTestWait }
	claudeUsedSince = func(time.Time) bool { return true }
	claudeAsked.Store(0)
	claudeUsage.Lock()
	claudeUsage.m = nil
	claudeUsage.Unlock()
	t.Cleanup(func() {
		claudeCLIUsage, claudeBase, claudeUsageWait, claudeUsedSince = old, oldBase, oldWait, oldUsed
		claudeAsked.Store(oldAsked)
		srv.Close()
		claudeUsage.Lock()
		claudeUsage.m = nil
		claudeUsage.Unlock()
		subscriptionUsageCache.Lock()
		subscriptionUsageCache.asked = false
		subscriptionUsageCache.Unlock()
	})
	return &runs
}

// Claude's usage is Claude Code's /usage, run only when the user asks,
// once an ask, and only for the account Claude Code is signed in to.
func TestClaudeWindowsAsked(t *testing.T) {
	var out atomic.Value
	out.Store("Current session: 40% used · resets " + soon(1) + " at 3:30pm (UTC)\nCurrent week (all models): 10% used · resets " + soon(3) + " at 2pm (UTC)\n")
	var fail atomic.Bool
	runs := fakeClaudeUsage(t, &out, &fail)
	ctx := context.Background()

	// a saved account is never read, asked or not
	if _, err := claudeWindows(ctx, "saved@x", false); err != errClaudeSaved || runs.Load() != 0 {
		t.Fatalf("saved unasked: %v %d", err, runs.Load())
	}
	// a saved account is never read, asked or not
	AskClaudeUsage()
	if _, err := claudeWindows(ctx, "saved@x", false); err != errClaudeSaved || runs.Load() != 0 {
		t.Fatalf("saved: %v %d", err, runs.Load())
	}

	// asked: one run, then what it told
	ws, err := claudeWindows(ctx, "A@x", true)
	if err != nil || len(ws) != 2 || ws[0].Used != 40 || ws[1].Used != 10 || runs.Load() != 1 {
		t.Fatalf("asked: %v %+v %d", err, ws, runs.Load())
	}
	for range 3 {
		if ws, err = claudeWindows(ctx, "a@x", true); err != nil || len(ws) != 2 || runs.Load() != 1 {
			t.Fatalf("kept: %v %d", err, runs.Load())
		}
	}
	// asked again at once: still the one run
	AskClaudeUsage()
	if _, err = claudeWindows(ctx, "a@x", true); err != nil || runs.Load() != 1 {
		t.Fatalf("too soon: %v %d", err, runs.Load())
	}

	// its wait on, unasked: run again by itself, once
	age := func(d time.Duration) {
		claudeUsage.Lock()
		e := claudeUsage.m["a@x"]
		e.tried = e.tried.Add(-d)
		claudeUsage.m["a@x"] = e
		claudeUsage.Unlock()
	}
	claudeAsked.Store(0) // the ask above was answered by the run before it
	age(usageTestWait - time.Minute)
	if _, err = claudeWindows(ctx, "a@x", true); err != nil || runs.Load() != 1 {
		t.Fatalf("ran before its wait: %v %d", err, runs.Load())
	}
	age(time.Minute)
	out.Store("Current session: 55% used · resets " + soon(1) + " at 3:30pm (UTC)\n")
	for range 3 {
		if ws, err = claudeWindows(ctx, "a@x", true); err != nil || len(ws) != 1 || ws[0].Used != 55 || runs.Load() != 2 {
			t.Fatalf("every: %v %+v %d", err, ws, runs.Load())
		}
	}

	// a failed run says why, and isn't run again until asked or its wait is up
	claudeUsage.Lock()
	claudeUsage.m["b@x"] = claudeUsageEntry{tried: time.Now().Add(-claudeUsageWaitMax)}
	claudeUsage.Unlock()
	fail.Store(true)
	if _, err = claudeWindows(ctx, "b@x", true); err == nil || runs.Load() != 3 {
		t.Fatalf("failed: %v %d", err, runs.Load())
	}
	if _, err = claudeWindows(ctx, "b@x", true); err == nil || err == errClaudeNotAsked || runs.Load() != 3 {
		t.Fatalf("after a failure: %v %d", err, runs.Load())
	}
}

func TestParseClaudeUsage(t *testing.T) {
	now := time.Date(2026, 10, 1, 4, 0, 0, 0, time.UTC)
	text := `You are currently using your subscription to power your Claude Code usage

Current session: 13% used · resets Oct 1 at 3:30pm (Asia/Shanghai)
Current week (all models): 4% used · resets Oct 3 at 2pm (Asia/Shanghai)
Current week (Opus): 12.5% used · resets Jan 2 at 2pm (Asia/Shanghai)
Current week (Fable): 0% used

What's contributing to your limits usage?
  96% of your usage came from sessions active for 8+ hours`
	ws, err := parseClaudeUsage(text, now)
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := time.LoadLocation("Asia/Shanghai")
	want := []struct {
		name, model string
		used        float64
		span        time.Duration
		reset       time.Time
	}{
		{"5 hours", "", 13, 5 * time.Hour, time.Date(2026, 10, 1, 15, 30, 0, 0, sh)},
		{"7 days", "", 4, 7 * 24 * time.Hour, time.Date(2026, 10, 3, 14, 0, 0, 0, sh)},
		{"7 days · Opus", "opus", 12.5, 7 * 24 * time.Hour, time.Date(2027, 1, 2, 14, 0, 0, 0, sh)},
		{"7 days · Fable", "fable", 0, 7 * 24 * time.Hour, time.Time{}},
	}
	if len(ws) != len(want) {
		t.Fatalf("windows %+v", ws)
	}
	for i, w := range want {
		g := ws[i]
		if g.Name != w.name || g.Model != w.model || g.Used != w.used || g.Span != w.span ||
			(w.reset.IsZero() != (g.ResetsAt == nil)) || (g.ResetsAt != nil && !g.ResetsAt.Equal(w.reset)) {
			t.Errorf("%d: %+v, want %+v", i, g, w)
		}
	}
	// a time alone is the next one
	if r, ok := claudeResetTime("3am (UTC)", now); !ok || !r.Equal(time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)) {
		t.Fatalf("time alone: %v %v", r, ok)
	}
	if _, err := parseClaudeUsage("Error: not logged in", now); err == nil {
		t.Fatal("nothing told, no error")
	}
}

// usageTestWait is the wait between unasked runs of /usage in tests.
const usageTestWait = 7 * time.Minute

// An unasked run of /usage waits a whole number of minutes from 5 to 15,
// drawn afresh each time, so it isn't run on a clock.
func TestClaudeWaitRandom(t *testing.T) {
	seen := map[time.Duration]bool{}
	for range 2000 {
		w := claudeUsageWait()
		if w < claudeUsageWaitMin || w > claudeUsageWaitMax || w%time.Minute != 0 {
			t.Fatalf("wait %v", w)
		}
		seen[w] = true
	}
	if len(seen) != 11 {
		t.Fatalf("waits drawn: %v", seen)
	}
}

// Unasked, /usage is run again only once Claude Code has been used since
// it last was; asked, it is run whether it was or not.
func TestClaudeUsageIdle(t *testing.T) {
	var out atomic.Value
	out.Store("Current session: 40% used · resets " + soon(1) + " at 3:30pm (UTC)\n")
	runs := fakeClaudeUsage(t, &out, nil)
	var used atomic.Bool
	claudeUsedSince = func(time.Time) bool { return used.Load() }
	ctx := context.Background()

	AskClaudeUsage()
	if _, err := claudeWindows(ctx, "a@x", true); err != nil || runs.Load() != 1 {
		t.Fatalf("asked: %v %d", err, runs.Load())
	}
	claudeAsked.Store(0)
	claudeUsage.Lock()
	e := claudeUsage.m["a@x"]
	e.tried = e.tried.Add(-usageTestWait)
	claudeUsage.m["a@x"] = e
	claudeUsage.Unlock()

	// its wait is up, but nobody used Claude Code
	if ws, err := claudeWindows(ctx, "a@x", true); err != nil || len(ws) != 1 || runs.Load() != 1 {
		t.Fatalf("idle: %v %d", err, runs.Load())
	}
	// asked, it runs all the same
	AskClaudeUsage()
	if _, err := claudeWindows(ctx, "a@x", true); err != nil || runs.Load() != 2 {
		t.Fatalf("asked while idle: %v %d", err, runs.Load())
	}
	claudeAsked.Store(0)
	claudeUsage.Lock()
	e = claudeUsage.m["a@x"]
	e.tried = e.tried.Add(-usageTestWait)
	claudeUsage.m["a@x"] = e
	claudeUsage.Unlock()
	// used since: it runs by itself again
	used.Store(true)
	if _, err := claudeWindows(ctx, "a@x", true); err != nil || runs.Load() != 3 {
		t.Fatalf("used: %v %d", err, runs.Load())
	}
}

// Claude Code was used since a time when one of its sessions was written
// to since then; its other files don't count.
func TestClaudeUsedSince(t *testing.T) {
	claudeHome(t)
	project := filepath.Join(filepath.Dir(claudeCredentialsPath()), "projects", "-Users-x-proj")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(project, "s.jsonl")
	os.WriteFile(session, []byte("{}\n"), 0o600)
	os.WriteFile(filepath.Join(project, "notes.txt"), nil, 0o600)
	now := time.Now()
	old := now.Add(-time.Hour)
	os.Chtimes(session, old, old)
	if claudeUsedSince(now.Add(-time.Minute)) {
		t.Fatal("used, with no session written to")
	}
	if !claudeUsedSince(now.Add(-2 * time.Hour)) {
		t.Fatal("not used, with a session written to")
	}
	os.Chtimes(session, now, now)
	if !claudeUsedSince(now.Add(-time.Minute)) {
		t.Fatal("not used, with a session just written to")
	}
}

// soon is the day n days from now as /usage writes it ("Oct 3"), so a
// window the tests read hasn't reset whatever day they run.
func soon(n int) string { return time.Now().UTC().AddDate(0, 0, n).Format("Jan 2") }
