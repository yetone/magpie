package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// realCodexUsage is /wham/usage's answer for a Pro account on 2026-11-02,
// as the backend sent it, its email and ids taken out and its reset
// credits put at 0 (so no second call is made): one weekly window
// and no second, credits held. The endpoint answered this shape that day,
// so a card saying it couldn't read one is not the endpoint changing.
const realCodexUsage = `{"plan_type": "pro", "rate_limit": {"allowed": true, "limit_reached": false, "primary_window": {"used_percent": 0, "limit_window_seconds": 604800, "reset_after_seconds": 155647, "reset_at": 1793774287}, "secondary_window": null}, "code_review_rate_limit": null, "additional_rate_limits": null, "model_usage": {"gpt-6-astra": {"available": true, "available_at": null, "credits_would_enable": false}}, "credits": {"has_credits": true, "unlimited": false, "overage_limit_reached": false, "balance": "62500.0000000000", "approx_local_messages": [15625, 81250], "approx_cloud_messages": [2500, 15625]}, "spend_control": {"reached": false, "individual_limit": null}, "rate_limit_reached_type": null, "promo": null, "rate_limit_reset_credits": {"available_count": 0, "applicable_available_count": 0}}`

// cloudflareChallengePage is Cloudflare's challenge as it begins, the
// page it serves in place of an answer (the gateway's notapi_test.go,
// #1012).
const cloudflareChallengePage = `<!DOCTYPE html><html lang="en-US"><head><title>Just a moment...</title><meta http-equiv="Content-Type" content="text/html; charset=UTF-8"></head><body><div class="main-wrapper" role="main"><div class="main-content"><noscript>Enable JavaScript and cookies to continue</noscript></div></div></body></html>`

// codexUsageServing has /wham/usage answer code and body.
func codexUsageServing(t *testing.T, code int, body string) {
	t.Helper()
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(body, "<") {
			w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		}
		w.WriteHeader(code)
		w.Write([]byte(body))
	}))
	t.Cleanup(fake.Close)
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })
}

// A ChatGPT account's usage read that fails says why, in words the card
// can tell apart, not Go's bare status text or decode error (hmy on
// Discord: five ChatGPT accounts in use, each card "Allowance
// unavailable"). What the old words were matched for still holds: a 429
// or a 5xx keeps the last reading (passing), a 403 or a plain 401 doesn't,
// and the answer itself is still found under the words (codexExpired).
func TestCodexUsageSaysWhy(t *testing.T) {
	isolate(t)
	for _, c := range []struct {
		name    string
		code    int
		body    string
		want    string
		passing bool
	}{
		{"cloudflare", 403, cloudflareChallengePage, "ChatGPT's Cloudflare check turned the usage read away (HTTP 403 Forbidden)", false},
		// the backend's own 401 words, as it answers a token it can't read
		{"401 parse", 401, `{"detail":"Could not parse your authentication token. Please try signing in again."}`, "ChatGPT answered the usage read with HTTP 401 Unauthorized: Could not parse your authentication token. Please try signing in again.", false},
		{"401 bare", 401, `{"detail":"Unauthorized"}`, "ChatGPT answered the usage read with HTTP 401 Unauthorized: Unauthorized", false},
		{"403 detail object", 403, `{"detail":{"code":"deactivated_workspace"}}`, "ChatGPT answered the usage read with HTTP 403 Forbidden: deactivated_workspace", false},
		{"429", 429, `{"detail":"Too many requests"}`, "ChatGPT answered the usage read with HTTP 429 Too Many Requests: Too many requests", true},
		{"502 empty", 502, ``, "ChatGPT answered the usage read with HTTP 502 Bad Gateway", true},
		// Cloudflare's own 52x has no status text in Go
		{"522", 522, cloudflareChallengePage[:40], "ChatGPT answered the usage read with HTTP 522", true},
		// the real answer, cut off
		{"cut short", 200, realCodexUsage[:300], "ChatGPT's usage reply isn't one magpie reads: unexpected end of JSON input", false},
		{"not an object", 200, `[]`, "ChatGPT's usage reply isn't one magpie reads: json: cannot unmarshal array", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			codexUsageServing(t, c.code, c.body)
			_, _, _, _, _, err := codexWindows(context.Background(), "tok", "acct")
			if err == nil || !strings.HasPrefix(err.Error(), c.want) {
				t.Fatalf("said %v, want it to begin %q", err, c.want)
			}
			if got := passing.MatchString(err.Error()); got != c.passing {
				t.Fatalf("passing %v, want %v: %q", got, c.passing, err)
			}
			var se *accountStatusError
			if c.code != 200 && (!errors.As(err, &se) || se.status != c.code) {
				t.Fatalf("the vendor's answer isn't under %q", err)
			}
		})
	}

	t.Run("network", func(t *testing.T) {
		old := CodexBase
		CodexBase = "http://127.0.0.1:1/backend-api/codex"
		t.Cleanup(func() { CodexBase = old })
		_, _, _, _, _, err := codexWindows(context.Background(), "tok", "acct")
		if err == nil || !strings.HasPrefix(err.Error(), "couldn't reach chatgpt.com for the usage read: ") || strings.Contains(err.Error(), "127.0.0.1:1/backend-api") || !passing.MatchString(err.Error()) {
			t.Fatalf("said %v", err)
		}
	})

	t.Run("the real answer reads", func(t *testing.T) {
		codexUsageServing(t, 200, realCodexUsage)
		plan, ws, _, credits, held, err := codexWindows(context.Background(), "tok", "acct")
		if err != nil || plan != "pro" || len(ws) != 1 || ws[0].Used != 0 || credits != "62500 credits" || held {
			t.Fatalf("plan %q windows %+v credits %q held %v err %v", plan, ws, credits, held, err)
		}
	})
}

// The account's own row says it too: LoginUsage's reading of a saved
// ChatGPT account turned away by Cloudflare carries the words.
func TestCodexLoginUsageSaysCloudflare(t *testing.T) {
	codexExpiredFakes(t, func(string) (int, string) { return 403, cloudflareChallengePage }, false)
	u := LoginUsage(context.Background(), "codex")
	for _, user := range []string{"me@example.com", "work@example.com"} {
		if q := u[user]; !strings.HasPrefix(q.Error, "ChatGPT's Cloudflare check turned the usage read away") {
			t.Fatalf("%s: %+v", user, q)
		}
	}
}
