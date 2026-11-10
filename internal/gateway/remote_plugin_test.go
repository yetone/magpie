package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// A plugin's subscription on a magpie another magpie uses as its Remote
// magpie (eachg0 on X: Tabbit's, through a remote-magpie): its models are
// listed there and answer each API an agent there speaks, each request
// passed to the other magpie once. The fake plugin serves Chat alone, as
// Tabbit and most plugins do, so Responses and Messages are translated.
func TestRemoteMagpieServesPluginModels(t *testing.T) {
	bun := testenv.Bun(t)
	fresh(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	var mu sync.Mutex
	var got []string
	up := testenv.FakeVendor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, r.URL.Path+" "+string(b))
		mu.Unlock()
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(404)
			return
		}
		if !strings.Contains(string(b), `"stream":true`) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"c","object":"chat.completion","model":"fake-1","choices":[{"index":0,"message":{"role":"assistant","content":"hi from chat"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{"role":"assistant","content":"hi from chat"},"finish_reason":null}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.APIKey(ctx, "fakeco", 0, nil, "k1", plugin.NewAccount); err != nil {
		t.Fatal(err)
	}

	_, secrets := newCaller(t, "B")
	h := New().Handler()
	var passes atomic.Int32 // requests the gateway was asked
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			passes.Add(1)
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(remote.Close)
	id, err := provider.Add(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Key: secrets[0], Chat: strings.TrimPrefix(remote.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	office, err := provider.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := office.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(listed, func(m catalog.Model) bool { return m.ID == "fakeco/fake-1" }) {
		t.Fatalf("the remote listed %v, without the plugin's fakeco/fake-1", listed)
	}
	model := id + "/fakeco/fake-1"

	for _, c := range []struct{ name, path, body string }{
		{"chat", "/v1/chat/completions", `{"model":"` + model + `","messages":[{"role":"user","content":"hello"}]}`},
		{"chat stream", "/v1/chat/completions", `{"model":"` + model + `","stream":true,"messages":[{"role":"user","content":"hello"}]}`},
		{"chat tools", "/v1/chat/completions", `{"model":"` + model + `","stream":true,"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"ls","parameters":{"type":"object","properties":{}}}}]}`},
		{"responses", "/v1/responses", `{"model":"` + model + `","input":"hello"}`},
		{"responses stream", "/v1/responses", `{"model":"` + model + `","stream":true,"input":"hello","tools":[{"type":"function","name":"ls","parameters":{"type":"object","properties":{}}}]}`},
		{"messages", "/v1/messages", `{"model":"` + model + `","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`},
		{"messages stream", "/v1/messages", `{"model":"` + model + `","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"ls","input_schema":{"type":"object","properties":{}}}]}`},
	} {
		r := httptest.NewRequest("POST", c.path, strings.NewReader(c.body))
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		passes.Store(0)
		h.ServeHTTP(rec, r)
		if p := passes.Load(); p != 1 {
			t.Errorf("%s: the remote was asked %d times, want 1", c.name, p)
		}
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hi from chat") {
			mu.Lock()
			t.Errorf("%s: %d %s\nvendor saw %q", c.name, rec.Code, rec.Body, got)
			mu.Unlock()
		}
	}
}
