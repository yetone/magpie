package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// GPUStack (github.com/gpustack/gpustack) is the user's own server: its
// OpenAI-compatible API is at <server>/v1 with a Bearer key, so it is a
// local-kind preset (the address is the user's) that, unlike Ollama, wants
// a key; GET /v1/models lists the ready models.
func TestGPUStackPreset(t *testing.T) {
	pr := Preset("gpustack")
	if pr == nil {
		t.Fatal("no gpustack preset")
	}
	if pr.Kind != KindLocal || pr.NoKey || pr.Chat != "http://localhost/v1" {
		t.Fatalf("preset: %+v", pr)
	}
	at, err := AtAddress(*pr, "gpustack.example.com:8080")
	if err != nil || at.Chat != "http://gpustack.example.com:8080/v1" {
		t.Fatalf("at address: %q %v", at.Chat, err)
	}
	// a server on the internet is not asked with no key
	p, err := FromPreset("gpustack")
	if err != nil {
		t.Fatal(err)
	}
	p.Chat = "https://gpustack.example.com/v1"
	if keyOptional(p) {
		t.Fatal("a GPUStack at a public host needs its key")
	}
}

// The list is GPUStack's own: routes/openai.py's SyncPage of OAIModel,
// {"object":"list","data":[{"id","object":"model","created","owned_by":"gpustack"}]},
// asked with the key as a Bearer.
func TestGPUStackModels(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer gpustack_abc_secret" {
			w.WriteHeader(401)
			w.Write([]byte(`{"detail":"Invalid authentication credentials"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"object":"list","data":[` +
			`{"id":"qwen3","object":"model","created":1760000000,"owned_by":"gpustack"},` +
			`{"id":"deepseek-r1-distill-qwen-7b","object":"model","created":1760000001,"owned_by":"gpustack"}]}`))
	}))
	defer srv.Close()
	p, err := FromPreset("gpustack")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.AtAddress(srv.URL); err != nil {
		t.Fatal(err)
	}
	p.Key = "gpustack_abc_secret"
	models, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	if len(ids) != 2 || ids[0] != "qwen3" || ids[1] != "deepseek-r1-distill-qwen-7b" {
		t.Fatalf("models: %v", ids)
	}
	p.Key = "wrong"
	if _, err := p.Fetch(context.Background()); err == nil {
		t.Fatal("a wrong key listed models")
	}
}
