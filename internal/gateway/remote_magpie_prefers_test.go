package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// A remote magpie's list says which API it asks each model on best
// (magpie_prefers), which native_endpoints, in its provider's order with
// Chat first, doesn't: a GPT whose provider there is best on Responses,
// and a routing group whose members are, are asked on Responses here too.
// Without it the magpie that has it as its provider took Chat for both,
// and wired Pi's openai/* and auto/* to openai-completions, where the
// vendor turns away the max effort Responses takes (OoopsHh on X). A model
// best on Chat there stays on Chat. Here the gateway is both computers'.
func TestRemoteMagpiePrefersResponses(t *testing.T) {
	var mu sync.Mutex
	var vendor, hops []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		vendor = append(vendor, r.URL.Path)
		mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			if strings.Contains(string(b), `"reasoning_effort":"max"`) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(400)
				io.WriteString(w, `{"error":{"message":"Unsupported value: 'reasoning_effort' does not support 'max' with this model.","type":"invalid_request_error"}}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"hello"}}]}`,
				`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, `data: [DONE]`))
		case strings.HasSuffix(r.URL.Path, "/responses"):
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(
				`data: {"type":"response.created","response":{"id":"r1","model":"gpt-6.2-astra"}}`,
				`data: {"type":"response.output_text.delta","delta":"hello"}`,
				`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(up.Close)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	// an OpenAI key at a relay given as its Responses URL, which serves Chat too
	if err := provider.Save(provider.Provider{ID: "openai", Name: "OpenAI", Key: "k", Models: []string{"gpt-6.2-astra"},
		Chat: up.URL + "/v1", Responses: up.URL + "/v1", BaseAPI: "responses"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "kimi", Name: "Kimi", Key: "k", Models: []string{"kimi-k3"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "auto", Name: "Auto", Members: []string{"openai/gpt-6.2-astra"}}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			mu.Lock()
			hops = append(hops, r.URL.Path)
			mu.Unlock()
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(remote.Close)
	id, err := provider.Add(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Chat: strings.TrimPrefix(remote.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	office, err := provider.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := office.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	live, _, _ := catalog.Live("office")
	prefers := map[string]string{}
	for _, m := range live {
		prefers[m.ID] = m.Prefers
	}
	want := map[string]string{"openai/gpt-6.2-astra": "responses", "group/auto": "responses", "kimi/kimi-k3": "chat"}
	for m, p := range want {
		if prefers[m] != p {
			t.Errorf("%s: the remote's list prefers %q, want %q (%v)", m, prefers[m], p, prefers)
		}
	}
	office, _ = provider.Find(id)
	for m, p := range map[string]provider.Protocol{"openai/gpt-6.2-astra": provider.Responses, "group/auto": provider.Responses, "kimi/kimi-k3": provider.Chat} {
		if n := office.Native(m); n != p {
			t.Errorf("%s is best asked on %q here, want %q", m, n, p)
		}
	}

	calls := []struct{ path, body, hop, vendor string }{
		// Pi on openai-responses, as it is now wired: max goes through
		{"/v1/responses", `{"model":"office/openai/gpt-6.2-astra","input":"hi","stream":true,"reasoning":{"effort":"max"}}`, "/v1/responses", "/v1/responses"},
		{"/v1/responses", `{"model":"office/group/auto","input":"hi","stream":true,"reasoning":{"effort":"max"}}`, "/v1/responses", "/v1/responses"},
		// a Messages client's request is turned into the remote's best
		// API here, once, not into Chat to go on as Chat
		{"/v1/messages", `{"model":"office/openai/gpt-6.2-astra","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, "/v1/responses", "/v1/responses"},
		// a model best on Chat there is turned into Chat
		{"/v1/messages", `{"model":"office/kimi/kimi-k3","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, "/v1/chat/completions", "/v1/chat/completions"},
	}
	for _, c := range calls {
		mu.Lock()
		vendor, hops = nil, nil
		mu.Unlock()
		code, body := post(t, c.path, c.body)
		mu.Lock()
		gotHops, gotVendor := slices.Clone(hops), slices.Clone(vendor)
		mu.Unlock()
		if code != 200 || !strings.Contains(body, "hello") {
			t.Fatalf("%s %s: %d %s", c.path, c.body, code, body)
		}
		if !slices.Equal(gotHops, []string{c.hop}) || !slices.Equal(gotVendor, []string{c.vendor}) {
			t.Errorf("%s %s: remote asked on %v, vendor on %v; want %s, %s", c.path, c.body, gotHops, gotVendor, c.hop, c.vendor)
		}
	}
}
