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

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/memdistill"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// agentRelay is a relay of three keys (sk-0, sk-A, sk-B) answering chat,
// responses, messages and count_tokens, and a second provider with one
// key (sk-S) for its fallback; the keys each call came with, in order,
// and the keys that answer 429.
func agentRelay(t *testing.T) (got func() []string, limited map[string]bool) {
	t.Helper()
	fresh(t)
	var mu sync.Mutex
	var seen []string
	limited = map[string]bool{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		k := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if k == "" {
			k = r.Header.Get("x-api-key")
		}
		mu.Lock()
		seen = append(seen, k)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if limited[k] {
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`)
			return
		}
		switch p := r.URL.Path; {
		case strings.HasSuffix(p, "/count_tokens"):
			io.WriteString(w, `{"input_tokens":5}`)
		case strings.HasSuffix(p, "/messages"):
			io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"from `+k+`"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
		case strings.HasSuffix(p, "/responses"):
			io.WriteString(w, `{"id":"resp_1","object":"response","status":"completed","model":"m","output":[{"type":"message","id":"m1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"from `+k+`","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
		default:
			io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"from `+k+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		}
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: up.URL, Responses: up.URL, Anthropic: up.URL, Key: "sk-0",
		Keys: []provider.KeyAccount{{Key: "sk-A"}, {Key: "sk-B"}}, Models: []string{"m"}, Routing: provider.Ordered, Fallback: []string{"spare/m"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "spare", Name: "Spare", Chat: up.URL, Responses: up.URL, Anthropic: up.URL, Key: "sk-S", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := seen
		seen = nil
		return out
	}, limited
}

// agentAsk sends path's body as agent's (its magpie-<agent> token) from
// this computer.
func agentAsk(t *testing.T, h http.Handler, agent, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer magpie-"+agent)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

var agentRoutes = []struct{ name, path, body, want string }{
	{"chat", "/v1/chat/completions", `{"model":"relay/m","messages":[{"role":"user","content":"hi"}]}`, "from "},
	{"responses", "/v1/responses", `{"model":"relay/m","input":"hi"}`, "from "},
	{"messages", "/v1/messages", `{"model":"relay/m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, "from "},
	{"count_tokens", "/v1/messages/count_tokens", `{"model":"relay/m","messages":[{"role":"user","content":"hi"}]}`, "input_tokens"},
}

// An agent set to use one key of a provider (nb5p on Discord: one relay
// key for Claude Code, another for Codex, so the relay shows each one's
// use) has every request on that key, on every route that picks one —
// chat, responses, messages, count_tokens — and the provider's other keys
// never; an agent set to none uses the provider as before.
func TestAgentAccountsHoldEachAgentToItsKey(t *testing.T) {
	got, _ := agentRelay(t)
	if err := provider.SetAgentAccount("relay", "claude", provider.KeyID("sk-A")); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAgentAccount("relay", "codex", provider.KeyID("sk-B")); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	for _, rt := range agentRoutes {
		for agent, key := range map[string]string{"claude": "sk-A", "codex": "sk-B", "opencode": "sk-0"} {
			for range 3 {
				w := agentAsk(t, h, agent, rt.path, rt.body)
				if w.Code != 200 || !strings.Contains(w.Body.String(), rt.want) {
					t.Fatalf("%s as %s: %d %s", rt.name, agent, w.Code, w.Body.String())
				}
				if keys := got(); !slices.Equal(keys, []string{key}) {
					t.Fatalf("%s as %s went on %v, want %s", rt.name, agent, keys, key)
				}
			}
		}
	}
	// let go, the agent uses the provider as every caller does
	if err := provider.SetAgentAccount("relay", "claude", ""); err != nil {
		t.Fatal(err)
	}
	w := agentAsk(t, h, "claude", "/v1/chat/completions", agentRoutes[0].body)
	if keys := got(); w.Code != 200 || !slices.Equal(keys, []string{"sk-0"}) {
		t.Fatalf("let go: %d %v", w.Code, keys)
	}
}

// The key an agent is set to use resting, its request goes on to the
// model's fallback, never to the provider's other keys; with no fallback
// it is refused, and the agent's other providers and the other agents
// are served as before. A key taken out of the provider holds nothing.
func TestAgentAccountsRestingKeyFallsBack(t *testing.T) {
	got, limited := agentRelay(t)
	if err := provider.SetAgentAccount("relay", "claude", provider.KeyID("sk-A")); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	body := agentRoutes[0].body
	limited["sk-A"] = true
	w := agentAsk(t, h, "claude", "/v1/chat/completions", body)
	keys := got()
	if w.Code != 200 || !strings.Contains(w.Body.String(), "from sk-S") {
		t.Fatalf("the agent's key limited: %d %s (%v)", w.Code, w.Body.String(), keys)
	}
	if !slices.Equal(keys, []string{"sk-A", "sk-S"}) {
		t.Fatalf("the agent's key limited went on %v", keys)
	}
	// resting now: straight to the fallback
	w = agentAsk(t, h, "claude", "/v1/chat/completions", body)
	if keys := got(); w.Code != 200 || slices.ContainsFunc(keys, func(k string) bool { return k == "sk-0" || k == "sk-B" }) {
		t.Fatalf("the agent's key resting: %d %s (%v)", w.Code, w.Body.String(), keys)
	}
	// another agent is served by the relay's keys as before
	w = agentAsk(t, h, "codex", "/v1/chat/completions", body)
	if keys := got(); w.Code != 200 || !slices.Equal(keys, []string{"sk-0"}) {
		t.Fatalf("another agent: %d %v", w.Code, keys)
	}
	// with no fallback, the agent is told its key is the one set
	p, _ := provider.Find("relay")
	p.Fallback = nil
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	w = agentAsk(t, h, "claude", "/v1/chat/completions", body)
	if keys := got(); w.Code < 400 || slices.ContainsFunc(keys, func(k string) bool { return k != "sk-A" }) {
		t.Fatalf("no fallback: %d %s (%v)", w.Code, w.Body.String(), keys)
	}
	// the key off: refused, saying it is the agent's choice
	limited["sk-A"] = false
	noRests()
	if err := provider.SetKeyOn("relay", provider.KeyID("sk-A"), false); err != nil {
		t.Fatal(err)
	}
	w = agentAsk(t, h, "claude", "/v1/chat/completions", body)
	if keys := got(); w.Code != 403 || !strings.Contains(w.Body.String(), "is set to use") || len(keys) != 0 {
		t.Fatalf("the agent's key off: %d %s (%v)", w.Code, w.Body.String(), keys)
	}
	// a key taken out holds nothing
	p, _ = provider.Find("relay")
	p.Keys = slices.DeleteFunc(p.Keys, func(k provider.KeyAccount) bool { return k.Key == "sk-A" })
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	w = agentAsk(t, h, "claude", "/v1/chat/completions", body)
	if keys := got(); w.Code != 200 || !slices.Equal(keys, []string{"sk-0"}) {
		t.Fatalf("the key gone: %d %v", w.Code, keys)
	}
}

// An agent's key and a gateway key's accounts hold a request together: a
// gateway key free of accounts is served on the agent's key, and the
// agent's key is a model list's as any key: the agent sees its models.
func TestAgentAccountsWithAGatewayKey(t *testing.T) {
	got, _ := agentRelay(t)
	if err := provider.SetAgentAccount("relay", "claude", provider.KeyID("sk-B")); err != nil {
		t.Fatal(err)
	}
	// package agent lists the agents (usage.Agents), and isn't linked here
	was := usage.PluginAgents
	usage.PluginAgents = func() []usage.Known {
		return []usage.Known{{ID: "claude", Names: []string{"claude"}, UA: []string{"claude-cli"}}}
	}
	t.Cleanup(func() { usage.PluginAgents = was })
	keys, secrets := newCaller(t, "Held")
	h := New().Handler()
	ask := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(agentRoutes[0].body))
		r.Header.Set("Authorization", "Bearer "+secrets[0])
		r.Header.Set("User-Agent", "claude-cli/2.1.316 (external, cli)")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := ask(); w.Code != 200 || !slices.Equal(got(), []string{"sk-B"}) {
		t.Fatalf("a free gateway key: %d %s", w.Code, w.Body.String())
	}
	// the gateway key held to another key of the provider: they share
	// none, so the relay serves neither, refused as a key held away from
	// a provider's every account is, saying both
	if _, err := access.Update("accounts-key", access.Change{Key: keys[0].ID, Accounts: []string{"relay/" + provider.KeyID("sk-A")}}); err != nil {
		t.Fatal(err)
	}
	if w := ask(); w.Code != 403 || len(got()) != 0 || !strings.Contains(w.Body.String(), `\"Held\"`) || !strings.Contains(w.Body.String(), "claude is set to use") {
		t.Fatalf("no key shared: %d %s", w.Code, w.Body.String())
	}
	// the models listed to the agent are its usual ones
	r := httptest.NewRequest("GET", "/v1/models", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Authorization", "Bearer magpie-claude")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"relay/m"`) || !strings.Contains(w.Body.String(), `"spare/m"`) {
		t.Fatalf("models: %d %s", w.Code, w.Body.String())
	}
}

// heldCtx is the context agentHeld gives a request of agent's: what a
// request's handler, and magpie's own asks made while serving it, run in.
func heldCtx(t *testing.T, agent string) context.Context {
	t.Helper()
	var ctx context.Context
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Authorization", "Bearer "+TokenFor(agent))
	agentHeld(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { ctx = r.Context() })).ServeHTTP(httptest.NewRecorder(), r)
	return ctx
}

// relayKeys is keys less the fallback's: a search the fake relay answers
// without streaming goes on to the model's fallback too (and rests the
// key, so each ask starts with noRests).
func relayKeys(keys []string) []string {
	return slices.DeleteFunc(keys, func(k string) bool { return k == "sk-S" })
}

// Magpie's own requests set to one key of a provider (nb5p on Discord:
// Claude Code and Codex each on a relay key, magpie's own on a third)
// are on that key, every one of them, whichever agent's request they are
// made for: Memory tidying conversations (its token, through the
// Handler), a web search by a model, an image description and a routing
// group's classifier asked through chat (in process, past the Handler,
// in the context of the agent's request). Unset, they go as before: the
// in-process ones on the agent's key, Memory on any.
func TestOwnRequestsHeldToMagpiesKey(t *testing.T) {
	got, _ := agentRelay(t)
	if err := provider.SetAgentAccount("relay", "claude", provider.KeyID("sk-A")); err != nil {
		t.Fatal(err)
	}
	s := New()
	h := s.Handler()
	memory := func() *httptest.ResponseRecorder {
		return agentAsk(t, h, memdistill.Agent, "/v1/messages", agentRoutes[2].body)
	}
	inProcess := map[string]func(context.Context){
		"search": func(ctx context.Context) { s.searchWith(ctx, provider.Provider{ID: "relay"}, "m", "q", "") },
		"vision": func(ctx context.Context) {
			r := httptest.NewRequestWithContext(ownAsk(magpieChose(ctx)), "POST", "http://magpie/v1/chat/completions", nil)
			r.Header.Set("User-Agent", VisionAgent)
			s.serve(httptest.NewRecorder(), r, provider.Chat, []byte(agentRoutes[0].body))
		},
		"classifier": func(ctx context.Context) {
			s.askWaited(ctx)(access.Identity{}, "relay/m", []string{"code", "chat"}, before{}, false, "hi")
		},
	}
	// unset: as before
	if w := memory(); w.Code != 200 || !slices.Equal(got(), []string{"sk-0"}) {
		t.Fatalf("memory, magpie unset: %d %s", w.Code, w.Body.String())
	}
	for name, ask := range inProcess {
		noRests()
		ask(heldCtx(t, "claude"))
		if keys := relayKeys(got()); len(keys) == 0 || slices.ContainsFunc(keys, func(k string) bool { return k != "sk-A" }) {
			t.Fatalf("%s for claude, magpie unset, went on %v", name, keys)
		}
	}
	if err := provider.SetAgentAccount("relay", OwnAgent, provider.KeyID("sk-B")); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if w := memory(); w.Code != 200 || !slices.Equal(got(), []string{"sk-B"}) {
			t.Fatalf("memory: %d %s", w.Code, w.Body.String())
		}
	}
	for name, ask := range inProcess {
		for _, agent := range []string{"claude", "codex"} {
			noRests()
			ask(heldCtx(t, agent))
			if keys := relayKeys(got()); len(keys) == 0 || slices.ContainsFunc(keys, func(k string) bool { return k != "sk-B" }) {
				t.Fatalf("%s for %s went on %v, want sk-B", name, agent, keys)
			}
		}
	}
	// the agent's own request stays on its key
	if w := agentAsk(t, h, "claude", "/v1/chat/completions", agentRoutes[0].body); w.Code != 200 || !slices.Equal(got(), []string{"sk-A"}) {
		t.Fatalf("claude's own request: %d %s", w.Code, w.Body.String())
	}
}

// Magpie's own requests are held as an agent's are: their key resting,
// a request goes to the model's fallback, never to the provider's other
// keys; their key off, it is refused, saying it is magpie's own
// requests' setting and where it is.
func TestOwnRequestsRestingKeyFallsBack(t *testing.T) {
	got, limited := agentRelay(t)
	if err := provider.SetAgentAccount("relay", OwnAgent, provider.KeyID("sk-B")); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	body := agentRoutes[2].body
	limited["sk-B"] = true
	w := agentAsk(t, h, memdistill.Agent, "/v1/messages", body)
	if keys := got(); w.Code != 200 || !slices.Equal(keys, []string{"sk-B", "sk-S"}) {
		t.Fatalf("magpie's key limited: %d %s (%v)", w.Code, w.Body.String(), keys)
	}
	limited["sk-B"] = false
	noRests()
	if err := provider.SetKeyOn("relay", provider.KeyID("sk-B"), false); err != nil {
		t.Fatal(err)
	}
	p, _ := provider.Find("relay")
	p.Fallback = nil
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	w = agentAsk(t, h, memdistill.Agent, "/v1/messages", body)
	if keys := got(); w.Code != 403 || !strings.Contains(w.Body.String(), "magpie's own requests are set to use") || !strings.Contains(w.Body.String(), "Accounts by agent") || len(keys) != 0 {
		t.Fatalf("magpie's key off: %d %s (%v)", w.Code, w.Body.String(), keys)
	}
}

// A plan's own search service is asked with the key magpie's own
// requests are set to use (ownAccountOn), and with none other while it
// is off; unset, with the plan's key as before.
func TestOwnAccountOnPicksMagpiesKey(t *testing.T) {
	agentRelay(t)
	p, _ := provider.Find("relay")
	if q, ok := ownAccountOn(*p); !ok || q.Key != "sk-0" {
		t.Fatalf("unset: %v %s", ok, q.Key)
	}
	if err := provider.SetAgentAccount("relay", OwnAgent, provider.KeyID("sk-B")); err != nil {
		t.Fatal(err)
	}
	p, _ = provider.Find("relay")
	if q, ok := ownAccountOn(*p); !ok || q.Key != "sk-B" {
		t.Fatalf("set: %v %s", ok, q.Key)
	}
	if err := provider.SetKeyOn("relay", provider.KeyID("sk-B"), false); err != nil {
		t.Fatal(err)
	}
	p, _ = provider.Find("relay")
	if q, ok := ownAccountOn(*p); ok {
		t.Fatalf("off: on %s", q.Key)
	}
}
