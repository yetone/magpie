package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// seedKeptPlan saves provider alpha (model m1, answering from up; its
// Anthropic base is Ollama Cloud's, which makes its key a plan's, and is
// never called) with a
// key whose plan card is kept on disk with a 5 hours and a 7 days window,
// as the status line reads it.
func seedKeptPlan(t *testing.T, up string) {
	t.Helper()
	if err := provider.Save(provider.Provider{ID: "alpha", Name: "Alpha", Key: "alpha-one", Chat: up + "/v1", Anthropic: "https://ollama.com", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("alpha-one"))
	reset := time.Now().Add(2 * time.Hour)
	kept := map[string]any{"alpha/plan " + hex.EncodeToString(sum[:8]): map[string]any{"at": time.Now().Add(-time.Minute), "quota": provider.SubscriptionQuota{
		Provider: "alpha",
		Windows:  []provider.QuotaWindow{{Name: "5 hours", Used: 62, ResetsAt: &reset}, {Name: "7 days", Used: 41, ResetsAt: &reset}}}}}
	if err := os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "quotas.json"), mustJSON(kept), 0o600); err != nil {
		t.Fatal(err)
	}
	provider.ForgetKeptCardsForTest()
	provider.ForgetBalances()
	provider.ForgetPlanQuotas()
}

// Claude Code on another computer than the gateway (steven_4422: magpie in
// Docker, Claude Code on the Linux host) runs curl for its status line.
// The line is the CLI's own, the model of the session sent in the body
// first, and another machine is told it only with a gateway key.
func TestStatuslineEndpoint(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"ok","model":"m1","choices":[{"message":{"role":"assistant","content":"hi"}}]}`)
	}))
	t.Cleanup(up.Close)
	seedKeptPlan(t, up.URL)
	keys, secrets := newCaller(t, "Host", "Display", "Budget")
	if _, err := access.Update("access-key", access.Change{Key: keys[1].ID, ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := access.Update("limit-key", access.Change{Key: keys[2].ID, Limit: &access.Limit{Period: "day", Tokens: 1_000_000}}); err != nil {
		t.Fatal(err)
	}
	h := lanGuard(New().Handler())
	do := func(method, target, from, secret, body string) (int, string) {
		t.Helper()
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r.RemoteAddr = from
		if secret != "" {
			r.Header.Set("Authorization", "Bearer "+secret)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	const here, host = "127.0.0.1:5000", "192.168.1.9:5000"

	// a turn of the session, so it has a route
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"alpha/m1","messages":[{"role":"user","content":"hi"}]}`))
	req.RemoteAddr = here
	req.Header.Set(SessionHeader, "6f1d2c3a")
	if w := httptest.NewRecorder(); func() int { h.ServeHTTP(w, req); return w.Code }() != 200 {
		t.Fatal("the session's turn failed")
	}

	const cards = "alpha* 5h 62% 7d 41%"
	const claudeCode = `{"cwd":"/home/me","session_id":"6f1d2c3a","version":"2.1.296"}`
	// this computer: no key, as the CLI is
	if c, b := do("POST", StatuslinePath, here, "", claudeCode); c != 200 || b != "→ alpha/m1 | "+cards+"\n" {
		t.Errorf("POST from this computer: %d %q", c, b)
	}
	if c, b := do("GET", StatuslinePath+"?session=6f1d2c3a", here, "", ""); c != 200 || b != "→ alpha/m1 | "+cards+"\n" {
		t.Errorf("GET ?session= from this computer: %d %q", c, b)
	}
	// no session: the accounts alone; ?only= keeps those named
	if c, b := do("POST", StatuslinePath, here, "", ""); c != 200 || b != cards+"\n" {
		t.Errorf("POST with no body: %d %q", c, b)
	}
	if c, b := do("GET", StatuslinePath+"?only=nobody", here, "", ""); c != 200 || strings.TrimSpace(b) != "" {
		t.Errorf("GET ?only=nobody: %d %q", c, b)
	}
	if c, b := do("GET", StatuslinePath+"?only=alpha,x", here, "", ""); c != 200 || b != cards+"\n" {
		t.Errorf("GET ?only=alpha: %d %q", c, b)
	}

	// another machine (the Docker host): a gateway key or nothing
	for _, m := range []string{"GET", "POST"} {
		if c, b := do(m, StatuslinePath, host, "", claudeCode); c != http.StatusUnauthorized || strings.Contains(b, "alpha") {
			t.Errorf("%s from another machine with no key: %d %q", m, c, b)
		}
		if c, b := do(m, StatuslinePath, host, "not-a-key", claudeCode); c != http.StatusUnauthorized || strings.Contains(b, "alpha") {
			t.Errorf("%s from another machine with a wrong key: %d %q", m, c, b)
		}
	}
	if c, b := do("POST", StatuslinePath, host, secrets[0], claudeCode); c != 200 || b != "→ alpha/m1 | "+cards+"\n" {
		t.Errorf("POST from another machine with a key: %d %q", c, b)
	}
	// a read-only key reads the accounts' use, and is told no route
	if c, b := do("POST", StatuslinePath, host, secrets[1], claudeCode); c != 200 || b != cards+"\n" {
		t.Errorf("POST with a read-only key: %d %q", c, b)
	}
	// a key held to a budget is told none of the accounts
	if c, b := do("POST", StatuslinePath, host, secrets[2], claudeCode); c != http.StatusForbidden || strings.Contains(b, "alpha 5h") {
		t.Errorf("POST with a held key: %d %q", c, b)
	}
}
