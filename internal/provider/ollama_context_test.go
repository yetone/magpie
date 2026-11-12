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
