package provider

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/catalog"
)

// OpenCode Zen's free models are listed: magpie asks them as OpenCode does
// (gateway/zenfree.go), so they answer.
func TestOpenCodeZenListsFreeModels(t *testing.T) {
	p, err := FromPreset("opencode-zen")
	if err != nil {
		t.Fatal(err)
	}
	got := p.planModels([]catalog.Model{{ID: "mimo-v2.6-flash-free"}, {ID: "claude-opus-5-5"}, {ID: "gpt-5.5"}})
	if len(got) != 3 || got[0].ID != "mimo-v2.6-flash-free" {
		t.Fatalf("got %v", got)
	}
	if !p.OpenCodeFree("mimo-v2.6-flash-free") || p.OpenCodeFree("gpt-5.5") {
		t.Fatal("OpenCodeFree")
	}
	if (Provider{Preset: "together"}).OpenCodeFree("a-free") {
		t.Fatal("OpenCodeFree on another provider")
	}
}

// On OpenCode Go a free model Go lists itself is asked as Go's others are,
// not as Zen's free tier wants (withtee on X); one Go doesn't list, which
// is asked at Zen when Go refuses it, is Zen's still. A Zen provider's
// free models are unchanged, Go's among them.
func TestOpenCodeGoOwnFreeModelsAreNotZens(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	free := `{"cost":{"input":0,"output":0}}`
	body := `{"opencode-go":{"models":{"beam-free":` + free + `,"cat-jam-free":` + free + `,"glm-5.3":{"cost":{"input":1.4,"output":4.4}}}},` +
		`"opencode":{"models":{"cat-jam-free":` + free + `,"glm-5-free":` + free + `}}}`
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	g, err := FromPreset("opencode-go")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"beam-free", "cat-jam-free", "glm-5.3"} {
		if g.OpenCodeFree(m) {
			t.Errorf("Go's own %s counted as Zen's free", m)
		}
	}
	if !g.OpenCodeFree("glm-5-free") {
		t.Error("Zen's glm-5-free on Go is no longer asked as Zen's free tier wants")
	}
	z, err := FromPreset("opencode-zen")
	if err != nil {
		t.Fatal(err)
	}
	if !z.OpenCodeFree("cat-jam-free") || !z.OpenCodeFree("big-pickle") {
		t.Error("Zen's free models changed")
	}
}

// The Test button's probe of a free model is streamed and offers
// OpenCode's five core tools, each in the protocol's own tool shape.
func TestZenFreeProbe(t *testing.T) {
	for proto, key := range map[Protocol]string{Chat: "function", Responses: "name", Anthropic: "input_schema"} {
		var m map[string]any
		if err := json.Unmarshal([]byte(zenFreeProbe(proto, `{"model":"x-free","max_tokens":1}`)), &m); err != nil {
			t.Fatal(err)
		}
		tools, _ := m["tools"].([]any)
		if m["stream"] != true || len(tools) != len(OpenCodeTools) || m["model"] != "x-free" {
			t.Fatalf("%v: %v", proto, m)
		}
		if _, ok := tools[0].(map[string]any)[key]; !ok {
			t.Fatalf("%v: tool %v", proto, tools[0])
		}
		// each takes a parameter: Zen refuses one with none
		for _, tl := range tools {
			if !strings.Contains(fmt.Sprint(tl), "properties:map[") || strings.Contains(fmt.Sprint(tl), "properties:map[]") {
				t.Fatalf("%v: tool %v takes no parameter", proto, tl)
			}
		}
	}
}

// #572: an OpenCode Zen provider added with no key asks with the one
// OpenCode sends signed out, which Zen's free models answer; a key of the
// user's own is kept.
func TestOpenCodeZenNeedsNoKey(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	p, err := FromPreset("opencode-zen")
	if err != nil {
		t.Fatal(err)
	}
	p.Models = []string{"mimo-v2.6-flash-free"}
	id, err := Add(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != OpenCodeAnonymousKey {
		t.Fatalf("key %q", got.Key)
	}
	p.Name, p.Key = "Zen paid", "sk-own"
	if id, err = Add(p); err != nil {
		t.Fatal(err)
	}
	if got, _ = Find(id); got.Key != "sk-own" {
		t.Fatalf("own key %q", got.Key)
	}
	// another provider still needs one
	if _, err := Add(Provider{Name: "Relay", Chat: "https://api.relay.example/v1", Models: []string{"m"}}); err == nil {
		t.Fatal("a relay with no key")
	}
}

// #1795: Add another key on an OpenCode Zen provider with a key of the
// user's own, with nothing pasted, adds public behind it, as a new Zen
// provider saved with no key has it; asked again, it says it has it. Kilo,
// whose free models need no key too, has no such key to add.
func TestZenAddKeyEmptyIsPublic(t *testing.T) {
	isolate(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	zen, err := FromPreset("opencode-zen")
	if err != nil {
		t.Fatal(err)
	}
	zen.ID, zen.Key = "zen", "sk-own"
	if err := Save(zen); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("zen", "", "", ""); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("zen")
	if on := p.KeysOn(); len(on) != 2 || on[0].Key != "sk-own" || on[1].Key != OpenCodeAnonymousKey {
		t.Fatalf("keys on %+v", on)
	}
	if err := AddKey("zen", "", "", ""); err == nil || !strings.Contains(err.Error(), "already has public") {
		t.Fatalf("public twice: %v", err)
	}
	kilo, err := FromPreset("kilo")
	if err != nil {
		t.Fatal(err)
	}
	kilo.ID, kilo.Key = "kilo", "sk-kilo"
	if err := Save(kilo); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("kilo", "", "", ""); err == nil {
		t.Fatal("Kilo took an empty key")
	}
}

// Zen's free tier is asked where OpenCode 2 asks it, the base_url
// models.dev's v2 catalog gives each protocol (#3491): /inference/openai/v1
// for Chat and Responses, /inference/anthropic for Messages, from Zen's own
// URL or 1.x's /inference/v1. Go's and other URLs are as they were.
func TestOpenCodeFreeURL(t *testing.T) {
	for _, c := range []struct {
		in    string
		proto Protocol
		want  string
	}{
		{"https://opencode.ai/zen/v1", Chat, "https://opencode.ai/inference/openai/v1"},
		{"https://opencode.ai/zen/v1/", Chat, "https://opencode.ai/inference/openai/v1/"},
		{"https://opencode.ai/zen/v1/chat/completions", Chat, "https://opencode.ai/inference/openai/v1/chat/completions"},
		{"https://opencode.ai/zen/v1/responses", Responses, "https://opencode.ai/inference/openai/v1/responses"},
		{"https://opencode.ai/inference/v1", Chat, "https://opencode.ai/inference/openai/v1"},
		{"https://opencode.ai/inference/v1", Responses, "https://opencode.ai/inference/openai/v1"},
		{"https://opencode.ai/inference/openai/v1", Chat, "https://opencode.ai/inference/openai/v1"},
		{"https://opencode.ai/zen", Anthropic, "https://opencode.ai/inference/anthropic"},
		{"https://opencode.ai/zen/v1/messages", Anthropic, "https://opencode.ai/inference/anthropic/v1/messages"},
		{"https://opencode.ai/inference", Anthropic, "https://opencode.ai/inference/anthropic"},
		{"https://opencode.ai/inference/anthropic/v1", Anthropic, "https://opencode.ai/inference/anthropic/v1"},
		{"https://opencode.ai/zen/v1", Gemini, "https://opencode.ai/inference/v1"},
		{"https://opencode.ai/zenith/v1", Chat, "https://opencode.ai/zenith/v1"},
		{"https://opencode.ai/zen/go/v1", Chat, "https://opencode.ai/zen/go/v1"},
		{"https://opencode.ai/zen/go", Anthropic, "https://opencode.ai/zen/go"},
		{"https://relay.test/zen/v1", Chat, "https://relay.test/zen/v1"},
		{"https://api.opencode.ai/zen/v1", Chat, "https://api.opencode.ai/inference/openai/v1"},
	} {
		if got := OpenCodeFreeURL(c.in, c.proto); got != c.want {
			t.Errorf("%s on %s: %s, want %s", c.in, c.proto, got, c.want)
		}
	}
	p, _ := FromPreset("opencode-zen")
	if url, _ := tiny(p, Responses, "muse-spark-1.3-contributor-free"); url != "https://opencode.ai/inference/openai/v1/responses" {
		t.Fatalf("probe %s", url)
	}
	if url, _ := tiny(p, Anthropic, "claude-free"); url != "https://opencode.ai/inference/anthropic/v1/messages" {
		t.Fatalf("Messages probe %s", url)
	}
	if url, _ := tiny(p, Responses, "gpt-5.5"); url != "https://opencode.ai/zen/v1/responses" {
		t.Fatalf("paid probe %s", url)
	}
}

// zenFake serves Zen's /models as the public key read it on 2026-10-15
// (testdata/zen_public_models.json, byte for byte) beside models.dev's
// opencode entries for those ids from the same day (zen_catalog.json),
// and answers a probe as Zen answers public: 200 for a free model at the
// inference API on the API the catalog names for it, 401 for any other.
// It returns the probes asked, as "path model".
func zenFake(t *testing.T) func() []string {
	t.Helper()
	isolate(t)
	isolateFetchHome(t)
	cat, err := os.ReadFile("testdata/zen_catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	list, err := os.ReadFile("testdata/zen_public_models.json")
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	if err := os.WriteFile(catalog.CachePath(), cat, 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/v1/models") {
			w.Write(list)
			return
		}
		b, _ := io.ReadAll(r.Body)
		model := gjson.GetBytes(b, "model").String()
		mu.Lock()
		asked = append(asked, r.URL.Path+" "+model)
		mu.Unlock()
		api := catalog.APIOf("opencode", model)
		if api == "" {
			api = "chat"
		}
		path := map[string]string{"chat": "/inference/openai/v1/chat/completions", "responses": "/inference/openai/v1/responses", "anthropic": "/inference/anthropic/v1/messages"}[api]
		if !strings.HasSuffix(model, "-free") && model != "big-pickle" || r.URL.Path != path {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}],\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n"))
	}))
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	t.Cleanup(func() { http.DefaultClient.Transport = old })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(asked)
	}
}

// #2385: the built-in OpenCode Zen, asked with public, lists the free
// models Zen lists, deepseek-v4.1-flash-free among them, as Zen lists
// them: there is no table of free models to fall behind.
func TestZenPublicListsNewFreeModels(t *testing.T) {
	zenFake(t)
	p, _ := FromPreset("opencode-zen")
	id, err := Add(p)
	if err != nil {
		t.Fatal(err)
	}
	q, _ := Find(id)
	if _, err := q.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	q, _ = Find(id)
	var ids []string
	for _, m := range q.Available() {
		ids = append(ids, m.ID)
	}
	if !slices.Contains(ids, "deepseek-v4.1-flash-free") || !q.OpenCodeFree("deepseek-v4.1-flash-free") {
		t.Fatalf("listed %v", ids)
	}
}

// z'x on Discord: Test on an OpenCode Zen provider with the public key
// always failed. It asked public for claude-fable-5 on Messages and
// gemini-3-flash on Gemini, which public doesn't reach, and big-pickle on
// Responses, where Zen doesn't serve it. Now each API is asked for a free
// model it serves, and an API no free model is on is left out.
func TestZenPublicTestAsksFreeModels(t *testing.T) {
	asked := zenFake(t)
	p, _ := FromPreset("opencode-zen")
	id, err := Add(p)
	if err != nil {
		t.Fatal(err)
	}
	q, _ := Find(id)
	if q.Key != OpenCodeAnonymousKey {
		t.Fatalf("key %q", q.Key)
	}
	got := map[Protocol]Result{}
	for _, r := range q.Test(context.Background()) {
		got[r.Protocol] = r
	}
	for proto, r := range got {
		if !r.OK {
			t.Errorf("%s: %+v (asked %v)", proto, r, asked())
		}
	}
	if got[Chat].Model != "big-pickle" || !strings.HasPrefix(got[Responses].Model, "muse-spark-") || !strings.HasSuffix(got[Responses].Model, "-free") {
		t.Errorf("models %+v", got)
	}
	if _, ok := got[Anthropic]; ok {
		t.Errorf("Messages tried with public: %+v", got[Anthropic])
	}
	if _, ok := got[Gemini]; ok {
		t.Errorf("Gemini tried with public: %+v", got[Gemini])
	}
	// a Zen key of the user's own is still asked for paid models
	q.Key = "sk-own"
	if m := q.testModel(*q, Anthropic); m != "claude-fable-5" {
		t.Errorf("own key's Messages model %q", m)
	}
	if m := q.testModel(*q, Responses); m == "big-pickle" {
		t.Errorf("own key's Responses model %q, a chat model", m)
	}
}
