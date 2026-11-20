package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// A provider the user added lists its embedding and rerank models where a
// client looks for them: /v1/models?kind=embedding,rerank, each as
// provider/model with its kind and route, and Cherry Studio, which can't
// ask, sees them by default. The default list stays chat models
// (TestKindGroupsServeEachAPIByRoute). Before, /v1/models refused
// ?kind=embedding and no list had them (@rfiacne on X: an added
// provider's embedding models weren't recognised).
func TestAddedProvidersRetrievalModelsListed(t *testing.T) {
	fresh(t)
	// SiliconFlow's /v1/models, the shape of its docs' example
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/embeddings" {
			io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.5,0.25]}],"model":"BAAI/bge-m3","usage":{"prompt_tokens":1,"total_tokens":1}}`)
			return
		}
		io.WriteString(w, `{"object":"list","data":[`+
			`{"id":"deepseek-ai/DeepSeek-V3","object":"model","created":0,"owned_by":""},`+
			`{"id":"BAAI/bge-m3","object":"model","created":0,"owned_by":""},`+
			`{"id":"Qwen/Qwen3-Embedding-8B","object":"model","created":0,"owned_by":""},`+
			`{"id":"BAAI/bge-reranker-v2-m3","object":"model","created":0,"owned_by":""}]}`)
	}))
	defer up.Close()
	saveFetched(t, provider.Provider{ID: "sf", Name: "SiliconFlow", Chat: up.URL + "/v1", Key: "k", Models: []string{"deepseek-ai/DeepSeek-V3"}})
	s := New()

	list := func(q, ua string) (int, map[string]string) {
		t.Helper()
		r := httptest.NewRequest("GET", "/v1/models"+q, nil)
		if ua != "" {
			r.Header.Set("User-Agent", ua)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		var got struct {
			Data []map[string]any `json:"data"`
		}
		json.Unmarshal(w.Body.Bytes(), &got)
		kinds := map[string]string{}
		for _, m := range got.Data {
			id, _ := m["id"].(string)
			k, _ := m["kind"].(string)
			kinds[id] = k
			if k == "embedding" || k == "rerank" {
				ends, _ := m["supported_endpoint_types"].([]any)
				want := map[string]string{"embedding": "embeddings", "rerank": "jina-rerank"}[k]
				if len(ends) != 1 || ends[0] != want {
					t.Fatalf("%s: supported_endpoint_types %v, want [%s]", id, ends, want)
				}
			}
		}
		return w.Code, kinds
	}

	want := map[string]string{"sf/BAAI/bge-m3": "embedding", "sf/Qwen/Qwen3-Embedding-8B": "embedding", "sf/BAAI/bge-reranker-v2-m3": "rerank"}
	code, got := list("?kind=embedding,rerank", "")
	if code != 200 {
		t.Fatalf("?kind=embedding,rerank: %d", code)
	}
	for id, k := range want {
		if got[id] != k {
			t.Fatalf("?kind=embedding,rerank: %s is %q, want %q: %v", id, got[id], k, got)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("?kind=embedding,rerank lists more than its kinds: %v", got)
	}
	if _, got := list("?kind=rerank", ""); len(got) != 1 || got["sf/BAAI/bge-reranker-v2-m3"] != "rerank" {
		t.Fatalf("?kind=rerank: %v", got)
	}
	_, got = list("?kind=all", "")
	for id, k := range want {
		if got[id] != k {
			t.Fatalf("?kind=all: %s is %q: %v", id, got[id], got)
		}
	}
	// Cherry Studio is listed every kind; its knowledge bases take these
	_, got = list("", "CherryStudio/1.6.0")
	for id, k := range want {
		if got[id] != k {
			t.Fatalf("Cherry Studio: %s is %q: %v", id, got[id], got)
		}
	}
	// the default stays the models agents chat with
	_, got = list("", "")
	for id := range got {
		if strings.Contains(strings.ToLower(id), "bge") || strings.Contains(id, "Embedding") {
			t.Fatalf("the default /v1/models lists %s", id)
		}
	}
	// the id it lists is one the route takes
	if code, raw := postRetrieval(t, s, "/v1/embeddings", `{"model":"sf/BAAI/bge-m3","input":"hi"}`); code != 200 || !strings.Contains(raw, `[0.5,0.25]`) {
		t.Fatalf("embeddings by the listed id: %d %s", code, raw)
	}

	// a model disabled in magpie is left out
	if err := provider.DisableModels("sf/BAAI/bge-m3"); err != nil {
		t.Fatal(err)
	}
	if _, got := list("?kind=embedding", ""); got["sf/BAAI/bge-m3"] != "" || got["sf/Qwen/Qwen3-Embedding-8B"] != "embedding" {
		t.Fatalf("with bge-m3 disabled: %v", got)
	}
	if code, _ := list("?kind=moderation", ""); code != 400 {
		t.Fatalf("an unknown kind: %d, want 400", code)
	}
}

// RetrievalModels names each model once, its fetched list's and its
// preset's together.
func TestRetrievalModelsOnce(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":"gpt-5.5"},{"id":"text-embedding-3-small"},{"id":"text-embedding-3-small"}]}`)
	}))
	defer up.Close()
	p := saveFetched(t, provider.Provider{ID: "oa", Name: "Relay", Chat: up.URL + "/v1", Key: "k", Models: []string{"gpt-5.5"}})
	var ids []string
	for _, m := range RetrievalModels(p, catalog.ServesEmbedding) {
		ids = append(ids, m.ID)
	}
	if !slices.Equal(ids, []string{"text-embedding-3-small"}) {
		t.Fatalf("embedding models %v", ids)
	}
}
