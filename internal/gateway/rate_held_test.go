package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// limitedUp is a vendor that has every key or account it serves over its
// rate limit for the next five minutes, and says so as refuse does. It
// counts what it was asked.
type limitedUp struct {
	refuse func(w http.ResponseWriter)
	n      atomic.Int32
}

func (u *limitedUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	u.n.Add(1)
	w.Header()["Content-Type"] = nil
	u.refuse(w)
}

// limitedFor is the 300 seconds hsjzg58958's vendor named.
const limitedFor = 300 * time.Second

// rateRefusals are the ways a vendor says an account or key is rate
// limited for limitedFor: the Retry-After header, Anthropic's
// anthropic-ratelimit-*-reset and OpenAI's x-ratelimit-reset-* headers
// with no Retry-After, and ChatGPT's resets_in_seconds in the error.
var rateRefusals = []struct {
	name   string
	refuse func(w http.ResponseWriter)
}{
	{"Retry-After", func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "300")
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"Rate limit reached for requests","type":"rate_limit_error"}}`)
	}},
	{"anthropic-ratelimit reset", func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("anthropic-ratelimit-requests-remaining", "0")
		w.Header().Set("anthropic-ratelimit-requests-reset", time.Now().Add(limitedFor).UTC().Format(time.RFC3339))
		w.WriteHeader(429)
		io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit. Please try again later."}}`)
	}},
	{"x-ratelimit reset", func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-ratelimit-remaining-requests", "0")
		w.Header().Set("x-ratelimit-reset-requests", "5m0s")
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"Rate limit reached for gpt-5.5 on requests per min (RPM): Limit 3, Used 3, Requested 1.","type":"requests","code":"rate_limit_exceeded"}}`)
	}},
	{"resets_in_seconds", func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"type":"rate_limit_exceeded","message":"Rate limit reached. Please try again later.","resets_in_seconds":300}}`)
	}},
}

// clientRetries is an agent asking again every half a second after a
// 429, as Codex and Claude Code do on their own; it returns the last
// answer.
func clientRetries(t *testing.T, n int, ask func() *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	var rec *httptest.ResponseRecorder
	for range n {
		time.Sleep(500 * time.Millisecond)
		rec = ask()
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("retry answered %d: %s", rec.Code, rec.Body)
		}
	}
	return rec
}

// heldRetryAfter checks that an answer given while every candidate is
// limited tells the agent when it is back, within the vendor's time.
func heldRetryAfter(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	ra, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || ra < int(limitedFor/time.Second)-30 || ra > int(limitedFor/time.Second) {
		t.Errorf("Retry-After %q, want about %v", rec.Header().Get("Retry-After"), limitedFor)
	}
}

// hsjzg58958 on X: a vendor rate limited the account for 300 seconds, and
// magpie asked it again about every half second, as the agent retried,
// till the vendor flagged two accounts. A key or account the vendor named
// a time for is not asked again before it: the agent's retries are
// answered here, 429 with when the first of them is back.
func TestRateLimitedKeysHeldUntilTheirReset(t *testing.T) {
	for _, x := range rateRefusals {
		for _, keys := range [][]string{{"k-a"}, {"k-a", "k-b"}} {
			t.Run(x.name+"/"+strconv.Itoa(len(keys))+" keys", func(t *testing.T) {
				fresh(t)
				up := &limitedUp{refuse: x.refuse}
				srv := httptest.NewServer(up)
				t.Cleanup(srv.Close)
				p := provider.Provider{ID: "lim", Name: "lim", Chat: srv.URL + "/v1", Models: []string{"m"}, Key: keys[0]}
				for _, k := range keys[1:] {
					p.Keys = append(p.Keys, provider.KeyAccount{Key: k})
				}
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
				s := New()
				ask := func() *httptest.ResponseRecorder {
					rec := httptest.NewRecorder()
					req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"lim/m","messages":[{"role":"user","content":"hi"}]}`))
					req.Header.Set("Content-Type", "application/json")
					s.Handler().ServeHTTP(rec, req)
					return rec
				}
				if rec := ask(); rec.Code != http.StatusTooManyRequests {
					t.Fatalf("first: %d %s", rec.Code, rec.Body)
				}
				first := int(up.n.Load())
				if first != len(keys) {
					t.Errorf("first request asked %d times, want each key once", first)
				}
				rec := clientRetries(t, 4, ask)
				if n := int(up.n.Load()); n != first {
					t.Errorf("the agent's 4 retries reached the vendor %d times while every key was limited for %v", n-first, limitedFor)
				}
				heldRetryAfter(t, rec)
			})
		}
	}
}

// The same for ChatGPT accounts behind Codex, one or two (the thread
// hsjzg58958 replied in): each refused for five minutes is asked once,
// whether ChatGPT says it is rate limited or out of its allowance till
// then.
func TestRateLimitedCodexAccountsHeldUntilTheirReset(t *testing.T) {
	cases := append([]struct {
		name   string
		refuse func(w http.ResponseWriter)
	}{{"usage_limit_reached", func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_in_seconds":300}}`)
	}}}, rateRefusals[0], rateRefusals[3])
	for _, x := range cases {
		for _, spares := range [][]string{nil, {"spare@example.com"}} {
			t.Run(x.name+"/"+strconv.Itoa(len(spares)+1)+" accounts", func(t *testing.T) {
				codexSignedIn(t, spares...)
				up := &limitedUp{refuse: x.refuse}
				srv := httptest.NewServer(up)
				t.Cleanup(srv.Close)
				was := provider.CodexBase
				provider.CodexBase = srv.URL + "/backend-api/codex"
				t.Cleanup(func() { provider.CodexBase = was })
				s := New()
				ask := func() *httptest.ResponseRecorder {
					rec := httptest.NewRecorder()
					req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(oneAccountTurn))
					req.Header.Set("Authorization", "Bearer chatgpt-token")
					req.Header.Set("chatgpt-account-id", "acct-1")
					s.Handler().ServeHTTP(rec, req)
					return rec
				}
				if rec := ask(); rec.Code != http.StatusTooManyRequests {
					t.Fatalf("first: %d %s", rec.Code, rec.Body)
				}
				first := int(up.n.Load())
				if first != len(spares)+1 {
					t.Errorf("first request asked ChatGPT %d times, want each account once", first)
				}
				rec := clientRetries(t, 4, ask)
				if n := int(up.n.Load()); n != first {
					t.Errorf("Codex's 4 retries reached ChatGPT %d times while every account was limited for %v", n-first, limitedFor)
				}
				heldRetryAfter(t, rec)
			})
		}
	}
}
