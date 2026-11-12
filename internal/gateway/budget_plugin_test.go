package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/budget"
	"github.com/yetone/magpie/internal/gwbudget"
)

// The budget point with plugins/budget on and off, for a key held to a
// limit beside one that isn't. On, the limited key is let in while it has
// something left and refused with a 429 after. Off, the limited key is
// refused with a 503 and nothing is sent, in the chat API and the search
// MCP server alike (the vision one: TestVisionMCPHeldToKeyLimit), and the
// refusal is recorded; the key
// with no limit and a keyless request from this computer go on as before.
// A refresh that turns the plugin on lets the limited key's next request
// in at once.
func TestKeyLimitsOnAndOff(t *testing.T) {
	fresh(t)
	budget.Forget()
	pinLimitClock(t)
	calls := limitedUpstream(t, 0)
	keys, secrets := newCaller(t, "Capped", "Free")
	setLimit(t, keys[0].ID, &access.Limit{Period: "day", Tokens: 700})
	s := New()
	h := s.Handler()
	call := func(secret string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq))
		if secret != "" {
			r.Header.Set("Authorization", "Bearer "+secret)
		} else {
			r.RemoteAddr = "127.0.0.1:5000"
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	capped := access.Identity{KeyID: keys[0].ID, KeyName: "Capped", Limit: &access.Limit{Period: "day", Tokens: 700}}
	mcp := func() error {
		r := httptest.NewRequest("POST", SearchMCPPath, nil).WithContext(access.WithIdentity(t.Context(), capped))
		_, err := s.searchMCPRun(r, "latest go", nil, nil)
		return err
	}

	// on
	if w := call(secrets[0]); w.Code != 200 {
		t.Fatal("on: within the limit", w.Code, w.Body.String())
	}
	if w := call(secrets[0]); w.Code != 200 {
		t.Fatal("on: within the limit", w.Code, w.Body.String())
	}
	if w := call(secrets[0]); w.Code != http.StatusTooManyRequests {
		t.Fatal("on: a spent key let in", w.Code, w.Body.String())
	}
	if calls.Load() != 2 {
		t.Fatalf("on: %d calls reached the provider", calls.Load())
	}

	// off
	withoutLimits(t)
	setLimit(t, keys[0].ID, &access.Limit{Period: "day", Tokens: 100000})
	w := call(secrets[0])
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "Key limits plugin") || w.Header().Get("X-Should-Retry") != "false" {
		t.Fatal("off: a limited key's request", w.Code, w.Body.String(), w.Header())
	}
	if calls.Load() != 2 {
		t.Fatal("off: a limited key's request reached the provider")
	}
	if rec := lastUsage(t); !rec.Rejected || rec.CallerKeyID != keys[0].ID || rec.ErrType != "gateway_key_limit_off" || rec.Status != 503 {
		t.Fatalf("off: refusal recorded as %+v", rec)
	}
	if err := mcp(); err == nil || !strings.Contains(err.Error(), "Key limits plugin") {
		t.Errorf("off: the search MCP server said %v, not that the plugin is off", err)
	}
	if w := call(secrets[1]); w.Code != 200 {
		t.Fatal("off: a key with no limit refused", w.Code, w.Body.String())
	}
	if w := call(""); w.Code != 200 {
		t.Fatal("off: a keyless loopback request refused", w.Code, w.Body.String())
	}

	// the gateway's refresh turns it on for the request that needs it
	var on func()
	stop := gwbudget.Refresh(func() { on = gwbudget.Limit(budget.Limiter{}) })
	defer stop()
	gwbudget.ForgetRefreshForTest()
	before := calls.Load()
	if w := call(secrets[0]); w.Code != 200 || on == nil || calls.Load() != before+1 {
		t.Fatal("a refresh that turned the plugin on still refused", w.Code, w.Body.String())
	}
	on()
}
