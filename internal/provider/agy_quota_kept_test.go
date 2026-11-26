package provider

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/testenv"
)

// agy-cli's literal reasons for no reading this time (its usage.mjs:
// DUE_FOR_RENEWAL, STILL_READING and the two timeouts), at 0.1.147.
const (
	agyDueForRenewal = "agy's sign-in is due to be renewed, and agy renews it itself on this account's next chat; the quota is read after that. " +
		"magpie doesn't run agy just for usage meanwhile: a renewal that fails there can open a Google sign-in in the browser."
	agyStillReading = "agy is still reading this account's quota (its own read takes longer here: a slow network or proxy, or agy's first start). " +
		"It shows on the next refresh."
	agyReadTimeout    = "agy's own usage read didn't finish in 45s. agy asks Google about twelve times, one after another, before it has the quota, which takes longer behind a slow network or proxy. It's asked again in a minute; if this stays, check agy's network or proxy."
	agyVersionTimeout = "agy didn't say its version in 45s (its first start after an update can take that long), so the quota wasn't read. It's asked again in a minute."
)

// Each of agy-cli's reasons for no reading this time passes: the last
// reading is shown beside it. An account whose sign-in is gone doesn't.
func TestAgyReasonsPass(t *testing.T) {
	for _, s := range []string{agyDueForRenewal, agyStillReading, agyReadTimeout, agyVersionTimeout} {
		if !passing.MatchString(s) {
			t.Errorf("%q doesn't pass", s)
		}
	}
	for _, s := range []string{
		"agy's sign-in no longer works. Sign in again from this account's Sign in in magpie, or run `agy` in a terminal; magpie doesn't run agy for usage until then.",
		"agy CLI not found. Sign in to this account again in magpie to have it installed, or install Antigravity CLI yourself.",
	} {
		if passing.MatchString(s) {
			t.Errorf("%q passes", s)
		}
	}
}

// punk2sang on X: "好像没办法看 antigravity 额度了". agy-cli 0.1.140 reads
// the quota through agy alone, and doesn't run agy while its sign-in is due
// to be renewed, which it is an hour after agy last ran: the read says so
// and has no windows. magpie didn't take that for a read failing in
// passing, nor keep agy-cli's readings (its pools count some models), so
// the card had the reason in English and no quota at all. The last reading
// is kept, across a restart too, its pools still counting their models,
// and shown as of when it was read with the reason beside it.
func TestAgyQuotaKeptWhileAgyCantRead(t *testing.T) {
	bun := testenv.Bun(t)
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	const user = "agy@fake"
	ws := filepath.Join(t.TempDir(), "windows.json")
	resets := time.Now().Add(72 * time.Hour).UTC().Format(time.RFC3339)
	claude := []string{"claude-opus-5-5", "claude-opus-5-5-high", "claude-sonnet-5-5", "gpt-oss-120b"}
	say := func(v any) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{user: v})
		if err := os.WriteFile(ws, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// as parseUsageReport gives LIVE_REPORT (plugins' test/usage.mjs)
	say([]map[string]any{
		{"name": "7 days", "used": 66.394961, "resetsAt": resets, "span": 604800, "aside": true, "pool": "Claude & GPT", "models": claude},
		{"name": "5 hours", "used": 0, "resetsAt": resets, "span": 18000, "aside": true, "pool": "Gemini", "models": []string{"gemini-4-hydrogen", "gemini-3.8-flash"}},
		{"name": "Claude Opus 5.5", "used": 66.394961, "resetsAt": resets, "span": 604800, "model": "claude-opus-5-5", "family": "Claude", "pool": "Claude & GPT", "models": []string{"claude-opus-5-5", "claude-opus-5-5-high"}},
	})
	t.Setenv("FAKE_WINDOWS", ws)
	t.Cleanup(plugin.Settle)
	forget := func() {
		loginUsageCache.Lock()
		loginUsageCache.m, loginUsageCache.pending = nil, nil
		loginUsageCache.Unlock()
		lastQuotas.Lock()
		lastQuotas.m, lastQuotas.loaded = nil, false
		lastQuotas.Unlock()
	}
	forget()
	t.Cleanup(forget)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	k, err := plugin.Import(ctx, "fakeco", map[string]any{"type": "oauth", "refresh": "r", "access": "a", "expires": 9e15, "accountId": user})
	if err != nil {
		t.Fatal(err)
	}
	saveLogins(t, savedLogin{Agent: "plugin:fakeco", User: user, Home: k, On: true})
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	l := Login{Agent: "plugin:fakeco", User: user}
	if q := loginReading(ctx, l).read; q.Error != "" || len(q.Windows) != 3 {
		t.Fatalf("the first reading: %+v", q)
	}
	for _, why := range []string{agyDueForRenewal, agyStillReading} {
		say(why)
		// a restart: nothing in memory, the reading kept in quotas.json
		forget()
		q := loginReading(ctx, l).read
		if q.Error != "" || len(q.Windows) != 3 || q.AsOf == nil {
			t.Fatalf("%.30s…: the card has no reading: %+v", why, q)
		}
		if q.ReadError != why || q.ReadDeferred {
			t.Fatalf("%.30s…: the card doesn't say why: %q (deferred %v)", why, q.ReadError, q.ReadDeferred)
		}
		pool, opus, gemini := q.Windows[0], q.Windows[2], q.Windows[1]
		if pool.Used != 66.394961 || !pool.Aside || pool.Pool != "Claude & GPT" || opus.Model != "claude-opus-5-5" {
			t.Fatalf("the kept windows: %+v", q.Windows)
		}
		// each still counts its own models, and only those
		if pool.matches == nil || !pool.matches("gpt-oss-120b") || pool.matches("gemini-4-hydrogen") ||
			gemini.matches == nil || !gemini.matches("gemini-3.8-flash") || gemini.matches("claude-opus-5-5") ||
			opus.matches == nil || !opus.matches("claude-opus-5-5-high") || opus.matches("claude-sonnet-5-5") {
			t.Fatalf("%.30s…: the kept windows count other models", why)
		}
	}
	// a reading of the models a window doesn't count is kept as it was told
	say([]map[string]any{{"name": "Month", "used": 40, "resetsAt": resets, "span": 2592000, "notModels": []string{"fake-free"}}})
	forget()
	if q := loginReading(ctx, l).read; q.Error != "" || len(q.Windows) != 1 {
		t.Fatalf("the notModels reading: %+v", q)
	}
	say(agyDueForRenewal)
	forget()
	q := loginReading(ctx, l).read
	if len(q.Windows) != 1 || q.Windows[0].matches == nil || q.Windows[0].matches("fake-free") || !q.Windows[0].matches("fake-pro") {
		t.Fatalf("the kept notModels window: %+v", q)
	}
}
