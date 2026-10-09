package gateway

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// An account out of quota rests until it says it's back, however far off
// that is, and the trace says so (#147: ChatGPT's resets_at three hours
// off was a rest of "59 minutes", the Retry-After magpie made of it cut to
// an hour); a rate limit's Retry-After and a failure's backoff stay short,
// and a Codex reset spent lifts its rest out of quota, not one for a rate
// limit or a failure.
func TestQuotaRestsUntilItsReset(t *testing.T) {
	old := allowances
	defer func() { allowances = old }()
	now := time.Now()
	share := map[string]provider.Allowance{}
	allowances = func(string) map[string]provider.Allowance { return share }
	acct := func(user string) candidate {
		return candidate{p: provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: user}}, model: "gpt-5.6-sol", rest: "codex#" + user}
	}
	s := &Server{}
	near := func(d, want time.Duration) bool { return d > want-time.Minute && d <= want }

	// ChatGPT's refusal, put in words for the agent, with the headers
	// keepRetry gave it
	at := now.Add(3*time.Hour + 45*time.Minute)
	body := []byte(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_at":` + strconv.FormatInt(at.Unix(), 10) + `,"resets_in_seconds":13500}}`)
	h := http.Header{}
	keepRetry(h, http.Header{}, body)
	if h.Get("Retry-After") != "13500" && h.Get("Retry-After") != "13499" {
		t.Fatalf("Retry-After for the agent: %q", h.Get("Retry-After"))
	}
	r := s.restAfter(acct("a@x.com"), 429, h, []byte("Codex: The usage limit has been reached"))
	if r.Why != failQuota || r.By != "resets" || !near(time.Until(r.Until), 3*time.Hour+45*time.Minute) {
		t.Fatalf("ChatGPT out until %v: rests %v by %s (%s)", at, time.Until(r.Until), r.By, r.Why)
	}
	if got, ok := restOf(acct("a@x.com").restKey()); !ok || !got.Until.Equal(r.Until) {
		t.Fatalf("the trace's rest: %+v %v", got, ok)
	}
	// resets_in_seconds alone
	r = s.restAfter(acct("b@x.com"), 429, http.Header{}, []byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":18000}}`))
	if r.By != "resets" || !near(time.Until(r.Until), 5*time.Hour) {
		t.Fatalf("resets in 5h: %v by %s", time.Until(r.Until), r.By)
	}
	// no word in the refusal, but its week is full: until it renews
	share["c@x.com"] = provider.Allowance{{Used: 100, Resets: now.Add(50 * time.Hour), Span: 7 * 24 * time.Hour}}
	h = http.Header{}
	h.Set("Retry-After", "600")
	r = s.restAfter(acct("c@x.com"), 429, h, []byte(`{"error":{"message":"You've hit your usage limit"}}`))
	if r.By != "window" || !near(time.Until(r.Until), 50*time.Hour) {
		t.Fatalf("week full: %v by %s", time.Until(r.Until), r.By)
	}
	// only a header to go by: held to the longest wait
	h.Set("Retry-After", "36000")
	r = s.restAfter(acct("d@x.com"), 429, h, []byte(`{"error":{"message":"usage limit reached"}}`))
	if r.By != "retry-after" || !near(time.Until(r.Until), longestWait) {
		t.Fatalf("quota by header: %v by %s", time.Until(r.Until), r.By)
	}
	// a rate limit, and a failure, stay short
	r = s.restAfter(acct("e@x.com"), 429, h, []byte(`{"error":{"message":"Too many requests"}}`))
	if r.Why != failRate || !near(time.Until(r.Until), longestWait) {
		t.Fatalf("rate limited: %v by %s", time.Until(r.Until), r.By)
	}
	r = s.restAfter(acct("f@x.com"), 502, http.Header{}, []byte("bad gateway"))
	if r.By != "backoff" || !near(time.Until(r.Until), fallbackCooldown) {
		t.Fatalf("failed: %v by %s", time.Until(r.Until), r.By)
	}
	// a reset far past a week is not trusted past the longest
	r = s.restAfter(acct("g@x.com"), 429, http.Header{}, []byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":`+strconv.Itoa(30*24*3600)+`}}`))
	if !near(time.Until(r.Until), longestQuota) {
		t.Fatalf("a month off: %v", time.Until(r.Until))
	}

	// a Codex reset spent on a@x.com brings it back, and it alone
	renewed("codex", "A@x.com")
	if _, ok := restOf(acct("a@x.com").restKey()); ok {
		t.Fatal("still resting after its windows started again")
	}
	if _, ok := restOf(acct("b@x.com").restKey()); !ok {
		t.Fatal("another account's rest was lifted")
	}
	// its windows started again lift a rest out of them, not one for a
	// rate limit or a failure
	for _, u := range []string{"c@x.com", "e@x.com", "f@x.com"} {
		renewed("codex", u)
	}
	if r, ok := restOf(acct("c@x.com").restKey()); ok {
		t.Fatalf("failed with its week full, still resting after it started again: %+v", r)
	}
	for _, u := range []string{"e@x.com", "f@x.com"} {
		if r, ok := restOf(acct(u).restKey()); !ok {
			t.Fatalf("%s: its rest (%s) lifted by its windows started again", u, r.Why)
		}
	}
}
