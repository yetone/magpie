package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// unlistedArgon signs in two Antigravity accounts, a and b, with the model
// lists given, fetches them, and answers requests: a refuses Gemini 4 as
// refuse says, everyone else answers. It returns the server, the turn and
// who was asked.
func unlistedArgon(t *testing.T, lists map[string]string, refuse func() (int, string)) (*Server, func(secret ...string) (int, string), func() []string) {
	t.Helper()
	fresh(t)
	var logins []map[string]any
	for _, user := range []string{"a@example.com", "b@example.com"} {
		logins = append(logins, map[string]any{
			"agent": "antigravity", "user": user, "on": true,
			"auth": map[string]any{"access_token": user, "refresh_token": "ref-" + user, "project": "p1",
				"expiry_date": time.Now().Add(time.Hour).UnixMilli()},
		})
	}
	dir := filepath.Dir(provider.Path())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "logins.json"), mustJSON(logins), 0o600); err != nil {
		t.Fatal(err)
	}
	old := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		var body string
		switch {
		case strings.HasSuffix(r.URL.Path, "latest-arm64-mac.yml"):
			body = "version: 2.9.1\n"
		case strings.Contains(r.URL.Path, ":loadCodeAssist"):
			body = `{"currentTier":{"id":"standard-tier"},"cloudaicompanionProject":"p1"}`
		case strings.Contains(r.URL.Path, ":fetchAvailableModels"):
			body = lists[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		default:
			return nil, fmt.Errorf("unexpected request to %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	t.Cleanup(func() { http.DefaultClient = old })
	p, _ := provider.Find("antigravity")
	if _, err := p.Fetch(t.Context()); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var asked []string
	s := New()
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		who := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		asked = append(asked, who)
		mu.Unlock()
		if who == "a@example.com" && strings.Contains(string(b), "argon") {
			code, body := refuse()
			rec.Header().Set("Content-Type", "application/json")
			rec.WriteHeader(code)
			io.WriteString(rec, body)
			return rec.Result(), nil
		}
		rec.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(rec, sse(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"from `+who+`"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3}}}`))
		return rec.Result(), nil
	})}
	turn := func(secret ...string) (int, string) {
		body := `{"model":"antigravity/gemini-4-argon","stream":false,"messages":[{"role":"user","content":"hi"}]}`
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		for _, k := range secret {
			r.Header.Set("Authorization", "Bearer "+k)
		}
		s.Handler().ServeHTTP(rec, r)
		return rec.Code, rec.Body.String()
	}
	whoAsked := func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := slices.Clone(asked)
		asked = nil
		return out
	}
	return s, turn, whoAsked
}

// Antigravity's real refusal of a model the account isn't given (#3027):
// a 404 NOT_FOUND from Code Assist.
const codeAssistNotFound = `{"error":{"code":404,"message":"Requested entity was not found.","status":"NOT_FOUND"}}`

// An account whose list names the model refuses it as one it isn't given,
// and the subscription's other account, whose list was fetched before it
// was given the model, serves it: the request goes on to that account,
// not back to the agent with the first one's 404 (01huadalang on Discord:
// Gemini 4 on one Antigravity account of several). The Routing page has
// it asked, not "unlisted, skipped", and the first rests for that model.
func TestUnlistedAccountAskedAfterModelRefused(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	s, turn, asked := unlistedArgon(t, map[string]string{
		"a@example.com": `{"models":{"gemini-3.8-flash":{},"gemini-4-argon":{"displayName":"Gemini 4 Argon"}}}`,
		"b@example.com": `{"models":{"gemini-3.8-flash":{}}}`,
	}, func() (int, string) { return 404, codeAssistNotFound })
	code, out := turn()
	if code != 200 || !strings.Contains(out, "from b@example.com") {
		t.Fatalf("got %d %s, want b's answer", code, out)
	}
	if got := asked(); !slices.Equal(got, []string{"a@example.com", "b@example.com"}) {
		t.Errorf("asked %v, want a then b", got)
	}
	rt := lastRoute(s)
	if !slices.ContainsFunc(rt.Order, func(w Weighed) bool { return w.Who == "b@example.com" && !w.Unlisted }) {
		t.Errorf("b is not in the route's order: %+v", rt.Order)
	}
	if slices.ContainsFunc(rt.Left, func(w Weighed) bool { return w.Who == "b@example.com" }) {
		t.Errorf("b is still left out as unlisted: %+v", rt.Left)
	}
	if n := len(rt.Tries); n != 2 || rt.Tries[0].Rest == nil {
		t.Errorf("tries %+v, want a's rested and b's", rt.Tries)
	}
	// a's other models aren't rested by it
	if _, ok := restOf("antigravity@a@example.com"); ok {
		t.Error("a's refusal of Gemini 4 rested the whole account")
	}
}

// A refusal that isn't about the model (Antigravity's own 403 and 429
// bodies) leaves the accounts whose list lacks it alone, as before: they
// would only refuse it too.
func TestUnlistedAccountNotAskedForOtherRefusals(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	for _, c := range []struct {
		name, body string
		code       int
	}{
		{"denied", `{"error":{"code":403,"message":"The caller does not have permission","status":"PERMISSION_DENIED"}}`, 403},
		{"capacity", `{"error":{"code":429,"message":"You have exhausted your capacity on this model. Your quota will reset after 3h2m1s.","status":"RESOURCE_EXHAUSTED"}}`, 429},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, turn, asked := unlistedArgon(t, map[string]string{
				"a@example.com": `{"models":{"gemini-3.8-flash":{},"gemini-4-argon":{"displayName":"Gemini 4 Argon"}}}`,
				"b@example.com": `{"models":{"gemini-3.8-flash":{}}}`,
			}, func() (int, string) { return c.code, c.body })
			code, out := turn()
			if code < 400 {
				t.Fatalf("got %d %s, want a's refusal", code, out)
			}
			if slices.Contains(asked(), "b@example.com") {
				t.Error("b, whose list lacks the model, was asked over a refusal not about the model")
			}
		})
	}
}

// A gateway key held to some accounts (#905) isn't given an unlisted
// account it may not use: the refusal reaches its agent, as before.
func TestUnlistedAccountHeldFromKey(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	_, turn, asked := unlistedArgon(t, map[string]string{
		"a@example.com": `{"models":{"gemini-3.8-flash":{},"gemini-4-argon":{"displayName":"Gemini 4 Argon"}}}`,
		"b@example.com": `{"models":{"gemini-3.8-flash":{}}}`,
	}, func() (int, string) { return 404, codeAssistNotFound })
	keys, secrets := newCaller(t, "Held")
	if _, err := access.Update("accounts-key", access.Change{Key: keys[0].ID, Accounts: []string{"antigravity/a@example.com"}}); err != nil {
		t.Fatal(err)
	}
	if code, out := turn(secrets[0]); code != 404 {
		t.Fatalf("got %d %s, want a's 404", code, out)
	}
	if got := asked(); !slices.Equal(got, []string{"a@example.com"}) {
		t.Errorf("asked %v, want a alone", got)
	}
}

// The unlisted account is asked before the provider's Fallback: it is the
// same model on the same subscription, which the fallback is not.
func TestUnlistedAccountBeforeFallback(t *testing.T) {
	t.Cleanup(provider.AntigravityBuiltinForTest()) // the retired built-in route
	_, turn, asked := unlistedArgon(t, map[string]string{
		"a@example.com": `{"models":{"gemini-3.8-flash":{},"gemini-4-argon":{"displayName":"Gemini 4 Argon"}}}`,
		"b@example.com": `{"models":{"gemini-3.8-flash":{}}}`,
	}, func() (int, string) { return 404, codeAssistNotFound })
	if err := provider.Save(provider.Provider{ID: "antigravity", Fallback: []string{"antigravity/gemini-3.8-flash"}}); err != nil {
		t.Fatal(err)
	}
	code, out := turn()
	if code != 200 || !strings.Contains(out, "from b@example.com") || !strings.Contains(out, "gemini-4-argon") {
		t.Fatalf("got %d %s, want b's Gemini 4", code, out)
	}
	if got := asked(); !slices.Equal(got, []string{"a@example.com", "b@example.com"}) {
		t.Errorf("asked %v, want a then b, the fallback not reached", got)
	}
}
