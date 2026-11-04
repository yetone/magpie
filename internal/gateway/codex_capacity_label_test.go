package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// 蛋仔 on Discord: a Codex turn over two ChatGPT accounts, the first's
// model at capacity (529), the last out of its allowance. Codex is told
// the overload ("Selected model is at capacity. Please try a different
// model."), as it should be, but the Requests row read "529 ·
// usage_limit_reached": the status and words were the overload's, the
// kind the last account's. The row's kind is the failure Codex was told,
// and only the account out of its allowance rests for long.
func TestCodexCapacityNotLabelledUsageLimit(t *testing.T) {
	for name, overload := range map[string]string{
		"backend's overload":     overloadedStatus,
		"the words Codex showed": `{"error":{"type":"server_error","code":"server_is_overloaded","message":"Selected model is at capacity. Please try a different model."}}`,
	} {
		t.Run(name, func(t *testing.T) {
			codexSignedIn(t, "spare@example.com")
			var tried []string
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.ReadAll(r.Body)
				acct := r.Header.Get("chatgpt-account-id")
				tried = append(tried, acct)
				w.Header().Set("Content-Type", "application/json")
				if acct == "acct-1" {
					w.WriteHeader(529)
					io.WriteString(w, overload)
					return
				}
				w.WriteHeader(429)
				io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_in_seconds":7200}}`)
			}))
			t.Cleanup(up.Close)
			was := provider.CodexBase
			provider.CodexBase = up.URL + "/backend-api/codex"
			t.Cleanup(func() { provider.CodexBase = was })
			pause := retryPause
			retryPause = time.Millisecond
			t.Cleanup(func() { retryPause = pause })

			code, body := codexPost(t, oneAccountTurn)
			if code != 529 || strings.Contains(strings.ToLower(body), "usage limit") {
				t.Fatalf("Codex told %d %s (tried %v)", code, body, tried)
			}
			if tried[0] != "acct-1" || tried[len(tried)-1] != "acct-2" {
				t.Fatalf("tried %v", tried)
			}
			rec := lastUsage(t)
			if rec.Status != 529 || rec.ErrType == "usage_limit_reached" || !strings.Contains(strings.ToLower(rec.Error), "overloaded") && !strings.Contains(rec.Error, "at capacity") {
				t.Fatalf("row: %d · %q · %q", rec.Status, rec.ErrType, rec.Error)
			}
			if rec.ErrType != "server_error" {
				t.Fatalf("row's kind %q, want the overload's server_error", rec.ErrType)
			}
			// the overloaded account sits out a moment at most; the one out
			// of its allowance rests till ChatGPT says it is back
			restingUntil.Lock()
			defer restingUntil.Unlock()
			long := 0
			for id, u := range restingUntil.m {
				switch d := time.Until(u); {
				case d > time.Hour:
					long++
				case d > overloadRestMost:
					t.Errorf("%s rests %v for an overload", id, d)
				}
			}
			if long != 1 {
				t.Errorf("%d accounts rest for hours, want the one out of its allowance: %v", long, restingUntil.m)
			}
		})
	}
}
