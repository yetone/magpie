package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// The countdown is what Claude Code's API-token clients receive in the
// error text. It is the actual reset, not the capped Retry-After.
func checkQuotaResetHint(t *testing.T, rec *httptest.ResponseRecorder, reset time.Time) {
	t.Helper()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	msg := gjson.Get(rec.Body.String(), "error.message").String()
	m := regexp.MustCompile(`\(reset after ([0-9hms]+)\)`).FindStringSubmatch(msg)
	if len(m) != 2 {
		t.Fatalf("no reset countdown in %q", msg)
	}
	d, err := time.ParseDuration(m[1])
	if err != nil || d < time.Until(reset)-2*time.Second || d > time.Until(reset)+2*time.Second {
		t.Fatalf("countdown %q, want about %v (err %v)", m[1], time.Until(reset), err)
	}
}

func TestClaudeQuotaResetHint(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			reset := time.Now().Add(2 * time.Hour).Truncate(time.Second)
			refusingClaude(t, reset)
			capRetryAfterOn(t, 60)
			s := New()
			t.Cleanup(s.subscription.abortAll)
			for _, name := range []string{"first", "held"} {
				t.Run(name, func(t *testing.T) {
					rec := httptest.NewRecorder()
					body := fmt.Sprintf(`{"model":"claude/claude-sonnet-5","max_tokens":100,"stream":%v,"messages":[{"role":"user","content":"hi"}]}`, stream)
					s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
					checkQuotaResetHint(t, rec, reset)
					if !strings.Contains(rec.Body.String(), "weekly limit") {
						t.Fatalf("lost vendor's reason: %s", rec.Body)
					}
					if rec.Header().Get("Retry-After") != "60" {
						t.Fatalf("Retry-After %q, want capped 60", rec.Header().Get("Retry-After"))
					}
				})
			}
		})
	}
}

// A known window can hold the whole pool before any upstream is asked.
func TestQuotaResetHintUsageCap(t *testing.T) {
	reset := time.Now().Add(3 * time.Hour)
	refusingClaude(t, reset)
	old := allowances
	allowances = func(string) map[string]provider.Allowance {
		return map[string]provider.Allowance{"a@example.com": {{Used: 75, Resets: reset, Span: 5 * time.Hour}}}
	}
	t.Cleanup(func() { allowances = old })
	if err := provider.SetAccountCap("claude", "a@example.com", 70); err != nil {
		t.Fatal(err)
	}
	s := New()
	t.Cleanup(s.subscription.abortAll)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude/claude-sonnet-5","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)))
	checkQuotaResetHint(t, rec, reset)
	if !strings.Contains(rec.Body.String(), "usage cap reached") {
		t.Fatalf("not a cap refusal: %s", rec.Body)
	}
}

// Both initial failover and the next refusal must use the first eligible
// reset, not the last vendor's deadline or a guessed cooldown.
func TestQuotaResetHintPool(t *testing.T) {
	for _, tc := range []struct {
		name       string
		second     string
		known      bool
		wantStatus int
		path       string
		pass       bool
	}{
		{name: "both spent", second: "quota", known: true, wantStatus: 429},
		{name: "unknown reset", second: "unknown", wantStatus: 429},
		{name: "rate limit", second: "rate", wantStatus: 429},
		{name: "fallback answers", second: "ok", wantStatus: 200},
		{name: "chat protocol", second: "quota", wantStatus: 429, path: "/v1/chat/completions"},
		{name: "pass errors", second: "quota", wantStatus: 429, pass: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			now := time.Now().Truncate(time.Second)
			first, later := now.Add(2*time.Hour), now.Add(3*time.Hour)
			var asked atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				asked.Add(1)
				w.Header().Set("Content-Type", "application/json")
				kind, reset := "quota", first
				if r.Header.Get("x-api-key") == "b-key" {
					kind, reset = tc.second, later
				}
				if kind == "ok" {
					io.WriteString(w, `{"id":"m1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
					return
				}
				msg := "you have reached your weekly usage limit"
				if kind == "rate" {
					msg = "Rate limited. Please try again later."
				}
				w.WriteHeader(429)
				if kind == "unknown" {
					fmt.Fprintf(w, `{"type":"error","error":{"type":"rate_limit_error","message":%q}}`, msg)
				} else {
					fmt.Fprintf(w, `{"type":"error","error":{"type":"rate_limit_error","message":%q,"resets_at":%d}}`, msg, reset.Unix())
				}
			}))
			t.Cleanup(up.Close)
			p := provider.Provider{ID: "mockquota", Name: "Mock quota", Key: "a-key", Keys: []provider.KeyAccount{{Key: "b-key"}}, Models: []string{"m"}, Anthropic: up.URL + "/v1", Routing: provider.Ordered, PassErrors: tc.pass}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			s := New()
			path := tc.path
			if path == "" {
				path = "/v1/messages"
			}
			post := func() *httptest.ResponseRecorder {
				rec := httptest.NewRecorder()
				s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(`{"model":"mockquota/m","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)))
				return rec
			}
			rec := post()
			if rec.Code != tc.wantStatus {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body)
			}
			if tc.known {
				if got := asked.Load(); got != 2 {
					t.Fatalf("asked %d keys, want 2", got)
				}
				checkQuotaResetHint(t, rec, first)
				rec = post()
				checkQuotaResetHint(t, rec, first)
				if got := asked.Load(); got != 2 {
					t.Fatalf("asked %d keys on held response, want still 2", got)
				}
			} else if strings.Contains(rec.Body.String(), "(reset after ") {
				t.Fatalf("unexpected countdown: %s", rec.Body)
			}
		})
	}
}

func TestQuotaResetHintPreservesError(t *testing.T) {
	b := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"quota & limit","code":"spent"},"request_id":"req-1"}`)
	back := time.Now().Add(time.Hour)
	out := withQuotaResetHint(b, back)
	msg := gjson.GetBytes(out, "error.message").String()
	if !strings.HasPrefix(msg, "quota & limit"+magpieSays) || !strings.Contains(msg, "(reset after ") {
		t.Fatalf("message %q", msg)
	}
	if restored := setErrorField(out, "message", "quota & limit"); string(restored) != string(b) {
		t.Fatalf("other fields changed: %s", restored)
	}
	if twice := withQuotaResetHint(out, back); string(twice) != string(out) {
		t.Fatalf("duplicate hint: %s", twice)
	}
	if expired := withQuotaResetHint(b, time.Now().Add(-time.Second)); string(expired) != string(b) {
		t.Fatalf("expired hint: %s", expired)
	}
}

// An account held at its cap by the turn before it, after the plan let
// the turn in (nearCapHeld), is refused with the same countdown: the
// window's renewal, as when the plan holds it.
func TestQuotaResetHintNearCap(t *testing.T) {
	codexSignedIn(t)
	b := newNearCapBackend(t, map[string]float64{"acct-1": 89}, map[string]float64{"acct-1": 92}, map[string]float64{"acct-1": 92})
	nearCapAccounts(t, 90, 89)
	srv := New()
	out := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"codex/gpt-5.5","max_tokens":50,"messages":[{"role":"user","content":"ping"}]}`)))
			out <- rec
		}()
	}
	waitFor(t, "one turn sent, one waiting", func() bool { return len(b.sent()) == 1 && srv.nearCap.waiting() == 1 })
	close(b.open)
	var held *httptest.ResponseRecorder
	for range 2 {
		if rec := <-out; rec.Code == http.StatusTooManyRequests {
			held = rec
		} else if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
	}
	if held == nil || len(b.sent()) != 1 {
		t.Fatalf("sent %v; want one answered, one held at the cap", b.sent())
	}
	if !strings.Contains(held.Body.String(), "usage cap") {
		t.Fatalf("held as %s", held.Body)
	}
	checkQuotaResetHint(t, held, time.Unix(b.five, 0))
}
