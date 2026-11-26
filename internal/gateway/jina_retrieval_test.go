package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// jinaUp is api.jina.ai as its own OpenAPI spec (api.jina.ai/openapi.json,
// read 2026-11-26) describes it: GET /v1/models answers the list it
// served that day (catalog's testdata, its literal bytes), whose ids are
// "jina-ai/<model>", and POST /v1/embeddings and /v1/rerank take a body
// whose "model" picks its schema from a discriminator of bare ids
// ("jina-embeddings-v3", "jina-reranker-v2-base-multilingual"): any other
// model fails validation, 422. Every other host goes to next.
type jinaUp struct {
	list []byte
	next http.RoundTripper

	mu   sync.Mutex
	sent []string // path and body of each embeddings or rerank call
}

// The discriminator's keys in Jina's spec, for each route.
var (
	jinaEmbedIDs  = []string{"jina-clip-v1", "jina-clip-v2", "jina-code-embeddings-0.5b", "jina-code-embeddings-1.5b", "jina-colbert-v1-en", "jina-colbert-v2", "jina-embeddings-v2-base-code", "jina-embeddings-v2-base-de", "jina-embeddings-v2-base-en", "jina-embeddings-v2-base-es", "jina-embeddings-v2-base-zh", "jina-embeddings-v3", "jina-embeddings-v4", "jina-embeddings-v5-omni-nano", "jina-embeddings-v5-omni-small", "jina-embeddings-v5-text-nano", "jina-embeddings-v5-text-small"}
	jinaRerankIDs = []string{"jina-colbert-v1-en", "jina-colbert-v2", "jina-reranker-m0", "jina-reranker-v1-base-en", "jina-reranker-v1-tiny-en", "jina-reranker-v1-turbo-en", "jina-reranker-v2-base-multilingual", "jina-reranker-v3", "jina-reranker-v3.5"}
)

func (u *jinaUp) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Hostname() != "api.jina.ai" {
		return u.next.RoundTrip(r)
	}
	reply := func(code int, body string) (*http.Response, error) {
		return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	}
	if r.Header.Get("Authorization") != "Bearer jina_k" {
		return reply(401, `{"detail":"Authentication required.","code":"AUTH_MISSING_API_KEY"}`)
	}
	var b []byte
	if r.Body != nil {
		b, _ = io.ReadAll(r.Body)
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/v1/models":
		return reply(200, string(u.list))
	case r.Method == "POST" && (r.URL.Path == "/v1/embeddings" || r.URL.Path == "/v1/rerank"):
		u.mu.Lock()
		u.sent = append(u.sent, r.URL.Path+" "+string(b))
		u.mu.Unlock()
		var req struct {
			Model string `json:"model"`
		}
		json.Unmarshal(b, &req)
		ids := jinaEmbedIDs
		if r.URL.Path == "/v1/rerank" {
			ids = jinaRerankIDs
		}
		if !slices.Contains(ids, req.Model) {
			return reply(422, `{"detail":[{"type":"union_tag_invalid","loc":["body"],"msg":"Input tag '`+req.Model+`' found using 'model' does not match any of the expected tags"}]}`)
		}
		if r.URL.Path == "/v1/rerank" {
			return reply(200, `{"model":"`+req.Model+`","object":"list","usage":{"total_tokens":12},"results":[{"index":1,"relevance_score":0.9},{"index":0,"relevance_score":0.1}]}`)
		}
		return reply(200, `{"model":"`+req.Model+`","object":"list","usage":{"total_tokens":3,"prompt_tokens":3},"data":[{"object":"embedding","index":0,"embedding":[0.25,-0.5]}]}`)
	}
	return reply(404, `{"detail":"Invalid endpoint"}`)
}

func (u *jinaUp) got() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.sent)
}

// fenglian980510 on X: Jina AI added as a custom provider, its embedding
// and rerank models weren't there for the client. Jina's /v1/models
// names each model "jina-ai/<model>", the shape OpenRouter asks of the
// providers it lists, while its API takes "<model>" alone; and it says
// what a model gives in a top-level output_modalities ("embeddings"),
// which magpie didn't read, so jina-clip-v2 and jina-colbert-v2 were
// chat models. Now the models are listed by the ids Jina's API takes,
// each embedding model on ?kind=embedding and none on the chat list, and
// a request for one reaches Jina with its body as the client sent it.
func TestJinaEmbeddingAndRerankModels(t *testing.T) {
	fresh(t)
	list, err := os.ReadFile("../catalog/testdata/jina-models-2026-11-26.json")
	if err != nil {
		t.Fatal(err)
	}
	up := &jinaUp{list: list, next: http.DefaultTransport}
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = up
	t.Cleanup(func() { http.DefaultClient.Transport = old })
	saveFetched(t, provider.Provider{ID: "jina", Name: "Jina AI", Chat: "https://api.jina.ai/v1", Key: "jina_k"})
	s := New()
	s.client = &http.Client{Transport: up}

	list2 := func(q string) map[string]string {
		t.Helper()
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/v1/models"+q, nil))
		if w.Code != 200 {
			t.Fatalf("/v1/models%s: %d %s", q, w.Code, w.Body)
		}
		var got struct {
			Data []map[string]any `json:"data"`
		}
		json.Unmarshal(w.Body.Bytes(), &got)
		kinds := map[string]string{}
		for _, m := range got.Data {
			id, _ := m["id"].(string)
			k, _ := m["kind"].(string)
			kinds[id] = k
		}
		return kinds
	}

	kinds := list2("?kind=embedding,rerank")
	for _, id := range []string{"jina-embeddings-v3", "jina-embeddings-v4", "jina-embeddings-v5-text-small", "jina-clip-v2", "jina-colbert-v2", "jina-code-embeddings-1.5b"} {
		if kinds["jina/"+id] != "embedding" {
			t.Errorf("?kind=embedding: jina/%s is %q: %v", id, kinds["jina/"+id], kinds)
		}
	}
	for _, id := range []string{"jina-reranker-v2-base-multilingual", "jina-reranker-v3", "jina-reranker-m0"} {
		if kinds["jina/"+id] != "rerank" {
			t.Errorf("?kind=rerank: jina/%s is %q: %v", id, kinds["jina/"+id], kinds)
		}
	}
	for id := range kinds {
		if strings.Contains(id, "jina-ai/") {
			t.Errorf("listed by the list's id, which Jina's API doesn't take: %s", id)
		}
	}
	// none of them is offered to chat; Jina's chat models still are
	chat := list2("")
	for id := range chat {
		if kinds[id] != "" {
			t.Errorf("the chat list has %s, a %s model", id, kinds[id])
		}
	}
	if _, ok := chat["jina/jina-vlm"]; !ok {
		t.Errorf("the chat list lacks jina-vlm: %v", chat)
	}

	// each id a client may use reaches Jina as the model it takes, every
	// other field as the client sent it (Jina's task, dimensions,
	// late_chunking; rerank's top_n, return_documents)
	for _, asked := range []string{"jina/jina-embeddings-v3", "jina-embeddings-v3"} {
		code, raw := postRetrieval(t, s, "/v1/embeddings", `{"model":"`+asked+`","input":["hello"],"task":"retrieval.query","dimensions":256,"late_chunking":false,"embedding_type":"float"}`)
		if code != 200 || !strings.Contains(raw, `[0.25,-0.5]`) {
			t.Fatalf("embeddings, %s: %d %s (sent %v)", asked, code, raw, up.got())
		}
	}
	for _, sent := range up.got() {
		var b map[string]any
		json.Unmarshal([]byte(strings.TrimPrefix(sent, "/v1/embeddings ")), &b)
		if b["model"] != "jina-embeddings-v3" || b["task"] != "retrieval.query" || b["dimensions"] != float64(256) || b["late_chunking"] != false || b["embedding_type"] != "float" {
			t.Fatalf("Jina was sent %s", sent)
		}
	}
	n := len(up.got())
	for _, asked := range []string{"jina/jina-reranker-v2-base-multilingual", "jina-reranker-v2-base-multilingual"} {
		code, raw := postRetrieval(t, s, "/v1/rerank", `{"model":"`+asked+`","query":"q","documents":["a","b"],"top_n":2,"return_documents":false}`)
		if code != 200 || !strings.Contains(raw, `"relevance_score":0.9`) {
			t.Fatalf("rerank, %s: %d %s (sent %v)", asked, code, raw, up.got())
		}
	}
	for _, sent := range up.got()[n:] {
		if !strings.HasPrefix(sent, "/v1/rerank ") || !strings.Contains(sent, `"model":"jina-reranker-v2-base-multilingual"`) || !strings.Contains(sent, `"top_n":2`) || !strings.Contains(sent, `"return_documents":false`) {
			t.Fatalf("Jina was sent %s", sent)
		}
	}
}
