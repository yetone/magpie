package gateway

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// sub2api's refusals, as v0.1.149 sends them on the OpenAI and Responses
// paths: a key out of the 5-hour, day or 7-day limit its owner gave it is
// out of quota till that window starts again, though it says
// rate_limit_exceeded; the relay out of accounts for everyone, or its
// upstream rate limiting, is not the key's quota.
func TestSub2APIKeyLimitIsQuota(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   string
	}{
		{429, `{"error":{"message":"api key 7天限额已用完","type":"rate_limit_exceeded"}}`, failQuota},
		{429, `{"error":{"message":"api key 日限额已用完","type":"rate_limit_exceeded"}}`, failQuota},
		{429, `{"error":{"message":"api key 5小时限额已用完","type":"rate_limit_exceeded"}}`, failQuota},
		// the key's own total quota (its middleware's words)
		{429, `{"code":"API_KEY_QUOTA_EXHAUSTED","message":"API key 额度已用完"}`, failQuota},
		// the pool: v0.1.149's 503, later versions' 429
		{503, `{"error":{"message":"No available accounts","type":"api_error"}}`, failOther},
		{429, `{"error":{"message":"All available accounts are currently rate-limited. Please retry later.","type":"rate_limit_error"}}`, failRate},
		{429, `{"error":{"message":"Upstream rate limit exceeded, please retry later","type":"rate_limit_error"}}`, failRate},
		// the wallet
		{403, `{"code":"INSUFFICIENT_BALANCE","message":"Insufficient account balance"}`, failCredit},
	} {
		if got := failure(c.status, []byte(c.body)); got != c.want {
			t.Errorf("%d %s: %s, want %s", c.status, c.body, got, c.want)
		}
	}
}

// keyWindows swaps in each key's windows, by the key, for a test.
func keyWindows(t *testing.T, of map[string]provider.Allowance) *[]string {
	t.Helper()
	old, oldStale := keyAllowance, staleKeyAllowance
	var mu sync.Mutex
	var staled []string
	keyAllowance = func(p provider.Provider) (provider.Allowance, bool) {
		a, ok := of[p.Key]
		return a, ok
	}
	staleKeyAllowance = func(p provider.Provider) {
		mu.Lock()
		staled = append(staled, p.Key)
		mu.Unlock()
	}
	t.Cleanup(func() { keyAllowance, staleKeyAllowance = old, oldStale })
	return &staled
}

func week(used float64, renews time.Time) provider.Allowance {
	return provider.Allowance{{Used: used, Resets: renews, Span: 7 * 24 * time.Hour, Amount: used * 8, Of: 800, Unit: "USD"}}
}

// A provider's keys whose windows magpie reads (sub2api keys given a
// limit) are weighed by them as its accounts are: smart spends first the
// one whose week renews soonest and leaves one used up last, least used
// goes by the share, weekly pace by what is left per hour.
func TestKeysWeighedByTheirWindows(t *testing.T) {
	now := time.Now()
	of := map[string]provider.Allowance{}
	keyWindows(t, of)
	key := func(k string) candidate {
		return candidate{p: provider.Provider{ID: "kw", Key: k}, model: "gpt-6-astra", rest: "kw#" + k}
	}
	cs := []candidate{key("a"), key("b"), key("c")}
	p := provider.Provider{ID: "kw"}

	// smart: b's week renews in a day, a's in six; c used up
	of["a"], of["b"], of["c"] = week(10, now.Add(6*24*time.Hour)), week(40, now.Add(24*time.Hour)), week(99, now.Add(2*24*time.Hour))
	got, wg := weigh(p, cs, "gpt-6-astra", provider.Responses)
	if restsOf(got) != "kw#b kw#a kw#c " {
		t.Fatalf("smart: %s", restsOf(got))
	}
	if w := weighed(got[0], p, wg, false, provider.Responses); w.Kind != "key" || !w.Known || w.Used != 40 || w.Limit != 800 || w.Unit != "USD" || len(w.Renews) != 1 {
		t.Fatalf("the trace of b: %+v", w)
	}

	// least used: by the share
	p.Routing = provider.LeastUsed
	of["a"], of["b"], of["c"] = week(60, now.Add(6*24*time.Hour)), week(70, now.Add(24*time.Hour)), week(20, now.Add(2*24*time.Hour))
	if got := restsOf(route(p, cs, "gpt-6-astra", provider.Responses)); got != "kw#c kw#a kw#b " {
		t.Fatalf("least used: %s", got)
	}

	// weekly pace: a has 90% left for 20 hours, b 100% for a week
	p.Routing = provider.Pace
	of["a"], of["b"], of["c"] = week(10, now.Add(20*time.Hour)), week(0, now.Add(7*24*time.Hour)), week(99, now.Add(time.Hour))
	if got := restsOf(route(p, []candidate{key("b"), key("a"), key("c")}, "gpt-6-astra", provider.Responses)); got != "kw#a kw#b kw#c " {
		t.Fatalf("pace: %s", got)
	}
}

// In a group, a sub2api key given a limit stands beside subscriptions by
// its windows: first while its week renews sooner than theirs, after
// them once it is used up — rather than after every subscription with
// quota, whatever it has left.
func TestGroupWeighsKeyWindows(t *testing.T) {
	now := time.Now()
	old := allowances
	t.Cleanup(func() { allowances = old })
	allowances = func(string) map[string]provider.Allowance {
		return map[string]provider.Allowance{"me@example.com": week(30, now.Add(5*24*time.Hour))}
	}
	of := map[string]provider.Allowance{"sk-relay": week(10, now.Add(24*time.Hour))}
	keyWindows(t, of)
	codex := provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: "me@example.com"}}
	relay := provider.Provider{ID: "sub2api", Key: "sk-relay", Responses: "https://relay.example/v1"}
	heads := []candidate{
		{p: codex, model: "gpt-6-astra", rest: "codex"},
		{p: relay, model: "gpt-6-astra", rest: "sub2api"},
	}
	g := provider.Provider{ID: provider.GroupPrefix + "g"}
	got, wg := weigh(g, heads, "", provider.Responses)
	if restsOf(got) != "sub2api codex " {
		t.Fatalf("the key renewing first: %s", restsOf(got))
	}
	if w := weighed(got[0], relay, wg, false, provider.Responses); w.Kind != "provider" || !w.Known || w.Used != 10 {
		t.Fatalf("the key's trace: %+v", w)
	}
	of["sk-relay"] = week(99, now.Add(24*time.Hour))
	if got, _ := weigh(g, heads, "", provider.Responses); restsOf(got) != "codex sub2api " {
		t.Fatalf("the key used up: %s", restsOf(got))
	}
}

// A key refused for its own limit rests until the window it filled starts
// again, as an account does, and its windows are read again; when they
// were last read short of full, it rests a quarter of an hour.
func TestKeyOutOfItsWindowRestsUntilReset(t *testing.T) {
	now := time.Now()
	of := map[string]provider.Allowance{
		"sk-full":  week(100, now.Add(3*24*time.Hour)),
		"sk-stale": week(40, now.Add(3*24*time.Hour)),
	}
	staled := keyWindows(t, of)
	key := func(k string) candidate {
		return candidate{p: provider.Provider{ID: "kr", Key: k}, model: "gpt-6-astra", rest: "kr#" + k}
	}
	s := &Server{}
	body := []byte(`{"error":{"message":"api key 7天限额已用完","type":"rate_limit_exceeded"}}`)
	near := func(d, want time.Duration) bool { return d > want-time.Minute && d <= want }

	r := s.restAfter(key("sk-full"), 429, http.Header{}, body)
	if r.Why != failQuota || r.By != "window" || !near(time.Until(r.Until), 3*24*time.Hour) {
		t.Fatalf("full: rests %v by %s (%s)", time.Until(r.Until), r.By, r.Why)
	}
	r = s.restAfter(key("sk-stale"), 429, http.Header{}, body)
	if r.Why != failQuota || r.By != "quota" || !near(time.Until(r.Until), quotaRest) {
		t.Fatalf("stale: rests %v by %s (%s)", time.Until(r.Until), r.By, r.Why)
	}
	if len(*staled) != 2 || (*staled)[0] != "sk-full" || (*staled)[1] != "sk-stale" {
		t.Fatalf("read again: %v", *staled)
	}
}

// The relay out of accounts for everyone (sub2api's 503) says nothing of
// the key's own windows: it backs off a minute, full or not, and they
// aren't read again for it.
func TestKeyPoolErrorDoesNotRestForWindow(t *testing.T) {
	of := map[string]provider.Allowance{"sk-pool": week(100, time.Now().Add(3*24*time.Hour))}
	staled := keyWindows(t, of)
	c := candidate{p: provider.Provider{ID: "kp", Key: "sk-pool"}, model: "gpt-6-astra", rest: "kp#sk-pool"}
	forget := func() { routed.Lock(); delete(routed.failures, c.restKey()); routed.Unlock() }
	forget() // its first failure
	t.Cleanup(forget)
	r := (&Server{}).restAfter(c, 503, http.Header{}, []byte(`{"error":{"message":"No available accounts","type":"api_error"}}`))
	if r.Why != failOther || r.By != "backoff" || time.Until(r.Until) > fallbackCooldown {
		t.Fatalf("pool empty: rests %v by %s (%s)", time.Until(r.Until), r.By, r.Why)
	}
	if len(*staled) != 0 {
		t.Fatalf("read again: %v", *staled)
	}
}
