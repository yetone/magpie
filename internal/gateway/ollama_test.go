package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// ollamaPlan is a provider with a model that sees, thinks and has a
// window, and one that does none of these, for the Ollama routes to tell
// of.
func ollamaPlan(t *testing.T, chat string) {
	t.Helper()
	yes, no := true, false
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "upstream-secret", Chat: chat,
		Models: []string{"seer", "plain"}, Contexts: map[string]int{"seer": 400000}}); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		provider.SetModelImage("plan/seer", &yes), provider.SetModelImage("plan/plain", &no),
		provider.SetModelEfforts("plan/seer", []string{"low", "high"}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// A client that takes a custom endpoint only as an Ollama server (Visual
// Studio's Copilot BYOK, wani on Discord; VS Code's Copilot Ollama
// provider) asks /api/version, lists /api/tags and reads each model's
// capabilities and window from /api/show, then chats on
// /v1/chat/completions. The list is magpie's catalog by its ids, and a
// model's window and what it takes are its catalog entry's.
func TestOllamaModelRoutes(t *testing.T) {
	fresh(t)
	ollamaPlan(t, "http://127.0.0.1:1/v1")
	h := New().Handler()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:50123"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := do("GET", "/api/version", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"version":"`+ollamaVersion+`"`) {
		t.Fatalf("version: %d %s", w.Code, w.Body)
	}
	var tags struct {
		Models []struct {
			Name, Model, Digest string
			Details             struct{ Family string }
		}
	}
	w := do("GET", "/api/tags", "")
	if err := json.Unmarshal(w.Body.Bytes(), &tags); err != nil || w.Code != 200 {
		t.Fatalf("tags: %d %s", w.Code, w.Body)
	}
	var names []string
	for _, m := range tags.Models {
		if m.Name != m.Model || len(m.Digest) != 64 {
			t.Fatalf("tag %+v", m)
		}
		names = append(names, m.Name)
	}
	if !slices.Contains(names, "plan/seer") || !slices.Contains(names, "plan/plain") {
		t.Fatalf("tags list %v", names)
	}
	type shown struct {
		Capabilities []string
		ModelInfo    map[string]any `json:"model_info"`
	}
	show := func(body string) (int, shown) {
		t.Helper()
		w := do("POST", "/api/show", body)
		var s shown
		if w.Code == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
				t.Fatal(w.Body.String())
			}
		}
		return w.Code, s
	}
	c, s := show(`{"model":"plan/seer"}`)
	arch, _ := s.ModelInfo["general.architecture"].(string)
	if c != 200 || !slices.Equal(s.Capabilities, []string{"completion", "tools", "vision", "thinking"}) || s.ModelInfo[arch+".context_length"] != float64(400000) ||
		!strings.Contains(fmt.Sprint(s.ModelInfo["general.basename"]), "seer") {
		t.Fatalf("show seer: %d %+v", c, s)
	}
	// a client from before Ollama 0.5 names it "name"; a window magpie
	// doesn't know is left out, never told as 32768 (which Hermes takes
	// as the window and refuses under its 64K floor, @noting_ever on X)
	// or 0, so the client takes its own default
	c, s = show(`{"name":"plan/plain"}`)
	if _, told := s.ModelInfo[arch+".context_length"]; c != 200 || !slices.Equal(s.Capabilities, []string{"completion", "tools"}) || told {
		t.Fatalf("show plain: %d %+v", c, s)
	}
	// the ollama CLI before 0.7 reads these as numbers unchecked, and
	// panics on one missing ("ollama show" with 0.6.8)
	for _, k := range []string{"general.parameter_count", arch + ".embedding_length"} {
		if _, ok := s.ModelInfo[k].(float64); !ok {
			t.Fatalf("model_info %s: %v", k, s.ModelInfo[k])
		}
	}
	// "ollama run plan/plain" asks for plan/plain:latest, and pulls the
	// model when show says it isn't there
	if c, s := show(`{"model":"plan/plain:latest"}`); c != 200 || !slices.Equal(s.Capabilities, []string{"completion", "tools"}) {
		t.Fatalf("show plain:latest: %d %+v", c, s)
	}
	if c, _ := show(`{"model":"plan/nothing:latest"}`); c != 404 {
		t.Fatal("an unknown model:latest:", c)
	}
	if c, _ := show(`{"model":"plan/nothing"}`); c != 404 {
		t.Fatal("an unknown model:", c)
	}
	if c, _ := show(`{}`); c != 400 {
		t.Fatal("no model:", c)
	}
}

// A gateway key held to some models is listed and shown those alone on the
// Ollama routes, as on /v1/models, and chats on /api/chat and
// /api/generate with those alone, as on /v1/chat/completions.
func TestOllamaRoutesHoldAKey(t *testing.T) {
	fresh(t)
	ollamaPlan(t, ollamaUpstream(t, nil).URL+"/v1")
	keys, secrets := newCaller(t, "Held")
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"plan/plain"}}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+secrets[0])
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := do("GET", "/api/tags", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "plan/plain") || strings.Contains(w.Body.String(), "plan/seer") {
		t.Fatalf("held key's tags: %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/api/show", `{"model":"plan/seer"}`); w.Code != 404 {
		t.Fatalf("held key shown a model it may not use: %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/api/show", `{"model":"plan/plain"}`); w.Code != 200 {
		t.Fatalf("held key's own model: %d %s", w.Code, w.Body)
	}
	v1 := do("POST", "/v1/chat/completions", `{"model":"plan/seer","messages":[{"role":"user","content":"hi"}]}`)
	if v1.Code == 200 {
		t.Fatalf("held key chatted with a model it may not use on /v1: %s", v1.Body)
	}
	for _, rt := range [][2]string{
		{"/api/chat", `{"model":"%s","messages":[{"role":"user","content":"hi"}],"stream":false}`},
		{"/api/generate", `{"model":"%s","prompt":"hi","stream":false}`},
	} {
		if w := do("POST", rt[0], fmt.Sprintf(rt[1], "plan/seer")); w.Code != v1.Code || gjson.Get(w.Body.String(), "error").Type != gjson.String {
			t.Fatalf("%s: held key with a model it may not use: %d %s, want %d as /v1 and Ollama's error", rt[0], w.Code, w.Body, v1.Code)
		}
		if w := do("POST", rt[0], fmt.Sprintf(rt[1], "plan/plain")); w.Code != 200 || !strings.Contains(w.Body.String(), `"done":true`) {
			t.Fatalf("%s: held key's own model: %d %s", rt[0], w.Code, w.Body)
		}
	}
}

// The Ollama routes are guarded as every gateway route is, through the
// real server stack: with no key, a page elsewhere, a sandboxed frame
// (Origin null), a page at a localhost subdomain, a page rebound to
// loopback under its own name, and another machine while magpie isn't
// shared or sends none are all refused, and none is told a model. An
// agent on this computer, and a keyed call, are answered.
func TestOllamaRoutesGuarded(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	t.Setenv("MAGPIE_TRUST_PROXY", "")
	ollamaPlan(t, ollamaUpstream(t, nil).URL+"/v1")
	srv := lanGuard(New().Handler())
	routes := []struct{ method, path, body string }{
		{"GET", "/api/version", ""},
		{"GET", "/api/tags", ""},
		{"POST", "/api/show", `{"model":"plan/seer"}`},
		{"POST", "/api/chat", `{"model":"plan/seer","messages":[{"role":"user","content":"hi"}],"stream":false}`},
		{"POST", "/api/generate", `{"model":"plan/seer","prompt":"hi","stream":false}`},
	}
	const gw = "127.0.0.1:3425"
	browser := []string{"User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.6 Safari/605.1.15",
		"Sec-Fetch-Mode", "no-cors", "Content-Type", "text/plain;charset=UTF-8"}
	do := func(method, path, body, from, host string, hdr ...string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr, r.Host = from, host
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	refused := func(what string, w *httptest.ResponseRecorder, codes ...int) {
		t.Helper()
		if !slices.Contains(codes, w.Code) || strings.Contains(w.Body.String(), "plan/") || w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s: %d %s", what, w.Code, w.Body)
		}
	}
	for _, rt := range routes {
		at := rt.method + " " + rt.path
		// pages on this computer's browser, with no key
		refused(at+" from a page elsewhere", do(rt.method, rt.path, rt.body, "127.0.0.1:50123", gw, append([]string{"Origin", "https://evil.example", "Sec-Fetch-Site", "cross-site"}, browser...)...), http.StatusForbidden)
		refused(at+" from Origin null", do(rt.method, rt.path, rt.body, "127.0.0.1:50123", gw, append([]string{"Origin", "null", "Sec-Fetch-Site", "cross-site"}, browser...)...), http.StatusForbidden)
		refused(at+" from a localhost subdomain", do(rt.method, rt.path, rt.body, "127.0.0.1:50123", gw, append([]string{"Origin", "http://evil.localhost", "Sec-Fetch-Site", "cross-site"}, browser...)...), http.StatusForbidden)
		// rebound: no Origin on a GET, the page's own on a POST
		hdr := append([]string{"Sec-Fetch-Site", "same-origin"}, browser...)
		if rt.method == "POST" {
			hdr = append(hdr, "Origin", "http://rebind.attacker.example:3425")
		}
		refused(at+" rebound", do(rt.method, rt.path, rt.body, "127.0.0.1:50123", "rebind.attacker.example:3425", hdr...), http.StatusForbidden)
		refused(at+" rebound at a localhost subdomain", do(rt.method, rt.path, rt.body, "127.0.0.1:50123", "evil.localhost:3425", hdr...), http.StatusForbidden)
		// another machine, no Origin at all: magpie not shared
		refused(at+" from the LAN, not shared", do(rt.method, rt.path, rt.body, "192.168.1.9:5000", "192.168.1.2:3425"), http.StatusForbidden)
		// a tunnel on loopback
		refused(at+" through a tunnel, not shared", do(rt.method, rt.path, rt.body, "127.0.0.1:50123", gw, "X-Forwarded-For", "203.0.113.9"), http.StatusForbidden)
		// an agent here, as VS Code's Copilot calls it: answered
		if w := do(rt.method, rt.path, rt.body, "127.0.0.1:50123", gw, "User-Agent", "node-fetch"); w.Code != 200 {
			t.Errorf("%s from an agent here: %d %s", at, w.Code, w.Body)
		}
	}
	// shared: another machine needs an enabled key
	_, secrets := newCaller(t, "Visual Studio")
	for _, rt := range routes {
		at := rt.method + " " + rt.path
		for _, hdr := range [][]string{nil, {"Authorization", "Bearer magpie"}, {"Authorization", "Bearer sk-magpie-wrong"}} {
			resetAuthThrottle()
			refused(at+" from the LAN with "+strings.Join(hdr, " "), do(rt.method, rt.path, rt.body, "192.168.1.9:5000", "192.168.1.2:3425", hdr...), http.StatusUnauthorized)
		}
		if w := do(rt.method, rt.path, rt.body, "192.168.1.9:5000", "192.168.1.2:3425", "Authorization", "Bearer "+secrets[0]); w.Code != 200 {
			t.Errorf("%s from the LAN with its key: %d %s", at, w.Code, w.Body)
		}
		// a sandboxed frame or a rebound page with an enabled key is
		// answered, as on /v1
		if w := do(rt.method, rt.path, rt.body, "127.0.0.1:50123", gw, append([]string{"Origin", "null", "Authorization", "Bearer " + secrets[0]}, browser...)...); w.Code != 200 {
			t.Errorf("%s from Origin null with a key: %d %s", at, w.Code, w.Body)
		}
	}
	settings.Save(settings.Settings{LAN: false})
}

// VS Code's Copilot chats with a model it listed from /api/tags on
// <endpoint>/v1/chat/completions, streamed, with an empty bearer (its
// Ollama provider has no key field) and stream_options it adds itself
// (openAIEndpoint.ts interceptBody): the request reaches the model's
// provider by the id the list gave, and the stream comes back.
func TestOllamaClientChats(t *testing.T) {
	fresh(t)
	var got []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req struct{ Model string }
		json.Unmarshal(b, &req)
		got = append(got, r.URL.Path+" "+req.Model+" "+r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello from seer\"},\"finish_reason\":null}]}\n\n"+
			"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()
	ollamaPlan(t, up.URL+"/v1")
	srv := lanGuard(New().Handler())
	var tags struct{ Models []struct{ Model string } }
	r := httptest.NewRequest("GET", "/api/tags", nil)
	r.RemoteAddr, r.Host = "127.0.0.1:50123", "127.0.0.1:3425"
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if err := json.Unmarshal(w.Body.Bytes(), &tags); err != nil || len(tags.Models) == 0 {
		t.Fatalf("tags: %d %s", w.Code, w.Body)
	}
	model := ""
	for _, m := range tags.Models {
		if strings.HasSuffix(m.Model, "/seer") {
			model = m.Model
		}
	}
	body := `{"messages":[{"role":"system","content":"You are an AI programming assistant."},{"role":"user","content":"hi"}],"model":"` + model +
		`","temperature":0.1,"top_p":1,"n":1,"stream":true,"tools":[{"type":"function","function":{"name":"read_file","description":"Read a file","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}],"stream_options":{"include_usage":true}}`
	r = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	r.RemoteAddr, r.Host = "127.0.0.1:50123", "127.0.0.1:3425"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer ")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "hello from seer") {
		t.Fatalf("chat: %d %s", w.Code, w.Body)
	}
	if len(got) != 1 || got[0] != "/v1/chat/completions seer Bearer upstream-secret" {
		t.Fatalf("upstream got %q", got)
	}
}
