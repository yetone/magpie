package provider

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// the agy-cli provider's models, as the owner's plugin-providers.json had
// them on 11-22
var agyTestModels = []string{"gemini-4-neon", "gemini-4-argon", "gemini-4-hydrogen", "gemini-3.8-flash",
	"claude-opus-5-5", "claude-sonnet-5-5", "gpt-oss-120b-medium", "gpt-oss-120b"}

// agyPoolWin is one of a pool's windows as the agy-cli plugin tells it
// (usage.mjs): set aside, its models listed.
func agyPoolWin(pool, name string, span time.Duration, used float64, resets time.Time, models ...string) QuotaWindow {
	w := win(name, span, used, resets)
	w.Pool, w.Aside, w.matches = pool, true, idSetOf(models).has
	return w
}

// agyFamilyWin is a model family's window in a pool, reading the pool's
// tightest bucket.
func agyFamilyWin(pool, family, model string, w QuotaWindow) QuotaWindow {
	w.Name, w.Family, w.Model, w.Pool = model, family, model, pool
	return w
}

type agySent struct{ user, model string }

func agyWarmFor(t *testing.T, path string, now *time.Time, usage *map[string]SubscriptionQuota, sent *[]agySent) codexWarmer {
	t.Helper()
	a := &agyWarm{
		read:   func(context.Context) map[string]SubscriptionQuota { return *usage },
		models: func() []string { return agyTestModels },
		post: func(_ context.Context, user, model string) error {
			*sent = append(*sent, agySent{user, model})
			return nil
		},
	}
	w := agyWarmer(a)
	w.path, w.now = path, func() time.Time { return *now }
	return w
}

// 0Block0_o on X: an agy account's 7-day window starts at its first
// request after the reset (the owner's history: Gemini's read 100% with
// its reset rolling a week on from each reading, until a request began
// it), and each pool on its own: a Gemini request left Claude & GPT's
// rolling. So each pool whose window reset is sent one request, to a
// light model of its own, once, and a pool still running is sent none.
func TestAgyWarmStartsEachPoolOnItsOwn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agy-warmup.json")
	now := time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)
	gemini := []string{"gemini-3.8-flash", "gemini-4-hydrogen", "gemini-4-neon"}
	cg := []string{"claude-opus-5-5", "claude-sonnet-5-5", "gpt-oss-120b", "gpt-oss-120b-medium"}
	cgReset := now.Add(3 * time.Hour)
	read := func() map[string]SubscriptionQuota {
		// as read now: an idle window's reset a whole window from the reading
		geminiWeek := agyPoolWin("Gemini", "7 days", week, 0, now.Add(week), gemini...)
		return map[string]SubscriptionQuota{"a@example.com": {Windows: []QuotaWindow{
			agyFamilyWin("Gemini", "gemini", "gemini-3.8-flash", geminiWeek),
			agyFamilyWin("Claude & GPT", "claude", "claude-opus-5-5", agyPoolWin("Claude & GPT", "7 days", week, 41.2, cgReset, cg...)),
			agyPoolWin("Gemini", "5 hours", fiveHours, 0, now.Add(fiveHours), gemini...),
			geminiWeek,
			agyPoolWin("Claude & GPT", "5 hours", fiveHours, 0, now.Add(fiveHours), cg...),
			agyPoolWin("Claude & GPT", "7 days", week, 41.2, cgReset, cg...),
		}}}
	}
	usage := read()
	var sent []agySent
	w := agyWarmFor(t, path, &now, &usage, &sent)
	rs := w.warmNow(context.Background(), "week", nil)
	if len(rs) != 1 || rs[0].User != "a@example.com · Gemini" || strings.Join(rs[0].Windows, ",") != "7 days" {
		t.Fatalf("week: %+v", rs)
	}
	if !slices.Equal(sent, []agySent{{"a@example.com", "gemini-3.8-flash"}}) {
		t.Fatalf("sent %+v", sent)
	}
	if !agyWarmedOf(codexWarmedIn(path))["a@example.com"].Equal(now) {
		t.Fatal("warm-up not kept by account")
	}

	// the request began Gemini's week; a magpie started again sends nothing
	now = now.Add(5 * time.Minute)
	usage = read()
	q := usage["a@example.com"]
	q.Windows[3] = agyPoolWin("Gemini", "7 days", week, 0.08, now.Add(week-5*time.Minute), gemini...)
	usage["a@example.com"] = q
	sent = nil
	w = agyWarmFor(t, path, &now, &usage, &sent)
	if rs := w.warmNow(context.Background(), "week", nil); len(rs) != 0 || len(sent) != 0 {
		t.Fatalf("sent again: %+v %+v", rs, sent)
	}

	// Claude & GPT's week resets: its own request, to its lightest model
	now = cgReset.Add(time.Minute)
	usage = read()
	q = usage["a@example.com"]
	q.Windows[3] = agyPoolWin("Gemini", "7 days", week, 3, now.Add(4*24*time.Hour), gemini...)
	q.Windows[5] = agyPoolWin("Claude & GPT", "7 days", week, 0, now.Add(week), cg...)
	usage["a@example.com"] = q
	rs = w.warmNow(context.Background(), "week", nil)
	if len(rs) != 1 || rs[0].User != "a@example.com · Claude & GPT" || !slices.Equal(sent, []agySent{{"a@example.com", "gpt-oss-120b"}}) {
		t.Fatalf("claude & gpt: %+v %+v", rs, sent)
	}

	// "all" starts the 5-hour windows too, one request a pool for both
	sent = nil
	rs = w.warmNow(context.Background(), "all", nil)
	if len(rs) != 2 || len(sent) != 2 {
		t.Fatalf("all: %+v %+v", rs, sent)
	}
}

// A model family's window (its pool's figures under the family's name) is
// never warmed on its own: its pool's is, with one request.
func TestAgyPoolsLeaveFamiliesAndUnknownPoolsOut(t *testing.T) {
	now := time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)
	idleWeek := agyPoolWin("Gemini", "7 days", week, 0, now.Add(week), "gemini-3.8-flash")
	usage := map[string]SubscriptionQuota{
		"a@example.com": {Windows: []QuotaWindow{
			agyFamilyWin("Gemini", "gemini", "gemini-3.8-flash", idleWeek),
			idleWeek,
			// a pool of models the provider doesn't serve: nothing starts it
			agyPoolWin("Orchestrator", "7 days", week, 0, now.Add(week), "orchestrator-1"),
			// a bucket with no share told
			{Name: "7 days", Pool: "Gemini 4 Argon", Span: week, Aside: true, Text: true, Display: "Limits not available"},
		}},
		"b@example.com": {Error: "agy isn't signed in"},
	}
	pools, targets := agyPools(usage, agyTestModels)
	if len(pools) != 1 || len(pools["a@example.com · Gemini"].Windows) != 1 {
		t.Fatalf("pools: %+v", pools)
	}
	if w := pools["a@example.com · Gemini"].Windows[0]; w.Aside || w.Model != "" {
		t.Fatalf("window: %+v", w)
	}
	if tg := targets["a@example.com · Gemini"]; tg != (agyTarget{"a@example.com", "Gemini", "gemini-3.8-flash"}) {
		t.Fatalf("target: %+v", tg)
	}
}

func TestAgyWarmModelLightest(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{[]string{"gemini-4-neon", "gemini-4-hydrogen", "gemini-3.8-flash"}, "gemini-3.8-flash"},
		{[]string{"claude-opus-5-5", "claude-sonnet-5-5", "gpt-oss-120b-medium", "gpt-oss-120b"}, "gpt-oss-120b"},
		{[]string{"gemini-4-argon-high", "gemini-4-argon-low", "gemini-4-argon"}, "gemini-4-argon-low"},
		{[]string{"gemini-4-argon"}, "gemini-4-argon"},
	} {
		models := append([]string{"elsewhere-1"}, c.in...)
		if got := agyWarmModel(models, idSetOf(c.in).has); got != c.want {
			t.Errorf("%v: %q, want %q", c.in, got, c.want)
		}
	}
	if got := agyWarmModel(agyTestModels, idSetOf([]string{"nothing-here"}).has); got != "" {
		t.Fatalf("none: %q", got)
	}
}

// Warm up now asks each pool of the account whose windows aren't running,
// and Warm up all leaves an account whose every pool runs alone.
func TestAgyWarmNowAsksThePoolsNotRunning(t *testing.T) {
	now := time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)
	running := func(pool string, models ...string) QuotaWindow {
		return agyPoolWin(pool, "7 days", week, 10, now.Add(2*24*time.Hour), models...)
	}
	usage := map[string]SubscriptionQuota{
		"A@example.com": {Windows: []QuotaWindow{
			agyPoolWin("Gemini", "7 days", week, 0, now.Add(week), "gemini-3.8-flash"),
			running("Claude & GPT", "gpt-oss-120b"),
		}},
		"b@example.com": {Windows: []QuotaWindow{running("Gemini", "gemini-3.8-flash"), running("Claude & GPT", "gpt-oss-120b")}},
	}
	var sent []agySent
	send := func(_ context.Context, user, model string) error {
		sent = append(sent, agySent{user, model})
		return nil
	}
	if err := warmAgyNow(context.Background(), "a@example.com", usage, agyTestModels, now, send); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sent, []agySent{{"A@example.com", "gemini-3.8-flash"}}) {
		t.Fatalf("sent %+v", sent)
	}
	sent = nil
	if err := warmAgyNow(context.Background(), "b@example.com", usage, agyTestModels, now, send); !errors.Is(err, errAgyRunning) || len(sent) != 0 {
		t.Fatalf("running: %v %+v", err, sent)
	}
	if err := warmAgyNow(context.Background(), "c@example.com", usage, agyTestModels, now, send); err == nil {
		t.Fatal("an account never read warmed")
	}
	by := agyByAccount(usage, agyTestModels)
	if windowsRunning(by["A@example.com"], now) || !windowsRunning(by["b@example.com"], now) {
		t.Fatalf("running: %+v", by)
	}
}
