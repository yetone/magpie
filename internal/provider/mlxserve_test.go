package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// MLX-Serve (github.com/ddalcu/mlx-serve) is a local server on :11234
// (src/main.zig's default port) that speaks Chat, Responses and Anthropic
// Messages on one port and asks no key unless started with --api-key, so
// it's a NoKey local preset beside oMLX. Its Anthropic base is the root, as
// its docs/api.md has Claude Code use ANTHROPIC_BASE_URL=http://localhost:11234.
func TestMlxServePreset(t *testing.T) {
	pr := Preset("mlx-serve")
	if pr == nil {
		t.Fatal("no mlx-serve preset")
	}
	if pr.Kind != KindLocal || !pr.NoKey || pr.KeysURL != "" {
		t.Fatalf("kind/noKey/keysURL: %q %v %q", pr.Kind, pr.NoKey, pr.KeysURL)
	}
	if pr.Chat != "http://localhost:11234/v1" || pr.Responses != "http://localhost:11234/v1" || pr.Anthropic != "http://localhost:11234" {
		t.Fatalf("endpoints: %q %q %q", pr.Chat, pr.Responses, pr.Anthropic)
	}
	p, err := FromPreset("mlx-serve")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "mlx-serve" || p.Preset != "mlx-serve" || p.Icon != "mlx-serve" {
		t.Fatalf("from preset: %+v", p)
	}
}

// MLX-Serve's /v1/models lists the loaded model and the ones it found on
// disk, each with its own extra fields (src/server.zig's listing); magpie
// reads the ids of both.
func TestMlxServeModels(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"object":"list","data":[` +
			`{"id":"Qwen3-8B-4bit","object":"model","created":1760000000,"owned_by":"mlx-serve","loaded":true,"state":"ready","bytes_resident":4600000000,"bytes_on_disk":4600000000,"context_length":40960,"max_model_len":40960,"batched_decode":true,"capabilities":["chat","tools"],"input_modalities":["text"],"meta":{"architecture":"qwen3","engine":"mlx","vocab_size":151936,"hidden_size":4096,"num_layers":36,"quantization":"4-bit","context_length":40960,"model_max_tokens":40960,"embedding_max_length":null,"is_moe":false,"drafter_loaded":false,"drafter_path":null,"mtp_loaded":false,"mtp_available":false,"spec_exact":false,"kv_quant":"none","gen_temperature":0.6,"gen_top_p":0.95,"gen_top_k":20}},` +
			`{"id":"gemma-3-4b-it-qat-4bit","object":"model","created":0,"owned_by":"mlx-serve","loaded":false,"state":"unloaded","bytes_resident":0,"bytes_on_disk":3000000000,"meta":{"bytes_on_disk":3000000000}}` +
			`]}`))
	}))
	defer srv.Close()
	p, err := FromPreset("mlx-serve")
	if err != nil {
		t.Fatal(err)
	}
	p.Chat, p.Responses = srv.URL+"/v1", srv.URL+"/v1"
	models, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	if len(ids) != 2 || ids[0] != "Qwen3-8B-4bit" || ids[1] != "gemma-3-4b-it-qat-4bit" {
		t.Fatalf("models: %v", ids)
	}
}
