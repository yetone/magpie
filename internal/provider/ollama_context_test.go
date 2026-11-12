package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// Shaped as Ollama 0.40 answers /api/show: "parameters" is the Modelfile's
// PARAMETER lines as one text, model_info is keyed by architecture.
const (
	ollamaShowTuned = `{"license":"...","modelfile":"# Modelfile","parameters":"num_ctx                        16384\nstop                           \"<|im_end|>\"\ntemperature                    0.6","template":"{{ .Prompt }}","details":{"family":"mytune"},"model_info":{"general.architecture":"mytune","mytune.context_length":40960,"mytune.embedding_length":4096},"capabilities":["completion"]}`
	ollamaShowPlain = `{"parameters":"stop                           \"<|eot_id|>\"","model_info":{"general.architecture":"plainlm","plainlm.context_length":131072},"capabilities":["completion"]}`
)

func ollamaWithShow(t *testing.T) string {
	t.Helper()
	var shows atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			w.Write([]byte(`{"object":"list","data":[{"id":"mytune:8b","object":"model","owned_by":"library"},{"id":"plainlm:3b","object":"model","owned_by":"library"},{"id":"gone:1b","object":"model","owned_by":"library"}]}`))
		case "/api/show":
			shows.Add(1)
			b := make([]byte, 512)
			n, _ := r.Body.Read(b)
			switch {
			case strings.Contains(string(b[:n]), "mytune:8b"):
				w.Write([]byte(ollamaShowTuned))
			case strings.Contains(string(b[:n]), "plainlm:3b"):
				w.Write([]byte(ollamaShowPlain))
			default:
				w.WriteHeader(404)
				w.Write([]byte(`{"error":"model 'gone:1b' not found"}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestOllamaWindowReading(t *testing.T) {
	if n, set := ollamaWindow([]byte(ollamaShowTuned)); n != 16384 || !set {
		t.Fatalf("num_ctx must win over the architecture's 40960: %d %v", n, set)
	}
	if n, set := ollamaWindow([]byte(ollamaShowPlain)); n != 131072 || set {
		t.Fatalf("no num_ctx: the architecture's context_length: %d %v", n, set)
	}
	if n, _ := ollamaWindow([]byte(`{"error":"x"}`)); n != 0 {
		t.Fatalf("nothing said is 0: %d", n)
	}
	if n, _ := ollamaWindow([]byte(`not json`)); n != 0 {
		t.Fatalf("a failed parse is unknown: %d", n)
	}
}

// maohy915 on X: a local Ollama model's window is what its Modelfile sets
// (num_ctx), told to agents with a reply limit within it, and a window the
// user typed stays theirs.
func TestOllamaListGivesModelfileWindow(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	base := ollamaWithShow(t)
	p := Provider{ID: "ollama", Name: "Ollama", Preset: "ollama", Chat: base + "/v1", Anthropic: base,
		Contexts: map[string]int{"plainlm:3b": 8192}}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	q, _ := Find("ollama")
	p = *q
	ms, _, _, err := p.refetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]catalog.Model{}
	for _, m := range ms {
		by[m.ID] = m
	}
	s := settings.Settings{}
	if m := by["mytune:8b"]; m.Context != 16384 || p.WindowIn(m, s) != 16384 {
		t.Fatalf("mytune: Modelfile num_ctx is the window: %+v", m)
	} else if out := p.ReplyLimitIn(m, s); out != 4096 {
		t.Fatalf("mytune reply limit within the window: %d", out)
	}
	m := by["plainlm:3b"]
	if m.Context != 131072 {
		t.Fatalf("plainlm: architecture window when the Modelfile sets none: %+v", m)
	}
	if got := p.WindowIn(m, s); got != 8192 {
		t.Fatalf("a window the user typed stays theirs: %d", got)
	}
	if m := by["gone:1b"]; m.Context != 0 {
		t.Fatalf("a model whose show fails keeps what the list said: %+v", m)
	}
}

// maohy915 on X: "the model's name itself, Qwen-3.8-27b-128k". A window the
// name says is the window when nothing more authoritative is known: below
// the user's own and the vendor's list and models.dev, above nothing; for
// Ollama it replaces the architecture's maximum, never a Modelfile's num_ctx.
func TestNameWindowIsTheLastWord(t *testing.T) {
	p := Provider{ID: "local", Chat: "http://127.0.0.1:1/v1"}
	s := settings.Settings{}
	if got := p.WindowIn(catalog.Model{ID: "Qwen-3.8-27b-128k"}, s); got != 128_000 {
		t.Fatalf("the name's window: %d", got)
	}
	if got := p.WindowIn(catalog.Model{ID: "Qwen-3.8-27b-128k", Context: 40960}, s); got != 40960 {
		t.Fatalf("the list's window is read before the name's: %d", got)
	}
	if got := p.WindowIn(catalog.Model{ID: "gpt-4o-2024"}, s); got != catalog.ContextOf("gpt-4o-2024") {
		t.Fatalf("not a context suffix: %d", got)
	}
	p.Contexts = map[string]int{"Qwen-3.8-27b-128k": 8192}
	if got := p.WindowIn(catalog.Model{ID: "Qwen-3.8-27b-128k"}, s); got != 8192 {
		t.Fatalf("a window the user typed stays theirs: %d", got)
	}
	p.Contexts = map[string]int{"*": 16384}
	if got := p.WindowIn(catalog.Model{ID: "Qwen-3.8-27b-128k"}, s); got != 16384 {
		t.Fatalf("the provider's window the user typed: %d", got)
	}
}

func TestOllamaNameWindowOverArchitectureNotModelfile(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			w.Write([]byte(`{"object":"list","data":[{"id":"plain-128k:latest","object":"model"},{"id":"tuned-128k:latest","object":"model"},{"id":"plain:3b","object":"model"}]}`))
		case "/api/show":
			b := make([]byte, 512)
			n, _ := r.Body.Read(b)
			if strings.Contains(string(b[:n]), "tuned-128k") {
				w.Write([]byte(ollamaShowTuned))
			} else {
				w.Write([]byte(ollamaShowPlain))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	p := Provider{ID: "ollama", Name: "Ollama", Preset: "ollama", Chat: srv.URL + "/v1", Anthropic: srv.URL}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	q, _ := Find("ollama")
	ms, _, _, err := q.refetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, m := range ms {
		got[m.ID] = m.Context
	}
	want := map[string]int{"plain-128k:latest": 128_000, "tuned-128k:latest": 16384, "plain:3b": 131072}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: %d, want %d (%v)", id, got[id], w, got)
		}
	}
}
