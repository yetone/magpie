package gateway

import (
	"encoding/base64"
	"encoding/json"
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
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// LeiWang_2000 on X: a gateway key may keep some accounts out instead of
// naming the ones it uses ("<provider>/!<account>"), so an account added
// after the key was saved is one it uses, and one kept out is never
// asked, on every route that spends an account: chat, Anthropic's
// messages, responses, count_tokens, /v1/models, images, videos, System
// One, and a ChatGPT account's turn on magpie's endpoint and on Codex's.

// keptOutUp stands in for a keyed provider on every route, noting the key
// each request came with.
type keptOutUp struct {
	*httptest.Server
	mu   sync.Mutex
	used []string
}

func (u *keptOutUp) take() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := u.used
	u.used = nil
	return out
}

func newKeptOutUp(t *testing.T) *keptOutUp {
	u := &keptOutUp{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if key == "" {
			key = r.Header.Get("x-api-key")
		}
		u.mu.Lock()
		u.used = append(u.used, key)
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		switch {
		case strings.HasPrefix(p, "/decide"):
			io.WriteString(w, `{"answers":{"intent":{"choice":"bug"}},"usage":{"input_tokens":10,"output_tokens":2}}`)
		case strings.HasSuffix(p, "/messages/count_tokens"):
			io.WriteString(w, `{"input_tokens":5}`)
		case strings.HasSuffix(p, "/messages"):
			io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"from `+key+`"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`)
		case strings.HasSuffix(p, "/images/generations"):
			io.WriteString(w, `{"created":1,"data":[{"b64_json":"`+base64.StdEncoding.EncodeToString(pngBytes)+`"}]}`)
		case strings.HasSuffix(p, "/videos"):
			io.WriteString(w, `{"id":"vid-1"}`)
		case strings.Contains(string(b), `"stream":true`):
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"from `+key+`"}}]}`,
				`data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`,
				`data: [DONE]`))
		default:
			io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"from `+key+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
		}
	}))
	t.Cleanup(u.Close)
	return u
}

func TestGatewayKeyKeptOutAccountsKeyedRoutes(t *testing.T) {
	fresh(t)
	up := newKeptOutUp(t)
	relay := provider.Provider{ID: "relay", Name: "Relay", Chat: up.URL + "/v1", Anthropic: up.URL,
		Key: "kA", Keys: []provider.KeyAccount{{Key: "kB"}}, Models: []string{"m", "gpt-image-1"}}
	if err := provider.Save(relay); err != nil {
		t.Fatal(err)
	}
	office := provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset,
		Chat: up.URL + "/v1", Key: "oA", Keys: []provider.KeyAccount{{Key: "oB"}}, Models: []string{"grok-imagine-video"}}
	if err := provider.Save(office); err != nil {
		t.Fatal(err)
	}
	jev := provider.Provider{ID: "jev", Name: "Jev", Decide: up.URL + "/decide/v1", Key: "jA", Keys: []provider.KeyAccount{{Key: "jB"}}, Models: []string{"jev-latest"}}
	if err := provider.Save(jev); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("relay", up.URL+"/v1", []catalog.Model{{ID: "m"}, {ID: "gpt-image-1"}}); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Most")
	// every account but these: all the providers have now
	if _, err := access.Update("accounts-key", access.Change{Key: keys[0].ID, Accounts: []string{
		"relay/!" + provider.KeyID("kA"), "relay/!" + provider.KeyID("kB"),
		"office/!" + provider.KeyID("oA"), "office/!" + provider.KeyID("oB"),
		"jev/!" + provider.KeyID("jA"), "jev/!" + provider.KeyID("jB"),
	}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	ask := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+secrets[0])
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	routes := []struct {
		name, path, body string
		// counted locally when no account may count it
		local bool
	}{
		{"chat", "/v1/chat/completions", `{"model":"relay/m","messages":[{"role":"user","content":"hi"}]}`, false},
		{"messages", "/v1/messages", `{"model":"relay/m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, false},
		{"responses", "/v1/responses", `{"model":"relay/m","input":"hi"}`, false},
		{"count_tokens", "/v1/messages/count_tokens", `{"model":"relay/m","messages":[{"role":"user","content":"hi"}]}`, true},
		{"images", "/v1/images/generations", `{"model":"relay/gpt-image-1","prompt":"a magpie"}`, false},
		{"videos", "/v1/videos", `{"model":"office/grok-imagine-video","prompt":"a magpie"}`, false},
		{"systemone", "/v1/systemone", `{"model":"jev/jev-latest","state":{"message":"hi"},"questions":{"intent":{"type":"choice"}}}`, false},
	}
	listed := func() bool {
		w := ask("GET", "/v1/models", "")
		return strings.Contains(w.Body.String(), `"relay/m"`)
	}

	// every account kept out: nothing is asked, nothing listed
	for _, c := range routes {
		w := ask("POST", c.path, c.body)
		if got := up.take(); len(got) != 0 {
			t.Fatalf("%s: a kept-out account was asked: %v (%d %s)", c.name, got, w.Code, w.Body.String())
		}
		if !c.local && w.Code == 200 {
			t.Fatalf("%s: served with every account kept out: %s", c.name, w.Body.String())
		}
	}
	if listed() {
		t.Fatal("/v1/models lists a provider whose every account is kept out")
	}

	// an account added after the key was saved is the key's, on every
	// route; the kept-out ones, first in the order, are never asked
	relay.Keys = append(relay.Keys, provider.KeyAccount{Key: "kC"})
	office.Keys = append(office.Keys, provider.KeyAccount{Key: "oC"})
	jev.Keys = append(jev.Keys, provider.KeyAccount{Key: "jC"})
	for _, p := range []provider.Provider{relay, office, jev} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range routes {
		w := ask("POST", c.path, c.body)
		if w.Code != 200 {
			t.Fatalf("%s: the account added later didn't serve: %d %s", c.name, w.Code, w.Body.String())
		}
		want := map[string]string{"videos": "oC", "systemone": "jC"}[c.name]
		if want == "" {
			want = "kC"
		}
		if got := up.take(); !slices.Equal(got, []string{want}) {
			t.Fatalf("%s: asked with %v, want %s alone", c.name, got, want)
		}
	}
	if !listed() {
		t.Fatal("/v1/models leaves out the provider of the account added later")
	}
}

// A ChatGPT account kept out is never asked, on magpie's endpoint and on
// Codex's, and one signed in after the key was saved serves.
func TestGatewayKeyKeptOutCodexAccounts(t *testing.T) {
	heads := twoAccounts(t)
	keys, secrets := newCaller(t, "Most")
	if _, err := access.Update("accounts-key", access.Change{Key: keys[0].ID, Accounts: []string{"codex/!me@example.com"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	post := func(path, model string) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(`{"model":"`+model+`","stream":true,"input":"ping"}`))
		req.Header.Set("Authorization", "Bearer "+secrets[0])
		s.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	accounts := func() []string {
		var out []string
		for _, h := range *heads {
			out = append(out, h.Get("chatgpt-account-id"))
		}
		*heads = nil
		return out
	}
	// me, first in the order, kept out: spare serves, me never asked
	for path, model := range pinPaths {
		for range 2 {
			if code, body := post(path, model); code != 200 || !strings.Contains(body, "from acct-2") {
				t.Fatalf("%s: %d %s", path, code, body)
			}
			if got := accounts(); !slices.Equal(got, []string{"acct-2"}) {
				t.Fatalf("%s: asked %v", path, got)
			}
		}
	}
	// both kept out: no account asked
	if _, err := access.Update("accounts-key", access.Change{Key: keys[0].ID, Accounts: []string{"codex/!me@example.com", "codex/!spare@example.com"}}); err != nil {
		t.Fatal(err)
	}
	for path, model := range pinPaths {
		if code, body := post(path, model); code == 200 {
			t.Fatalf("%s: served with every account kept out: %s", path, body)
		}
		if got := accounts(); len(got) != 0 {
			t.Fatalf("%s: a kept-out account was asked: %v", path, got)
		}
	}
	// a third account signed in after the key was saved serves
	codexSignInLater(t, "third@example.com", "acct-3")
	for path, model := range pinPaths {
		if code, body := post(path, model); code != 200 || !strings.Contains(body, "from acct-3") {
			t.Fatalf("%s: the account signed in later: %d %s", path, code, body)
		}
		if got := accounts(); !slices.Equal(got, []string{"acct-3"}) {
			t.Fatalf("%s: asked %v", path, got)
		}
	}
}

// codexSignInLater adds a ChatGPT account to magpie's saved sign-ins, as
// signing in from magpie does, after codexSignedIn.
func codexSignInLater(t *testing.T, email, acct string) {
	t.Helper()
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	path := filepath.Join(filepath.Dir(provider.Path()), "logins.json")
	var saved []map[string]any
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	saved = append(saved, map[string]any{"agent": "codex", "user": email, "on": true, "seen": time.Now(),
		"auth": map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
			"id_token":      claims(map[string]any{"email": email}),
			"access_token":  claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "who": acct}),
			"refresh_token": "r-" + acct, "account_id": acct}}})
	if err := os.WriteFile(path, mustJSON(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	provider.ForgetAccounts()
}

// A key's quotas (#2216) are told for every account but the ones it keeps
// out, one signed in later too.
func TestGatewayKeyKeptOutQuotaCards(t *testing.T) {
	fresh(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "http://127.0.0.1:1/v1", Key: "kA", Keys: []provider.KeyAccount{{Key: "kB", Name: "Bee"}, {Key: "kC", Name: "Sea"}}, Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	who := access.Identity{KeyID: "k", Quotas: true, Accounts: []string{"relay/!" + provider.KeyID("kB")}}
	s := newQuotaScope(who, []provider.Entry{{Provider: provider.Provider{ID: "relay"}, Model: "m"}})
	if !s.provider("relay") {
		t.Fatal("the provider's quota is the key's")
	}
	if s.card("relay", "Bee") || !s.card("relay", "Sea") {
		t.Fatal("Bee is kept out, Sea is not")
	}
}
