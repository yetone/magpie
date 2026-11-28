package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// keySurfaceNeither is every route that is neither a model's nor an MCP
// server's: a key held to no model, or to no MCP server, reaches it as
// any key does. A route the router has in none of keyModelRoutes,
// keyMCPRoutes and this, or in two, fails TestKeySurfacesEveryRoute.
var keySurfaceNeither = []string{
	"GET /{$}", "GET /v1", "GET /v1/{$}", "/",
	// usage, a key's own limits and slots, the Library's skills
	"GET /v1/magpie/usage", "GET /v1/magpie/usage/requests", "POST /v1/magpie/usage/requests",
	"GET /v1/magpie/limit", "GET /v1/magpie/concurrency", "GET /v1/magpie/library",
	"GET " + SkillsArchivePath, "GET " + SkillsPullPath,
	// pages and files answered without a key, joins and proofs
	"GET " + KeyPagePath, "GET " + keyPageData, "GET " + ChatPagePath, "GET " + ChatPagePath + "/{path...}",
	"GET " + JoinPagePath, "GET " + joinData, "POST " + joinData, "GET " + CAPath,
	"POST " + provider.RemoteProvePath, "POST " + provider.RemoteUpdatePath,
	// this magpie's own: debugging, a restart, the admin API, issuing keys
	"GET " + ClaudeRunsPath, "GET " + HeapPath, "GET " + CPUPath, "GET " + GoroutinesPath, "POST " + RestartPath,
	AdminPath, "POST " + KeysPath, "GET " + KeysPath, "DELETE " + KeysPath + "/{id}", "POST " + OIDCKeysPath,
	"GET /v1/magpie/omarchy", "OPTIONS /v1/magpie/omarchy",
	// the Claude bridge's own helper, by a token of its own, not a key
	"POST " + claudeMCPPath + "{token}",
	// artifacts' pages and the Magpie Browser extension
	"GET " + ArtifactPagePath + "{ref}", "GET " + ArtifactPagePath + "{ref}/{path...}",
	"POST " + ArtifactPagePath + "{ref}/_api/db", "GET " + ArtifactPagePath + "{ref}/_api/events",
	"POST " + ArtifactPagePath + "{ref}/_api/publish", "POST " + ArtifactPagePath + "{ref}/_api/meta",
	"POST " + ArtifactPagePath + "{ref}/_api/upload", "POST " + ArtifactPagePath + "{ref}/_api/assets",
	"GET " + ArtifactPagePath + "{ref}/_api/shell",
	"POST " + ArtifactPagePath + "{ref}/_api/files",
	"GET " + ArtifactPagePath + "{ref}/_api/live",
	"GET " + artifactBlobPath + "{id}", "GET " + BrowserExtensionPath, "GET " + BrowserStatusPath,
}

// ifitscolorful on X moved an MCP gateway off magpie: model access and
// MCP shared one token, and no key could have the MCP servers without
// the models. A key's two surfaces are now chosen on their own. A key
// held to no model (access.NoModels) is refused every route that uses a
// model or tells of them (chat, responses, messages, embeddings,
// count_tokens, the model lists, the quotas, decisions, a WebSocket's
// upgrade, Gemini's, Ollama's, Codex's and a vendor's own API among
// them), from this computer and from another, before any upstream is
// reached; a key held to no MCP server (access.NoMCP) is refused every
// MCP route. Neither is refused the other surface, and a key with both
// is refused neither.
func TestKeySurfacesEveryRoute(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	var reached atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":5}}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "upstream-secret", Chat: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "MCP gateway", "Models only", "Both")
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{access.NoModels}}); err != nil {
		t.Fatal(err)
	}
	if _, err := access.Update("mcp-key", access.Change{Key: keys[1].ID, MCP: []string{access.NoMCP}}); err != nil {
		t.Fatal(err)
	}
	mcpOnly, modelsOnly, both := secrets[0], secrets[1], secrets[2]

	mux := New().routes()
	all := map[string]int{}
	for p := range keyModelRoutes {
		all[p]++
	}
	for p := range keyMCPRoutes {
		all[p]++
	}
	for _, p := range keySurfaceNeither {
		all[p]++
	}
	for _, p := range mux.patterns {
		if all[p] != 1 {
			t.Errorf("route %q is in %d of keyModelRoutes, keyMCPRoutes and keySurfaceNeither: say which surface it is", p, all[p])
		}
	}
	for p := range all {
		if !slices.Contains(mux.patterns, p) {
			t.Errorf("%q is named, and the router doesn't have it", p)
		}
	}
	models, mcps := sortedKeys(keyModelRoutes), sortedKeys(keyMCPRoutes)

	h := lanGuard(New().Handler())
	do := func(method, path, from, secret string, hdr ...string) (int, string) {
		var body io.Reader
		if method == "POST" || method == "PUT" {
			body = strings.NewReader(chatReq)
		}
		r := httptest.NewRequest(method, path, body)
		r.RemoteAddr = from
		r.Header.Set("Authorization", "Bearer "+secret)
		r.Header.Set("Content-Type", "application/json")
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	refused := func(who, from string, routes []string, secret, code string) {
		for _, p := range routes {
			for _, mp := range requestsFor(p) {
				c, b := do(mp[0], mp[1], from, secret)
				if c != http.StatusForbidden || !strings.Contains(b, code) {
					t.Errorf("%s from %s: %s %s (%s): %d %.200s", who, from, mp[0], mp[1], p, c, b)
				}
			}
		}
	}
	for _, from := range []string{"192.168.1.9:5000", "127.0.0.1:5000"} {
		reached.Store(0) // the chats the keys with models sent before
		refused("the MCP-only key", from, models, mcpOnly, KeyNoModelsCode)
		for _, path := range []string{"/v1/responses", "/responses"} {
			c, b := do("GET", path, from, mcpOnly, "Upgrade", "websocket", "Connection", "Upgrade", "Sec-WebSocket-Version", "13", "Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
			if c != http.StatusForbidden || !strings.Contains(b, KeyNoModelsCode) {
				t.Errorf("the MCP-only key from %s: a WebSocket on %s: %d %.200s", from, path, c, b)
			}
		}
		if c, b := do("POST", "/v1//chat/completions", from, mcpOnly); c == 200 || strings.Contains(b, `"choices"`) {
			t.Errorf("the MCP-only key from %s: an unclean chat path: %d %.200s", from, c, b)
		}
		if n := reached.Load(); n != 0 {
			t.Fatalf("the MCP-only key from %s: the upstream was reached %d times", from, n)
		}
		refused("the models-only key", from, mcps, modelsOnly, KeyNoMCPCode)

		// the surface each may use
		for _, secret := range []string{modelsOnly, both} {
			if c, b := do("POST", "/v1/chat/completions", from, secret); c != 200 || !strings.Contains(b, `"choices"`) {
				t.Errorf("a key with models from %s: chat: %d %.200s", from, c, b)
			}
		}
		for _, secret := range []string{mcpOnly, both} {
			if c, b := do("GET", OwnMCPPath, from, secret); c != 200 || strings.Contains(b, "gateway_key_") {
				t.Errorf("a key with MCP servers from %s: %s: %d %.200s", from, OwnMCPPath, c, b)
			}
		}
	}

	// every other route, judged by the guard alone (a route let through
	// may stream or wait, so it isn't run here): neither key is refused
	// the surface it has, nor anything that is no surface's
	passed := false
	g := keySurfaceGuard(mux, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { passed = true }))
	judge := func(who access.Identity, routes []string) {
		for _, p := range routes {
			for _, mp := range requestsFor(p) {
				passed = false
				r := httptest.NewRequest(mp[0], mp[1], nil)
				w := httptest.NewRecorder()
				g.ServeHTTP(w, r.WithContext(access.WithIdentity(context.Background(), who)))
				if !passed {
					t.Errorf("%s: %s %s (%s) refused: %d %.200s", who.KeyName, mp[0], mp[1], p, w.Code, w.Body)
				}
			}
		}
	}
	judge(access.Identity{KeyName: "MCP gateway", Models: []string{access.NoModels}}, append(slices.Clone(mcps), keySurfaceNeither...))
	judge(access.Identity{KeyName: "Models only", MCP: []string{access.NoMCP}}, append(slices.Clone(models), keySurfaceNeither...))
	judge(access.Identity{KeyName: "Both"}, append(append(slices.Clone(models), mcps...), keySurfaceNeither...))
	// a key issued under one held to no MCP server is held to none too
	judge(access.Identity{KeyName: "Issued", Issuer: &access.Identity{MCP: []string{access.NoMCP}}}, models)
	passed = false
	g.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", HubMCPAll, nil).WithContext(access.WithIdentity(context.Background(), access.Identity{KeyName: "Issued", Issuer: &access.Identity{MCP: []string{access.NoMCP}}})))
	if passed {
		t.Error("a key issued under one held to no MCP server reached the hub")
	}
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// An MCP gateway's key (ifitscolorful on X), held to no model, uses the
// hub from another computer as a full key does: the Library's servers
// that run as programs here are given to it, which a key held to some
// models isn't (nativeHeld), since no model limit is there for a program
// to slip past; its MCP list is what holds it to servers. Its chat is
// refused. A key held to no MCP server is refused the hub.
func TestMCPOnlyKeyUsesTheHub(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	t.Setenv("MAGPIE_TRUST_PROXY", "")
	servers := []HubServer{{Name: "shell", Program: true}, {Name: "docs"}}
	var tools []HubTool
	for _, s := range servers {
		tools = append(tools, HubTool{Server: s.Name, Tool: "do", Spec: hubSpec("do")})
	}
	f := &fakeHub{on: true, tools: map[string][]HubTool{"": tools}, servers: map[string][]HubServer{"": servers}}
	withHub(t, f)
	h := lanGuard(New().Handler())
	keys, secrets := newCaller(t, "MCP gateway", "Narrow", "Models only")
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{access.NoModels}}); err != nil {
		t.Fatal(err)
	}
	if _, err := access.Update("models-key", access.Change{Key: keys[1].ID, Models: []string{"some/model"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := access.Update("mcp-key", access.Change{Key: keys[2].ID, MCP: []string{access.NoMCP}}); err != nil {
		t.Fatal(err)
	}
	const there = "192.0.2.7:4000"
	auth := func(i int) []string { return []string{"Authorization", "Bearer " + secrets[i]} }
	listed := func(i int) []string {
		return listedNames(t, readMCP(t, hubPostAt(h, HubMCPAll, there, toolsList, auth(i)...)))
	}
	if got := listed(0); !slices.Contains(got, "shell"+hubSep+"do") || !slices.Contains(got, "docs"+hubSep+"do") {
		t.Errorf("the MCP-only key is given %v", got)
	}
	if got := listed(1); slices.Contains(got, "shell"+hubSep+"do") || !slices.Contains(got, "docs"+hubSep+"do") {
		t.Errorf("a key held to some models is given %v", got)
	}
	if w := hubPostAt(h, HubMCPAll, there, toolsList, auth(2)...); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), KeyNoMCPCode) {
		t.Errorf("the models-only key on the hub: %d %.200s", w.Code, w.Body)
	}
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq))
	r.RemoteAddr, r.Host = there, "127.0.0.1:3425"
	r.Header.Set("Authorization", "Bearer "+secrets[0])
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), KeyNoModelsCode) || !strings.Contains(w.Body.String(), "MCP servers only") {
		t.Errorf("the MCP-only key's chat: %d %.300s", w.Code, w.Body)
	}
}

// A key for MCP servers only uses magpie's own servers that ask a model
// (ifitscolorful on X): magpie-vision describes an image with the model
// the user set for it, though the key may use no model itself, and the
// same key's chat is refused.
func TestMCPOnlyKeyUsesVision(t *testing.T) {
	s, u := eyed(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	h := lanGuard(s.Handler())
	keys, secrets := newCaller(t, "MCP gateway")
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{access.NoModels}}); err != nil {
		t.Fatal(err)
	}
	const there = "192.0.2.7:4000"
	m := readMCP(t, visionPost(h, there, analyzeCall("data:image/png;base64,aGVsbG8=", ""), "Authorization", "Bearer "+secrets[0]))
	if m.Result.IsError || !strings.Contains(m.Result.Content[0].Text, "HELLO") {
		t.Fatalf("the MCP-only key's image: %+v", m)
	}
	if n := len(u.sent("eye")); n != 1 {
		t.Fatalf("the describer was asked %d times", n)
	}
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"eye","messages":[{"role":"user","content":"hi"}]}`))
	r.RemoteAddr, r.Host = there, "127.0.0.1:3425"
	r.Header.Set("Authorization", "Bearer "+secrets[0])
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), KeyNoModelsCode) {
		t.Errorf("the MCP-only key's chat: %d %.300s", w.Code, w.Body)
	}
	if n := len(u.sent("eye")); n != 1 {
		t.Fatalf("the chat reached the describer: %d", n)
	}
}
