package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
)

// besideFake installs the fake plugin as the plugin of the built-in id,
// not moved onto it (id-plugin beside the built-in), signed in with a
// key, its requests going to up; it gives the provider's id.
func besideFake(t *testing.T, id string, up http.Handler) string {
	t.Helper()
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	fresh(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Setenv("FAKE_ID", id)
	t.Setenv("FAKE_RESPONSES", "1")
	t.Cleanup(plugin.Settle)
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	t.Setenv("FAKE_BASE", srv.URL+"/v1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.APIKey(ctx, id, 0, nil, "k1", plugin.NewAccount); err != nil {
		t.Fatal(err)
	}
	pid := provider.PluginID(id)
	if p, err := provider.Find(pid); err != nil || !p.IsPlugin() || p.PluginProvider() != id {
		t.Fatalf("Find(%s) = %+v, %v", pid, p, err)
	}
	return pid
}

// The Grok plugin draws at Grok's Imagine API as the built-in does
// (TestGrokAccountDraws): the same models, the same request, sent through
// the plugin, which signs it.
func TestGrokPluginDraws(t *testing.T) {
	var mu sync.Mutex
	var paths, bodies []string
	pid := besideFake(t, "grok", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		paths, bodies = append(paths, r.URL.Path), append(bodies, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"b64_json":"`+base64.StdEncoding.EncodeToString(pngBytes)+`"}]}`)
	}))
	p, err := provider.Find(pid)
	if err != nil {
		t.Fatal(err)
	}
	if ds := Drawers(*p); len(ds) != 2 || ds[0].ID != "grok-imagine-image" || ds[0].Provider != pid {
		t.Fatalf("drawers %v", ds)
	}
	if vs := Videomakers(*p); len(vs) == 0 {
		t.Fatal("the Grok plugin makes no videos")
	}
	code, a, raw := postImages(t, New(), "/v1/images/generations", "application/json", `{"model":"`+pid+`/grok-imagine-image","prompt":"a magpie","size":"1792x1024"}`)
	if code != 200 || len(a.Data) != 1 {
		t.Fatalf("%d %s", code, raw)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 || paths[0] != "/v1/images/generations" || !strings.Contains(bodies[0], `"model":"grok-imagine-image"`) || !strings.Contains(bodies[0], `"aspect_ratio":"16:9"`) {
		t.Fatalf("asked %v %v", paths, bodies)
	}
}

// Codex's namespaced tools reach the Grok plugin flat, and its call comes
// back under the namespace, as with the built-in (TestGrokGetsNamespacedTools).
func TestGrokPluginGetsNamespacedTools(t *testing.T) {
	var mu sync.Mutex
	var sent []map[string]any
	call := `{"type":"function_call","id":"fc_1","call_id":"c9","name":"collaboration__spawn_agent","arguments":"{\"message\":\"go\"}","status":"completed"}`
	pid := besideFake(t, "grok", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var q map[string]any
		json.Unmarshal(b, &q)
		mu.Lock()
		sent = append(sent, q)
		mu.Unlock()
		done := `{"id":"r1","object":"response","status":"completed","output":[` + call + `],"usage":{"input_tokens":5,"output_tokens":3}}`
		if q["stream"] != true {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, done)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","object":"response","status":"in_progress","output":[]}}`,
			`event: response.output_item.added`+"\n"+`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"c9","name":"collaboration__spawn_agent","arguments":""}}`,
			`event: response.output_item.done`+"\n"+`data: {"type":"response.output_item.done","output_index":0,"item":`+call+`}`,
			`event: response.completed`+"\n"+`data: {"type":"response.completed","response":`+done+`}`))
	}))
	body := `{"model":"` + pid + `/fake-resp","stream":false,` + namespacedTools + `,"input":[
		{"type":"message","role":"user","content":"go"},
		{"type":"function_call","call_id":"c1","name":"spawn_agent","namespace":"collaboration","arguments":"{}"},
		{"type":"function_call_output","call_id":"c1","output":"ok"}]}`
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	mu.Lock()
	if len(sent) != 1 {
		t.Fatalf("asked %d times", len(sent))
	}
	q := sent[0]
	mu.Unlock()
	var names []string
	for _, tl := range q["tools"].([]any) {
		tm := tl.(map[string]any)
		names = append(names, tm["type"].(string)+":"+tm["name"].(string))
	}
	if got := strings.Join(names, ","); got != "function:exec_command,function:collaboration__spawn_agent,function:collaboration__freeform" {
		t.Fatalf("Grok was offered %s", got)
	}
	if c := q["input"].([]any)[1].(map[string]any); c["name"] != "collaboration__spawn_agent" || c["namespace"] != nil {
		t.Fatalf("the call handed back went as %v", c)
	}
	var res struct {
		Output []map[string]any `json:"output"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || len(res.Output) != 1 {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	if it := res.Output[0]; it["name"] != "spawn_agent" || it["namespace"] != "collaboration" {
		t.Fatalf("Codex was given %v", it)
	}
}

// Kiro's plugin beside the built-in (kiro-plugin) counts by estimate, as
// the built-in and the moved plugin do: the vendor isn't asked.
func TestPluginBesideBuiltinCountsByEstimate(t *testing.T) {
	up := &countAsked{}
	pid := besideFake(t, "kiro", up)
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages/count_tokens",
		strings.NewReader(`{"model":"`+pid+`/fake-claude","messages":[{"role":"user","content":"hello there"}]}`)))
	if n := estimated(t, rec); n == 999 || len(up.asked()) > 0 {
		t.Fatalf("the vendor was asked to count: %v, %d", up.asked(), n)
	}
}

// Grok's plugin beside the built-in Grok (grok-plugin) searches by itself
// as the built-in does, on Responses only.
func TestGrokPluginSearchesItself(t *testing.T) {
	pid := besideFake(t, "grok", http.NotFoundHandler())
	p, err := provider.Find(pid)
	if err != nil {
		t.Fatal(err)
	}
	if pid != "grok-plugin" || !searchesItself(*p, provider.Responses) || searchesItself(*p, provider.Chat) {
		t.Fatalf("%s searches by itself: %v on Responses, %v on Chat", pid, searchesItself(*p, provider.Responses), searchesItself(*p, provider.Chat))
	}
}
