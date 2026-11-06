package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Every one of magpie's ChatGPT accounts used up, Codex's turn routed
// through them: Codex gets the 429 the backend gave, typed
// usage_limit_reached with its plan and reset and the x-codex-* windows
// beside it, as it gets it on its own sign-in, so it shows its usage
// limit and when it ends. Re-typed rate_limit_error with the headers
// dropped, Codex took it for a retryable 429 and stopped on "exceeded
// retry limit" (bitssly on X). Asked again with both resting, the same.
func TestCodexEveryAccountOutToldAsTheBackend(t *testing.T) {
	for _, x := range []struct {
		name, body string
		status     int
	}{
		{"usage_limit_reached", `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_at":4102444800,"resets_in_seconds":7200}}`, 429},
		{"usage_not_included", `{"error":{"type":"usage_not_included","message":"To use Codex with your ChatGPT plan, upgrade to Plus."}}`, 403},
	} {
		t.Run(x.name, func(t *testing.T) {
			codexSignedIn(t, "spare@example.com")
			var asked []string
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				asked = append(asked, r.Header.Get("chatgpt-account-id"))
				w.Header()["Content-Type"] = nil
				w.Header().Set("x-codex-primary-used-percent", "100")
				w.Header().Set("x-codex-primary-window-minutes", "300")
				w.Header().Set("x-codex-primary-reset-at", "4102444800")
				w.WriteHeader(x.status)
				io.WriteString(w, x.body)
			}))
			t.Cleanup(up.Close)
			was := provider.CodexBase
			provider.CodexBase = up.URL + "/backend-api/codex"
			t.Cleanup(func() { provider.CodexBase = was })
			if _, routed := codexAccounts(httptest.NewRequest("POST", CodexPath+"/responses", nil), "gpt-5.5"); !routed {
				t.Fatal("two accounts on: Codex's turn isn't routed")
			}
			var want map[string]any
			json.Unmarshal([]byte(x.body), &want)
			for i := range 2 {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"ping"}`))
				req.Header.Set("Authorization", "Bearer chatgpt-token")
				req.Header.Set("chatgpt-account-id", "acct-1")
				New().Handler().ServeHTTP(rec, req)
				if rec.Code != x.status {
					t.Fatalf("turn %d: %d %s", i, rec.Code, rec.Body)
				}
				var got map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("turn %d: %v: %s", i, err, rec.Body)
				}
				e, w := got["error"].(map[string]any), want["error"].(map[string]any)
				for k, v := range w {
					if k == "message" {
						continue
					}
					if e[k] != v {
						t.Errorf("turn %d: error.%s = %v, the backend said %v: %s", i, k, e[k], v, rec.Body)
					}
				}
				if m, _ := e["message"].(string); !strings.Contains(m, w["message"].(string)) {
					t.Errorf("turn %d: message %q", i, m)
				}
				if rec.Header().Get("x-codex-primary-used-percent") != "100" || rec.Header().Get("x-codex-primary-reset-at") != "4102444800" {
					t.Errorf("turn %d: the windows left behind: %v", i, rec.Header())
				}
				if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
					t.Errorf("turn %d: Content-Type %q", i, rec.Header().Get("Content-Type"))
				}
			}
			if len(asked) < 2 || asked[0] == asked[1] {
				t.Fatalf("asked %v: both accounts are to be tried", asked)
			}
		})
	}
}

// A ChatGPT account's 429 that isn't its usage limit (a rate limit per
// minute) keeps magpie's shape: Codex retries it, as it should.
func TestCodexRateLimitNotToldAsUsedUp(t *testing.T) {
	p := provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex"}}
	if codexOutRefusal(p, provider.Responses, 429, []byte(`{"error":{"type":"rate_limit_exceeded","message":"slow down"}}`)) {
		t.Fatal("a rate limit told as used up")
	}
	if codexOutRefusal(provider.Provider{ID: "k"}, provider.Responses, 429, []byte(`{"error":{"type":"usage_limit_reached"}}`)) {
		t.Fatal("a key's 429 told as a ChatGPT account's")
	}
	if codexOutRefusal(p, provider.Chat, 429, []byte(`{"error":{"type":"usage_limit_reached"}}`)) {
		t.Fatal("Chat Completions told in the ChatGPT backend's shape")
	}
	if !codexOutRefusal(p, provider.Responses, 429, []byte(`{"error":{"type":"usage_limit_reached"}}`)) {
		t.Fatal("a used-up ChatGPT account not told as the backend")
	}
}
