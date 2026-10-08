package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

const (
	week      = 7 * 24 * time.Hour
	fiveHours = 5 * time.Hour
)

func win(name string, span time.Duration, used float64, resets time.Time) QuotaWindow {
	w := QuotaWindow{Name: name, Used: used, Span: span}
	if !resets.IsZero() {
		w.ResetsAt = &resets
	}
	return w
}

func TestWarmDue(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	idleNow := win("7 days", week, 0, now.Add(week))
	running := win("7 days", week, 12, now.Add(3*24*time.Hour))
	for _, c := range []struct {
		name string
		p    warmWindow
		seen bool
		cur  QuotaWindow
		want bool
	}{
		{"first seen, unused", warmWindow{}, false, idleNow, true},
		{"first seen, reset not known", warmWindow{}, false, win("7 days", week, 0, time.Time{}), true},
		{"first seen, running", warmWindow{}, false, running, false},
		{"first seen, used a little", warmWindow{}, false, win("7 days", week, 0, now.Add(week-time.Hour)), false},
		{"the same window again", warmWindow{ResetsAt: *running.ResetsAt, Used: 12}, true, running, false},
		{"reset passed, unused since", warmWindow{ResetsAt: now.Add(-time.Minute), Used: 40}, true, idleNow, true},
		{"reset passed, used since", warmWindow{ResetsAt: now.Add(-time.Hour), Used: 0}, true, win("7 days", week, 3, now.Add(week-50*time.Minute)), false},
		{"reset early: use fell to nothing, reset kept", warmWindow{ResetsAt: now.Add(2 * 24 * time.Hour), Used: 70}, true,
			win("7 days", week, 0, now.Add(2*24*time.Hour)), true},
		{"reset early: no window running", warmWindow{ResetsAt: now.Add(2 * 24 * time.Hour), Used: 70}, true, idleNow, true},
		{"an unused window sliding on", warmWindow{ResetsAt: now.Add(week - 5*time.Minute)}, true, idleNow, false},
		{"a stale read past its reset", warmWindow{ResetsAt: now.Add(-time.Minute), Used: 50}, true,
			win("7 days", week, 50, now.Add(-time.Minute)), true},
		{"a failed warm-up", warmWindow{Pending: true, Failed: 1}, true, running, true},
		{"still not started, its retry come", warmWindow{Idle: 1, Retry: now}, true, win("7 days", week, 0, time.Time{}), true},
		{"still not started, before its retry", warmWindow{Idle: 1, Retry: now.Add(time.Minute)}, true, win("7 days", week, 0, time.Time{}), false},
		{"not started before, running now", warmWindow{Idle: 1}, true, running, false},
	} {
		if got := warmDue(c.p, c.seen, asOf(c.cur, now), now); got != c.want {
			t.Errorf("%s: due %v, want %v", c.name, got, c.want)
		}
	}
}

// fakeWarm is an account's windows as the backend tells them, and the
// warm-ups sent.
type fakeWarm struct {
	now  time.Time
	ws   map[string][]QuotaWindow
	errs map[string]error
	sent []string
}

func (f *fakeWarm) warmer(path string) codexWarmer {
	return codexWarmer{path: path, now: func() time.Time { return f.now },
		usage: func(context.Context) map[string]SubscriptionQuota {
			out := map[string]SubscriptionQuota{}
			for u, ws := range f.ws {
				out[u] = SubscriptionQuota{Provider: "codex", User: u, Windows: ws}
			}
			return out
		},
		send: func(_ context.Context, user string) error {
			f.sent = append(f.sent, user)
			return f.errs[user]
		}}
}

func (f *fakeWarm) run(t *testing.T, path, which string) []CodexWarm {
	t.Helper()
	return f.warmer(path).warmNow(context.Background(), which, nil)
}

func TestCodexWarmOncePerReset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex-warmup.json")
	start := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	f := &fakeWarm{now: start, errs: map[string]error{}}
	resets := start.Add(10 * time.Minute)
	f.ws = map[string][]QuotaWindow{
		"a@example.com": {win("5 hours", fiveHours, 30, start.Add(time.Hour)), win("7 days", week, 60, resets)},
		"b@example.com": {win("7 days", week, 10, start.Add(3*24*time.Hour))},
	}
	// running windows are only noted
	if rs := f.run(t, path, "week"); len(rs) != 0 || len(f.sent) != 0 {
		t.Fatalf("warmed running windows: %+v", rs)
	}
	// a's week resets; the next read, cached from before, still has the
	// old window: it has started over all the same
	f.now = resets.Add(time.Minute)
	rs := f.run(t, path, "week")
	if len(rs) != 1 || rs[0].User != "a@example.com" || strings.Join(rs[0].Windows, ",") != "7 days" || rs[0].Err != "" {
		t.Fatalf("after the reset: %+v", rs)
	}
	// once: the read now shows it unused, then the window it started
	f.ws["a@example.com"][1] = win("7 days", week, 0, f.now.Add(week))
	f.now = f.now.Add(5 * time.Minute)
	f.run(t, path, "week")
	f.ws["a@example.com"][1] = win("7 days", week, 0, resets.Add(time.Minute+week))
	f.now = f.now.Add(5 * time.Minute)
	f.run(t, path, "week")
	// nor after a restart, which reads what was kept
	f.now = f.now.Add(5 * time.Minute)
	f.run(t, path, "week")
	if strings.Join(f.sent, ",") != "a@example.com" {
		t.Fatalf("sent %v", f.sent)
	}
	if w := codexWarmedIn(path)["a@example.com"]; !w.Equal(resets.Add(time.Minute)) {
		t.Fatalf("warmed at %v", w)
	}

	// OpenAI resets b early: its use falls to nothing before its reset
	f.ws["b@example.com"][0].Used = 0
	f.now = f.now.Add(5 * time.Minute)
	if rs := f.run(t, path, "week"); len(rs) != 1 || rs[0].User != "b@example.com" {
		t.Fatalf("early reset: %+v", rs)
	}
	f.now = f.now.Add(5 * time.Minute)
	f.run(t, path, "week")
	if strings.Join(f.sent, ",") != "a@example.com,b@example.com" {
		t.Fatalf("sent %v", f.sent)
	}

	// the 5-hour window only when asked for, and both windows one request
	f.sent = nil
	f.ws["a@example.com"][0] = win("5 hours", fiveHours, 0, time.Time{})
	f.now = f.now.Add(5 * time.Minute)
	if rs := f.run(t, path, "week"); len(rs) != 0 {
		t.Fatalf("the 5-hour window, weekly only: %+v", rs)
	}
	f.ws["a@example.com"][1] = win("7 days", week, 0, f.now.Add(week))
	f.ws["b@example.com"][0] = win("7 days", week, 0, f.now.Add(week))
	var hours []string
	for _, r := range f.run(t, path, "all") {
		hours = append(hours, r.User+":"+strings.Join(r.Windows, "+"))
	}
	// a's week is running (warmed above, its reset a week off): only its 5 hours
	if strings.Join(hours, ",") != "a@example.com:5 hours" {
		t.Fatalf("all: %v", hours)
	}
}

func TestCodexWarmFailureRetried(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex-warmup.json")
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	f := &fakeWarm{now: now, errs: map[string]error{"a@example.com": errors.New("503")},
		ws: map[string][]QuotaWindow{"a@example.com": {win("7 days", week, 0, now.Add(week))}}}
	for i := range warmTries + 2 {
		f.now = now.Add(time.Duration(i) * 5 * time.Minute)
		f.ws["a@example.com"][0] = win("7 days", week, 0, f.now.Add(week))
		f.run(t, path, "week")
	}
	if len(f.sent) != warmTries {
		t.Fatalf("tried %d times, want %d", len(f.sent), warmTries)
	}
	if w := codexWarmedIn(path); !w["a@example.com"].IsZero() {
		t.Fatalf("warmed: %v", w)
	}
}

// Codex can report an unused window with a reset a full span away. If a
// successful request leaves that window untouched, it still needs a retry.
func TestCodexUnusedFiveHourWindowRetried(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex-warmup.json")
	start := time.Date(2026, 9, 29, 6, 0, 0, 0, time.UTC)
	f := &fakeWarm{now: start, errs: map[string]error{}}
	f.ws = map[string][]QuotaWindow{"a@example.com": {{Name: "5 hours", Span: fiveHours, ResetSecs: int64(fiveHours.Seconds())}}}
	for i := range 10 {
		f.now = start.Add(time.Duration(i) * 5 * time.Minute)
		// The backend keeps reporting an unstarted window, rather than a
		// countdown from the first warm-up.
		f.run(t, path, "all")
	}
	if len(f.sent) != 3 {
		t.Fatalf("sent %d times in 45 minutes, want initial, 15-minute and 45-minute retries", len(f.sent))
	}
	// Once its reset counts down from the first request, stop retrying.
	f.now = start.Add(50 * time.Minute)
	reset := start.Add(45*time.Minute + fiveHours)
	f.ws["a@example.com"][0] = win("5 hours", fiveHours, 0, reset)
	f.run(t, path, "all")
	f.now = start.Add(2 * time.Hour)
	f.run(t, path, "all")
	f.now = start.Add(2*time.Hour + 5*time.Minute)
	f.ws["a@example.com"][0] = win("5 hours", fiveHours, 2, reset)
	f.run(t, path, "all")
	if len(f.sent) != 3 {
		t.Fatalf("retried a running window: %v", f.sent)
	}
	f.now = reset.Add(time.Minute)
	f.run(t, path, "all")
	if len(f.sent) != 4 {
		t.Fatalf("did not warm the next reset: %v", f.sent)
	}
}

// A warm-up goes to the ChatGPT backend as the gateway's Codex requests
// do: the account's own sign-in, Codex's instructions, nothing stored.
func TestCodexWarmRequest(t *testing.T) {
	home := signIn(t) // me@example.com, acct-1, lists gpt-5.5
	rememberLogins(true)
	var mu sync.Mutex
	var got []map[string]any
	var hdr http.Header
	var usageAsked int
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			usageAsked++
			// a week that hasn't started: nothing used, its reset a week off
			json.NewEncoder(w).Encode(map[string]any{"plan_type": "plus", "rate_limit": map[string]any{
				"primary_window":   map[string]any{"used_percent": 0, "limit_window_seconds": 18000, "reset_after_seconds": 18000},
				"secondary_window": map[string]any{"used_percent": 0, "limit_window_seconds": 604800, "reset_after_seconds": 604800}}})
		case "/backend-api/codex/responses":
			var m map[string]any
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &m)
			got, hdr = append(got, m), r.Header.Clone()
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer fake.Close()
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })

	loginUsageCache.Lock()
	loginUsageCache.m = nil
	loginUsageCache.Unlock()
	w := codexWarmer{path: codexWarmPath(), now: time.Now, usage: codexWarmUsage, send: warmCodexLogin}
	rs := w.warmNow(context.Background(), "week", nil)
	if len(rs) != 1 || rs[0].User != "me@example.com" || rs[0].Err != "" {
		t.Fatalf("warm: %+v", rs)
	}
	if !strings.HasPrefix(codexWarmPath(), filepath.Join(home, ".config")) {
		t.Fatalf("kept at %s", codexWarmPath())
	}
	if len(got) != 1 {
		t.Fatalf("%d requests", len(got))
	}
	m := got[0]
	if m["model"] != "gpt-5.5" || m["store"] != false || m["stream"] != true || m["instructions"] == "" || m["instructions"] == nil {
		t.Fatalf("body: %v", m)
	}
	if b, _ := json.Marshal(m["input"]); !strings.Contains(string(b), `"hi"`) {
		t.Fatalf("input: %s", b)
	}
	if !strings.HasPrefix(hdr.Get("Authorization"), "Bearer ") || hdr.Get("chatgpt-account-id") != "acct-1" || hdr.Get("originator") != "codex_cli_rs" {
		t.Fatalf("headers: %v", hdr)
	}
	// a second look, the read cached: nothing more is sent
	if rs := w.warmNow(context.Background(), "week", nil); len(rs) != 0 || len(got) != 1 {
		t.Fatalf("again: %+v", rs)
	}
	if CodexWarmed()["me@example.com"].IsZero() {
		t.Fatalf("not kept: %v", CodexWarmed())
	}
}

// #604: the warm-up asks the cheapest model, by price where it is known,
// else a mini, else a luna, else the first listed.
func TestWarmPick(t *testing.T) {
	ids := func(ids ...string) []catalog.Model {
		ms := make([]catalog.Model, len(ids))
		for i, id := range ids {
			ms[i] = catalog.Model{ID: id}
		}
		return ms
	}
	prices := map[string]catalog.Price{
		"gpt-6.1-sol": {Input: 2, Output: 10}, "gpt-6-astra": {Input: 10, Output: 50},
		"gpt-6-luna": {Input: 0.1, Output: 0.5}, "gpt-5.6-luna": {Input: 0.2, Output: 1.2},
	}
	priced := func(id string) (catalog.Price, bool) { p, ok := prices[id]; return p, ok }
	none := func(string) (catalog.Price, bool) { return catalog.Price{}, false }
	gpt6 := ids("gpt-6.1-sol", "gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5")
	for _, c := range []struct {
		name  string
		ms    []catalog.Model
		price func(string) (catalog.Price, bool)
		want  string
	}{
		{"priced", gpt6, priced, "gpt-6-luna"},
		{"no prices: a luna", gpt6, none, "gpt-6-luna"},
		{"no prices: a mini first", ids("gpt-5-codex", "gpt-5-codex-mini", "gpt-6-luna"), none, "gpt-5-codex-mini"},
		{"no prices, no cheap name", ids("gpt-6.1-sol", "gpt-6-sol"), none, "gpt-6.1-sol"},
		{"the model's own price first", []catalog.Model{{ID: "gpt-6.1-sol"}, {ID: "x", Price: &catalog.Price{Input: 0.01, Output: 0.01}}}, priced, "x"},
	} {
		if got := warmPick(c.ms, c.price).ID; got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}
