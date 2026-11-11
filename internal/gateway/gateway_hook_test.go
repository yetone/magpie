package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

func TestAsyncGatewayResponseDoesNotHoldStream(t *testing.T) {
	t.Setenv("MAGPIE_BUN", testenv.Bun(t))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Cleanup(plugin.Settle)
	generated := make(chan struct{})
	first := make(chan string, 1)
	completed := make(chan map[string]any, 1)
	release := make(chan struct{})
	notified := make(chan struct{}, 1)
	observer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		json.NewDecoder(r.Body).Decode(&payload)
		completed <- payload
		select {
		case <-release:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer func() { close(release); observer.Close() }()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"id":"c1","model":"m1","choices":[{"index":0,"delta":{"content":"live answer"}}]}`))
		w.(http.Flusher).Flush()
		select {
		case <-generated:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, sse(`data: {"id":"c1","model":"m1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, "data: [DONE]"))
	}))
	defer upstream.Close()
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "synthetic", Models: []string{"m1"}, Chat: upstream.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(t.TempDir(), "observer.hooks.js")
	script := `export async function onRequest(body,ctx){ctx.state.question=ctx.query;return body}
export async function onResponse(body,ctx){await fetch(ctx.options.url,{method:"POST",body:JSON.stringify({body,success:ctx.success,question:ctx.state.question}),signal:ctx.signal})}`
	if err := os.WriteFile(abs, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Add(t.Context(), abs); err != nil {
		t.Fatal(err)
	}
	if err := plugin.SetOptions(abs, map[string]any{"url": observer.URL, "gatewayTimeoutMs": 5000}); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(lanGuard(New().Handler()))
	defer func() {
		close(generated)
		gw.CloseClientConnections()
		gw.Close()
	}()
	// Client reads the first delta while upstream generation is still blocked.
	go func() {
		req, _ := http.NewRequestWithContext(t.Context(), "POST", gw.URL+"/v1/chat/completions", strings.NewReader(`{"model":"fake/m1","stream":true,"messages":[{"role":"user","content":"question"}]}`))
		req.Header.Set("Authorization", "Bearer magpie")
		res, err := gw.Client().Do(req)
		if err != nil {
			first <- err.Error()
			notified <- struct{}{}
			return
		}
		defer res.Body.Close()
		buf := make([]byte, 1024)
		n, _ := res.Body.Read(buf)
		first <- string(buf[:n])
		io.Copy(io.Discard, res.Body)
		notified <- struct{}{}
	}()
	select {
	case text := <-first:
		if !strings.Contains(text, "live answer") {
			t.Fatalf("first delta: %s", text)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stream waited for completed generation")
	}
	generated <- struct{}{}
	select {
	case payload := <-completed:
		if payload["success"] != true || payload["question"] != "question" || !strings.Contains(string(raw(payload["body"])), "live answer") {
			t.Fatalf("completion: %+v", payload)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("missing completion notification")
	}
	select {
	case <-notified:
	case <-time.After(2 * time.Second):
		t.Fatal("slow completion hook delayed client EOF")
	}
}

// Exercise the asynchronous host and server guard with all agent wire APIs.
// Service-specific contracts belong to companion plugin integration tests.
func TestAsyncGatewayThroughProtocols(t *testing.T) {
	t.Setenv("MAGPIE_BUN", testenv.Bun(t))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Cleanup(plugin.Settle)
	upstream := make(chan []byte, 20)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		upstream <- b
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"id":"c1","model":"m1","choices":[{"index":0,"delta":{"content":"recalled answer"}}]}`, `data: {"id":"c1","model":"m1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`, "data: [DONE]"))
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "upstream-secret", Models: []string{"m1"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	queries := make(chan map[string]any, 20)
	contextService := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer context-test" {
			t.Error("context service authorization missing")
		}
		var payload map[string]any
		json.NewDecoder(r.Body).Decode(&payload)
		queries <- payload
		w.Header().Set("Content-Type", "application/json")
		var content any = "Recalled Quartz codename: QUARTZ-8413 (" + payload["session"].(string) + ")"
		switch payload["query"] {
		case "empty":
			content = ""
		case "invalid":
			content = 123
		case "denied":
			w.WriteHeader(http.StatusForbidden)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"context": content})
	}))
	defer contextService.Close()
	abs := filepath.Join(t.TempDir(), "recall.hooks.js")
	if err := os.WriteFile(abs, []byte(`
export async function onRequest(body,ctx) {
 const response=await fetch(ctx.options.serverUrl,{method:"POST",headers:{"Authorization":ctx.options.authorization},body:JSON.stringify({session:ctx.session,query:ctx.query,origin:ctx.origin}),signal:ctx.signal})
 if(!response.ok)throw new Error("unavailable")
 const result=await response.json()
 if(typeof result.context!=="string")throw new Error("invalid")
 if(!result.context)return
 const text="\n<external-context>"+result.context+"</external-context>"
 const messages=ctx.protocol==="gemini"?body.contents:ctx.protocol==="responses"?body.input:body.messages
 const m=messages.findLast(m=>m.role==="user")
 if(typeof m.content==="string")m.content+=text
 else if(ctx.protocol==="gemini")m.parts.push({text})
 else m.content.push({type:ctx.protocol==="responses"?"input_text":"text",text})
 return body
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Add(t.Context(), abs); err != nil {
		t.Fatal(err)
	}
	if err := plugin.SetOptions(abs, map[string]any{"serverUrl": contextService.URL, "authorization": "Bearer context-test"}); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(lanGuard(New().Handler()))
	defer gw.Close()
	send := func(path, body, session string) []byte {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), "POST", gw.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer magpie-codex")
		req.Header.Set(SessionHeader, session)
		res, err := gw.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		reply, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 || !strings.Contains(string(reply), "recalled answer") {
			t.Fatalf("agent reply: %d %s", res.StatusCode, reply)
		}
		return <-upstream
	}
	cases := []struct{ name, path, body string }{
		{"chat", "/v1/chat/completions", `{"model":"fake/m1","stream":true,"messages":[{"role":"system","content":"<cwd>/tmp/quartz</cwd>"},{"role":"user","content":"Quartz codename?"}]}`},
		{"responses", "/v1/responses", `{"model":"fake/m1","stream":true,"instructions":"<cwd>/tmp/quartz</cwd>","input":[{"role":"user","content":[{"type":"input_text","text":"Quartz codename?"}]}]}`},
		{"codex-context", "/v1/responses", `{"model":"fake/m1","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"<environment_context><cwd>/tmp/quartz</cwd></environment_context>"},{"type":"input_text","text":"Quartz codename?"}]}]}`},
		{"anthropic", "/v1/messages", `{"model":"fake/m1","stream":true,"max_tokens":40,"system":"<cwd>/tmp/quartz</cwd>","messages":[{"role":"user","content":[{"type":"text","text":"Quartz codename?"}]}]}`},
		{"gemini", "/v1beta/models/fake/m1:streamGenerateContent", `{"systemInstruction":{"parts":[{"text":"<cwd>/tmp/quartz</cwd>"}]},"contents":[{"role":"user","parts":[{"text":"Quartz codename?"}]}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := string(send(c.path, c.body, "session-a"))
			if strings.Count(got, "QUARTZ-8413") != 1 || !strings.Contains(got, "session-a") || !strings.Contains(got, "Quartz codename?") {
				t.Fatalf("recall did not reach provider exactly once: %s", got)
			}
			if strings.Contains(got, "context-test") || strings.Contains(got, "magpie-codex") {
				t.Fatalf("plugin credentials leaked upstream: %s", got)
			}
			query := <-queries
			if query["query"] != "Quartz codename?" || query["session"] != "session-a" || query["origin"] != "local" {
				t.Fatalf("wrong preparation: %+v", query)
			}
		})
	}
	if t.Failed() {
		return
	}
	got := string(send(cases[0].path, cases[0].body, "session-b"))
	<-queries
	if !strings.Contains(got, "session-b") || strings.Contains(got, "session-a") {
		t.Fatalf("session context leaked: %s", got)
	}
	for _, query := range []string{"empty", "invalid", "denied"} {
		body := `{"model":"fake/m1","stream":true,"messages":[{"role":"user","content":"` + query + `"}]}`
		got := string(send(cases[0].path, body, "session-a"))
		<-queries
		if strings.Contains(got, "external-context") || !strings.Contains(got, query) {
			t.Fatalf("failed/empty recall changed prompt: %s", got)
		}
	}
	if err := plugin.SetOff(abs, true); err != nil {
		t.Fatal(err)
	}
	got = string(send(cases[0].path, cases[0].body, "session-a"))
	if strings.Contains(got, "external-context") {
		t.Fatalf("disabled plugin injected context: %s", got)
	}
	select {
	case q := <-queries:
		t.Fatalf("disabled plugin queried context service: %+v", q)
	default:
	}
}

func TestGatewayQueryUsesUserWords(t *testing.T) {
	for _, tc := range []struct {
		name     string
		messages []Message
		want     string
	}{
		{"Codex environment", []Message{userMsg(envContext, "Quartz codename?")}, "Quartz codename?"},
		{"agent notices", []Message{userMsg("Quartz codename?"), userMsg(pluginRef, taskNotification)}, "Quartz codename?"},
		{"tool-only increment", []Message{{Role: "user", Parts: []Part{{Kind: ToolResult, Text: "QUARTZ-8413"}}}}, ""},
		{"context only", []Message{userMsg(envContext)}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := gatewayQuery(&Request{Messages: tc.messages}); got != tc.want {
				t.Fatalf("query %q, want %q", got, tc.want)
			}
		})
	}
}

// Exercise provenance through authentication and the real Bun lifecycle; a
// client's body/header claims cannot replace the host's classification.
func TestAsyncGatewayOrigin(t *testing.T) {
	t.Setenv("MAGPIE_BUN", testenv.Bun(t))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Cleanup(plugin.Settle)
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := access.Update("add-key", access.Change{Name: "Shared"})
	if err != nil {
		t.Fatal(err)
	}
	// a key of the user's own value, made for a friend, is a named key too
	friend, err := access.Update("add-key", access.Change{Name: "Friend", Secret: "a-friends-own-key-0042"})
	if err != nil {
		t.Fatal(err)
	}
	// the sharing key magpie makes and writes into this computer's agents
	if err := access.ConfigureLAN(true, false); err != nil {
		t.Fatal(err)
	}
	mine := access.LANSecret()
	if mine == "" || mine == key {
		t.Fatalf("no default sharing key: %q", mine)
	}
	// this computer's own network address, and a WSL distro's NAT network
	oldOwn, oldNets := ownAddr, WSLNets
	ownAddr = func(a netip.Addr) bool { return a == netip.MustParseAddr("192.168.50.7") }
	_, wslNet, _ := net.ParseCIDR("172.28.80.1/20")
	WSLNets = func() []*net.IPNet { return []*net.IPNet{wslNet} }
	t.Cleanup(func() { ownAddr, WSLNets = oldOwn, oldNets })
	abs := filepath.Join(t.TempDir(), "origin.hooks.mjs")
	script := `export function onRequest(body,ctx){ctx.state.origin=ctx.origin;body.origin=ctx.origin;return body}
export async function onResponse(reply,ctx){await fetch(ctx.options.url,{method:"POST",body:JSON.stringify({origin:ctx.origin,before:ctx.state.origin}),signal:ctx.signal})}`
	observed := make(chan map[string]string, 20)
	observer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]string
		json.NewDecoder(r.Body).Decode(&got)
		observed <- got
		w.WriteHeader(204)
	}))
	defer observer.Close()
	if err := os.WriteFile(abs, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Add(t.Context(), abs); err != nil {
		t.Fatal(err)
	}
	if err := plugin.SetOptions(abs, map[string]any{"url": observer.URL}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, peer, header, key, trust, want string }{
		{"local", "127.0.0.1:1234", "", "", "0", "local"},
		{"ipv6", "[::1]:1234", "", "", "0", "local"},
		{"named-loopback", "127.0.0.1:1234", "", key, "0", "gateway-key"},
		{"remote", "10.0.0.1:1234", "", key, "0", "gateway-key"},
		{"proxy", "127.0.0.1:1234", "X-Forwarded-For", key, "0", "gateway-key"},
		{"friend-own-value", "10.0.0.1:1234", "", friend, "0", "gateway-key"},
		{"magpie-key-loopback", "127.0.0.1:1234", "", mine, "0", "magpie-key"},
		{"magpie-key-own-address", "192.168.50.7:1234", "", mine, "0", "magpie-key"},
		{"magpie-key-wsl-nat", "172.28.80.9:1234", "", mine, "0", "magpie-key"},
		// the same key copied to another computer (Gateway keys' Copy) or
		// handed to a friend is not the owner's own agent
		{"magpie-key-elsewhere", "10.0.0.1:1234", "", mine, "0", "gateway-key"},
		{"magpie-key-proxied", "127.0.0.1:1234", "X-Forwarded-For", mine, "0", "gateway-key"},
		{"magpie-key-trusted-proxy", "127.0.0.1:1234", "Forwarded", mine, "1", "gateway-key"},
		{"trusted-proxy", "127.0.0.1:1234", "Forwarded", "", "1", "remote"},
		{"trusted-tunnel", "127.0.0.1:1234", "Tailscale-User-Login", "", "1", "remote"},
		{"unknown-peer", "malformed", "", "", "0", "remote"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MAGPIE_TRUST_PROXY", tc.trust)
			// The guard requires sharing for nonlocal peers, without opening a listener.
			t.Setenv("MAGPIE_ADDR", "0.0.0.0:3425")
			req := httptest.NewRequest("POST", "http://127.0.0.1/v1/chat/completions", strings.NewReader(`{"model":"fake/m1","origin":"local","messages":[{"role":"user","content":"hello"}]}`))
			req.RemoteAddr = tc.peer
			req.Header.Set("X-Magpie-Origin", "local")
			if tc.header != "" {
				req.Header.Set(tc.header, "remote-client")
			}
			if tc.key != "" {
				req.Header.Set("Authorization", "Bearer "+tc.key)
			}
			// An invalid peer cannot pass the guard. Check it directly as a defensive
			// classifier case; all accepted cases go through the complete lifecycle.
			if tc.name == "unknown-peer" {
				if got := gatewayOrigin(req); got != tc.want {
					t.Fatalf("origin=%q, want %q", got, tc.want)
				}
				return
			}
			w := httptest.NewRecorder()
			lanGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w, b, done := gatewayHooks(w, r, provider.Chat, []byte(`{"model":"fake/m1","origin":"local","messages":[{"role":"user","content":"hello"}]}`))
				defer done()
				var got map[string]any
				json.Unmarshal(b, &got)
				if got["origin"] != tc.want {
					t.Errorf("request origin=%v, want %s", got["origin"], tc.want)
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			})).ServeHTTP(w, req)
			if w.Code != 200 {
				t.Fatalf("guard status=%d: %s", w.Code, w.Body.String())
			}
			select {
			case got := <-observed:
				if got["origin"] != tc.want || got["before"] != tc.want {
					t.Fatalf("callback provenance=%v, want %s", got, tc.want)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("missing completion origin")
			}
		})
	}
}

// Codex hangs up as soon as it reads response.completed, while ChatGPT may
// hold the connection a moment longer. The reply was delivered whole, so
// onResponse still sees it, as a success: the run ends with the handler,
// not with the client's connection.
func TestGatewayHookSeesReplyAfterClientHangsUp(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	held := make(chan struct{})
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"type":"response.output_text.delta","delta":"pong"}`,
			`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"pong"}]}]}}`))
		w.(http.Flusher).Flush()
		select {
		case <-held:
		case <-r.Context().Done():
		}
	})
	defer close(held)
	t.Setenv("MAGPIE_BUN", testenv.Bun(t))
	t.Cleanup(plugin.Settle)
	completed := make(chan map[string]any, 1)
	observer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		json.NewDecoder(r.Body).Decode(&payload)
		completed <- payload
	}))
	defer observer.Close()
	abs := filepath.Join(t.TempDir(), "capture.hooks.js")
	if err := os.WriteFile(abs, []byte(`export async function onRequest(body){return body}
export async function onResponse(reply,ctx){await fetch(ctx.options.url,{method:"POST",body:JSON.stringify({reply,success:ctx.success}),signal:ctx.signal})}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Add(t.Context(), abs); err != nil {
		t.Fatal(err)
	}
	if err := plugin.SetOptions(abs, map[string]any{"url": observer.URL, "gatewayTimeoutMs": 5000}); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(New().Handler())
	defer gw.Close()
	ctx, hangUp := context.WithCancel(t.Context())
	defer hangUp()
	req, _ := http.NewRequestWithContext(ctx, "POST", gw.URL+CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"ping"}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	res, err := gw.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	read := ""
	buf := make([]byte, 4096)
	for !strings.Contains(read, "response.completed") {
		n, err := res.Body.Read(buf)
		read += string(buf[:n])
		if err != nil {
			t.Fatalf("stream ended before response.completed: %v %q", err, read)
		}
	}
	hangUp() // as Codex does
	res.Body.Close()
	select {
	case payload := <-completed:
		if payload["success"] != true || !strings.Contains(string(raw(payload["reply"])), "pong") {
			t.Fatalf("completion after hang-up: %+v", payload)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("onResponse never ran after the client hung up on a whole reply")
	}
}

// A hooks package with its own page saves its options from that page. The
// host hands each hook the options it loaded with, so the save starts the
// host again; one that didn't left the hook on the old options until magpie
// restarted (magpie-community/plugins#117's PowerContext page).
func TestGatewayHookSeesOptionsSavedFromItsPage(t *testing.T) {
	t.Setenv("MAGPIE_BUN", testenv.Bun(t))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Cleanup(plugin.Settle)
	dir := filepath.Join(t.TempDir(), "paged-hook")
	if err := os.MkdirAll(filepath.Join(dir, "page"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"package.json":    `{"name":"paged-hook","version":"0.0.1","magpie":{"hooks":"./paged.hooks.mjs","page":{"title":"Paged","file":"page/index.html"}}}`,
		"paged.hooks.mjs": `export function onRequest(body,ctx){body.tag=ctx.options.tag;return body}`,
		"page/index.html": `<!doctype html><title>Paged</title>`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := plugin.Add(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	if err := plugin.SetOptions(dir, map[string]any{"tag": "first"}); err != nil {
		t.Fatal(err)
	}
	tag := func() any {
		req := httptest.NewRequest("POST", "http://127.0.0.1/v1/chat/completions", nil)
		req.RemoteAddr = "127.0.0.1:1234"
		var got map[string]any
		w := httptest.NewRecorder()
		_, b, done := gatewayHooks(w, req, provider.Chat, []byte(`{"model":"fake/m1","messages":[{"role":"user","content":"hello"}]}`))
		done()
		json.Unmarshal(b, &got)
		return got["tag"]
	}
	if got := tag(); got != "first" {
		t.Fatalf("tag=%v before the page saved, want first", got)
	}
	if err := plugin.SetPageOptions(dir, map[string]any{"tag": "second"}); err != nil {
		t.Fatal(err)
	}
	if got := tag(); got != "second" {
		t.Fatalf("tag=%v after the page saved, want second", got)
	}
}

// An async hook is told, per request, which ones a subagent made, as the
// client says it: Claude Code's x-claude-code-agent-id (real headers of
// 2.1.312's subagent in #4655), Codex's x-openai-subagent: collab_spawn.
// The main conversation after a subagent in the same session is not marked,
// and neither is a request whose history merely mentions the Agent tool.
func TestHookKindFromClientHeaders(t *testing.T) {
	for _, tc := range []struct {
		name  string
		agent string
		h     http.Header
		want  string
	}{
		{"claude subagent", "claude", http.Header{"X-Claude-Code-Agent-Id": {"a1b2"}, "X-Claude-Code-Session-Id": {"s"}}, "subagent"},
		{"claude main, same session", "claude", http.Header{"X-Claude-Code-Session-Id": {"s"}}, ""},
		{"codex subagent", "codex", http.Header{"X-Openai-Subagent": {"collab_spawn"}}, "subagent"},
		{"codex review is not a subagent", "codex", http.Header{"X-Openai-Subagent": {"review"}}, ""},
		{"codex main", "codex", http.Header{}, ""},
		{"no client headers", "", http.Header{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/v1/messages", nil)
			r.Header = tc.h
			if got := hookKind(r, tc.agent); got != tc.want {
				t.Fatalf("kind %q, want %q", got, tc.want)
			}
		})
	}
}

// The kind reaches the plugin host's onRequest and the same request's
// onResponse, through the real gateway and Bun.
func TestAsyncGatewayHookKindReachesPlugin(t *testing.T) {
	t.Setenv("MAGPIE_BUN", testenv.Bun(t))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Cleanup(plugin.Settle)
	seen := make(chan string, 8)
	observer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen <- string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer observer.Close()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "synthetic", Models: []string{"m1"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(t.TempDir(), "kind.hooks.js")
	script := `export async function onRequest(body,ctx){ctx.state.kind=ctx.kind;return body}
export async function onResponse(body,ctx){await fetch(ctx.options.url,{method:"POST",body:JSON.stringify({request:ctx.state.kind,response:ctx.kind}),signal:ctx.signal})}`
	if err := os.WriteFile(abs, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Add(t.Context(), abs); err != nil {
		t.Fatal(err)
	}
	if err := plugin.SetOptions(abs, map[string]any{"url": observer.URL, "gatewayTimeoutMs": 5000}); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(lanGuard(New().Handler()))
	defer gw.Close()
	ask := func(h map[string]string) string {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), "POST", gw.URL+"/v1/chat/completions", strings.NewReader(`{"model":"fake/m1","messages":[{"role":"user","content":"question"}]}`))
		req.Header.Set("Authorization", "Bearer magpie")
		for k, v := range h {
			req.Header.Set(k, v)
		}
		res, err := gw.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		select {
		case s := <-seen:
			return s
		case <-time.After(15 * time.Second):
			t.Fatal("no completion notification")
		}
		return ""
	}
	sess := map[string]string{"x-claude-code-session-id": "parent", "User-Agent": "claude-cli/2.1.312 (external, sdk-cli)"}
	sub := map[string]string{"x-claude-code-session-id": "parent", "x-claude-code-agent-id": "child-1", "User-Agent": "claude-cli/2.1.312 (external, sdk-cli)"}
	for _, c := range []struct {
		name string
		h    map[string]string
		want string
	}{
		{"main", sess, `{"request":"","response":""}`},
		{"subagent", sub, `{"request":"subagent","response":"subagent"}`},
		{"main again", sess, `{"request":"","response":""}`},
	} {
		if got := ask(c.h); got != c.want {
			t.Errorf("%s: hook saw %s, want %s", c.name, got, c.want)
		}
	}
}
