package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// #1981 (321sssrt-bit): magpie shared on the local network with a gateway
// key held to a budget, models or accounts, and that key still read every
// account's quota and balance from /v1/magpie/quotas. A held key is
// someone else's to use: it is told none of this magpie's accounts, from
// this computer or another, only its own limit and the routes of its own
// requests. A key held to nothing is told what it was told before.
func TestHeldKeyToldNoAccounts(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":5}}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "upstream-secret", Chat: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Owner", "Budget", "Models", "Accounts")
	if _, err := access.Update("limit-key", access.Change{Key: keys[1].ID, Limit: &access.Limit{Period: "day", Tokens: 1_000_000}}); err != nil {
		t.Fatal(err)
	}
	if _, err := access.Update("models-key", access.Change{Key: keys[2].ID, Models: []string{"plan/*"}}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("plan")
	if err != nil {
		t.Fatal(err)
	}
	as := p.AccountIDs()
	if len(as) == 0 {
		t.Fatal("no account to hold a key to")
	}
	if _, err := access.Update("accounts-key", access.Change{Key: keys[3].ID, Accounts: []string{"plan/" + as[0].ID}}); err != nil {
		t.Fatal(err)
	}
	h := lanGuard(New().Handler())
	do := func(method, path, from, secret string, hdr ...string) (int, string) {
		var body io.Reader
		if method == "POST" && strings.HasPrefix(path, "/v1/chat") {
			body = strings.NewReader(chatReq)
		}
		r := httptest.NewRequest(method, path, body)
		r.RemoteAddr = from
		r.Header.Set("Authorization", "Bearer "+secret)
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	// each key makes a request in a session of its own
	for i, s := range secrets {
		if c, b := do("POST", "/v1/chat/completions", "192.168.1.9:5000", s, SessionHeader, "sess-"+keys[i].Name); c != 200 {
			t.Fatal(keys[i].Name, c, b)
		}
	}
	told := []struct{ method, path string }{
		{"GET", "/v1/magpie/quotas"},
		{"GET", "/v1/magpie/quotas/history"},
		{"GET", AccountsPath},
		{"GET", provider.RemoteCardsPath},
		{"POST", provider.RemoteRefreshPath},
		{"GET", "/v1/magpie/concurrency"},
	}
	route := func(from, secret, session string) any {
		t.Helper()
		c, b := do("GET", "/v1/magpie/route?session="+session, from, secret)
		var out map[string]any
		if c != 200 || json.Unmarshal([]byte(b), &out) != nil {
			t.Fatal("route:", c, b)
		}
		return out["route"]
	}
	for _, from := range []string{"127.0.0.1:5000", "192.168.1.9:5000"} {
		for i, s := range secrets {
			name := keys[i].Name
			for _, x := range told {
				c, b := do(x.method, x.path, from, s)
				if i == 0 {
					if c != 200 {
						t.Errorf("%s from %s, a key held to nothing: %s %s: %d %s", name, from, x.method, x.path, c, b)
					}
					continue
				}
				if c != http.StatusForbidden || !strings.Contains(b, provider.KeyHeldCode) || strings.Contains(b, "upstream") {
					t.Errorf("%s from %s: %s %s: %d %s", name, from, x.method, x.path, c, b)
				}
			}
			if c, b := do("GET", "/v1/magpie/limit", from, s); c != 200 {
				t.Errorf("%s from %s: its own limit: %d %s", name, from, c, b)
			}
			if route(from, s, "sess-"+name) == nil {
				t.Errorf("%s from %s: no route for its own session", name, from)
			}
			other := "sess-" + keys[(i+1)%len(keys)].Name
			if r := route(from, s, other); (i == 0) != (r != nil) {
				t.Errorf("%s from %s: another key's session %s: %v", name, from, other, r)
			}
			// the whole trace (#2606), every key's routes: this computer
			// alone, and never a held key, even from here
			c, b := do("GET", "/v1/magpie/trace", from, s)
			if want := i == 0 && from == "127.0.0.1:5000"; want != (c == 200) {
				t.Errorf("%s from %s: the trace: %d %s", name, from, c, b)
			} else if i > 0 && from == "127.0.0.1:5000" && !strings.Contains(b, provider.KeyHeldCode) {
				t.Errorf("%s from %s: the trace refused without saying the key is held: %d %s", name, from, c, b)
			}
		}
	}
}
