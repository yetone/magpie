package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// axelburks on Discord: a model's own headers go on its provider's
// requests for that model, on every API (relayed, translated, streamed,
// count_tokens), after the provider's: a name both have goes once, as the
// model's, over the provider's and over magpie's own key header. The
// provider's other model, and a fallback provider serving the same model
// id, are sent the provider's headers alone.
func TestModelHeadersReachTheirModelOnEveryRoute(t *testing.T) {
	for _, c := range []struct {
		name, path, body string
		vendor           provider.Protocol
	}{
		{"messages relayed", "/v1/messages", `{"model":"fake/MODEL","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, provider.Anthropic},
		{"messages to chat", "/v1/messages", `{"model":"fake/MODEL","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, provider.Chat},
		{"chat", "/v1/chat/completions", `{"model":"fake/MODEL","messages":[{"role":"user","content":"hi"}]}`, provider.Chat},
		{"chat stream", "/v1/chat/completions", `{"model":"fake/MODEL","stream":true,"messages":[{"role":"user","content":"hi"}]}`, provider.Chat},
		{"responses to chat", "/v1/responses", `{"model":"fake/MODEL","input":"hi"}`, provider.Chat},
		{"count_tokens", "/v1/messages/count_tokens", `{"model":"fake/MODEL","messages":[{"role":"user","content":"hi"}]}`, provider.Anthropic},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			up := &sentTo{}
			rhVendor(t, provider.Provider{ID: "fake", Name: "Fake", Key: "sk-upstream-0123456789", Models: []string{"sol", "luna"},
				Headers: map[string]string{"X-Route": "provider", "X-Fixed": "fixed"}}, c.vendor, up.answer(c.vendor, 200))
			auth := "Authorization"
			if c.vendor == provider.Anthropic {
				auth = "X-Api-Key"
			}
			if err := provider.SetModelHeaders("fake/sol", map[string]string{"x-route": "model", "X-Model": "sol", auth: "mine"}); err != nil {
				t.Fatal(err)
			}
			send := func(model string) http.Header {
				t.Helper()
				rec := httptest.NewRecorder()
				r := httptest.NewRequest("POST", c.path, strings.NewReader(strings.Replace(c.body, "MODEL", model, 1)))
				New().Handler().ServeHTTP(rec, r)
				if rec.Code != 200 {
					t.Fatalf("%s: %d %s", model, rec.Code, rec.Body)
				}
				h := up.last()
				if h == nil {
					t.Fatalf("%s: the vendor was sent nothing", model)
				}
				return h
			}
			h := send("sol")
			for k, want := range map[string]string{"X-Route": "model", "X-Model": "sol", "X-Fixed": "fixed", auth: "mine"} {
				if got := h.Values(k); len(got) != 1 || got[0] != want {
					t.Errorf("sol: %s = %q, want %q once", k, got, want)
				}
			}
			h = send("luna")
			if got := h.Values("X-Route"); len(got) != 1 || got[0] != "provider" {
				t.Errorf("luna: X-Route = %q, want the provider's", got)
			}
			if h.Get("X-Model") != "" || !strings.Contains(h.Get(auth), "sk-upstream-0123456789") {
				t.Errorf("luna was sent sol's headers: X-Model %q, %s %q", h.Get("X-Model"), auth, h.Get(auth))
			}
		})
	}
}

// A fallback provider serving the model by the same id is sent its own
// headers alone, not the first provider's model's.
func TestModelHeadersStayWithTheirProvider(t *testing.T) {
	fresh(t)
	noRests()
	t.Cleanup(noRests)
	first, spare := &sentTo{}, &sentTo{}
	rhVendor(t, provider.Provider{ID: "ag", Name: "AG", Key: "k", Models: []string{"sol"}, Fallback: []string{"spare/sol"}},
		provider.Anthropic, first.answer(provider.Anthropic, 429))
	rhVendor(t, provider.Provider{ID: "spare", Name: "Spare", Key: "k", Models: []string{"sol"}},
		provider.Chat, spare.answer(provider.Chat, 200))
	if err := provider.SetModelHeaders("ag/sol", map[string]string{"X-Endpoint": "ep-1"}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"ag/sol","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if h := first.last(); h == nil || h.Get("X-Endpoint") != "ep-1" {
		t.Errorf("the model's provider was sent %v", h)
	}
	if h := spare.last(); h == nil || h.Get("X-Endpoint") != "" {
		t.Errorf("the fallback was sent the other provider's model's header: %v", h)
	}
}

// System One's decision model is sent its own headers too.
func TestModelHeadersReachSystemOne(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	var got http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = r.Header.Clone()
		mu.Unlock()
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"model":"jev","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "jev", Name: "Jev", Key: "upstream", Decide: up.URL + "/v1",
		Headers: map[string]string{"X-Route": "provider"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelHeaders("jev/jev-latest", map[string]string{"X-Route": "model"}); err != nil {
		t.Fatal(err)
	}
	code, out := sendTo(New(), "/v1/systemone", `{"model":"jev/jev-latest","state":{"message":"hi"},"questions":{}}`)
	if code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	mu.Lock()
	defer mu.Unlock()
	if v := got.Values("X-Route"); len(v) != 1 || v[0] != "model" {
		t.Errorf("System One: X-Route = %q, want the model's", v)
	}
}

// axelburks on Discord: a model of a relay with a System One URL beside
// its chat URL, set to be asked on System One, is asked there by its own
// name, and a conversation turn for it is refused as Jev's is.
func TestModelSetToSystemOneAskedThere(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	var paths, bodies []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		paths, bodies = append(paths, r.URL.Path), append(bodies, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"model":"ep-decide","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "ark", Name: "Ark", Key: "upstream", Chat: up.URL + "/api/v3",
		Decide: up.URL + "/api/v3", Models: []string{"ep-chat", "ep-decide"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelAPI("ark/ep-decide", "decide"); err != nil {
		t.Fatal(err)
	}
	code, out := sendTo(New(), "/v1/systemone", `{"model":"ark/ep-decide","state":{"message":"hi"},"questions":{}}`)
	if code != 200 {
		t.Fatalf("System One: %d %s", code, out)
	}
	mu.Lock()
	if len(paths) != 1 || !strings.HasSuffix(paths[0], "/systemone") || !strings.Contains(bodies[0], `"ep-decide"`) {
		t.Fatalf("asked %v %v", paths, bodies)
	}
	mu.Unlock()
	code, out = sendTo(New(), "/v1/chat/completions", `{"model":"ark/ep-decide","messages":[{"role":"user","content":"hi"}]}`)
	if code != 400 {
		t.Fatalf("a turn for it: %d %s", code, out)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 {
		t.Fatalf("the turn reached the vendor: %v", paths)
	}
}
