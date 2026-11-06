package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// fakeVoyage answers as api.voyageai.com does, in the shapes its API
// reference gives (docs.voyageai.com/reference/embeddings-api and
// reranker-api, read 11-06): a Bearer key, embeddings in OpenAI's list
// shape and rerank results in data, both counted in usage.total_tokens,
// a refusal as {"detail": …}. It has no /models.
type fakeVoyage struct {
	mu   sync.Mutex
	sent map[string][]string
}

func (f *fakeVoyage) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	if f.sent == nil {
		f.sent = map[string][]string{}
	}
	f.sent[r.Method+" "+r.URL.Path] = append(f.sent[r.Method+" "+r.URL.Path], string(body))
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer pa-voyage" {
		w.WriteHeader(401)
		io.WriteString(w, `{"detail":"Provided API key is invalid."}`)
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "POST /v1/embeddings":
		if strings.Contains(string(body), `"model":"rerank-`) {
			w.WriteHeader(400)
			io.WriteString(w, `{"detail":"Model rerank-3 is not supported."}`)
			return
		}
		io.WriteString(w, `{"object":"list","data":[{"object":"embedding","embedding":[-0.016709786,0.026996311],"index":0},{"object":"embedding","embedding":[0.003613521,0.026428301],"index":1}],"model":"voyage-4-large","usage":{"total_tokens":8}}`)
	case "POST /v1/rerank":
		io.WriteString(w, `{"object":"list","data":[{"relevance_score":0.455078125,"index":1,"document":"a magpie"},{"relevance_score":0.439453125,"index":0,"document":"a crow"}],"model":"rerank-3","usage":{"total_tokens":11}}`)
	default:
		w.WriteHeader(404)
		io.WriteString(w, `{"detail":"Not Found"}`)
	}
}

func (f *fakeVoyage) got(route string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent[route]...)
}

// voyaged is a gateway with the Voyage AI preset added, its URL the fake's.
func voyaged(t *testing.T) (*Server, *fakeVoyage) {
	t.Helper()
	fresh(t)
	f := &fakeVoyage{}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	p, err := provider.FromPreset("voyage")
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != "https://api.voyageai.com/v1" {
		t.Fatalf("preset's base %q", p.Chat)
	}
	p.Chat, p.Key = up.URL+"/v1", "pa-voyage"
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return New(), f
}

// heatonz on Discord: Voyage AI's embeddings and rerank, by the model's
// bare id or the provider's, go to its /v1/embeddings and /v1/rerank as
// sent, and the vendor's answer and token count come back.
func TestVoyageEmbeddingsAndRerank(t *testing.T) {
	s, f := voyaged(t)
	code, raw := postRetrieval(t, s, "/v1/embeddings", `{"model":"voyage-4-large","input":["hello","world"],"input_type":"document","output_dimension":1024}`)
	if code != 200 || !strings.Contains(raw, `"embedding":[-0.016709786,0.026996311]`) || !strings.Contains(raw, `"total_tokens":8`) {
		t.Fatalf("embeddings: %d %s", code, raw)
	}
	sent := f.got("POST /v1/embeddings")
	if len(sent) != 1 || !strings.Contains(sent[0], `"model":"voyage-4-large"`) || !strings.Contains(sent[0], `"input_type":"document"`) || !strings.Contains(sent[0], `"output_dimension":1024`) {
		t.Fatalf("Voyage was sent %v", sent)
	}
	if code, raw := postRetrieval(t, s, "/v1/embeddings", `{"model":"voyage/voyage-code-3","input":"func main() {}"}`); code != 200 {
		t.Fatalf("by the provider's id: %d %s", code, raw)
	}
	code, raw = postRetrieval(t, s, "/v1/rerank", `{"model":"rerank-3","query":"magpie","documents":["a crow","a magpie"],"top_k":1,"return_documents":true}`)
	if code != 200 || !strings.Contains(raw, `"relevance_score":0.455078125`) || !strings.Contains(raw, `"document":"a magpie"`) {
		t.Fatalf("rerank: %d %s", code, raw)
	}
	sent = f.got("POST /v1/rerank")
	if len(sent) != 1 || !strings.Contains(sent[0], `"model":"rerank-3"`) || !strings.Contains(sent[0], `"top_k":1`) || !strings.Contains(sent[0], `"return_documents":true`) {
		t.Fatalf("Voyage was sent %v", sent)
	}
	var recs []usage.Record
	for i := 0; i < 100; i++ {
		if recs = usage.Load(time.Time{}); len(recs) >= 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	ops := map[string]usage.Record{}
	for _, r := range recs {
		ops[r.Operation+" "+r.Model] = r
	}
	if r := ops["embeddings voyage-4-large"]; r.Provider != "voyage" || r.Input != 8 || r.Status != 200 {
		t.Fatalf("embeddings' usage %+v (all %+v)", r, recs)
	}
	if r := ops["rerank rerank-3"]; r.Provider != "voyage" || r.Input != 11 || r.Status != 200 {
		t.Fatalf("rerank's usage %+v (all %+v)", r, recs)
	}
}

// A Voyage model is asked only on its own API's route: a reranker is no
// model of the embeddings route, nor an embedding model of rerank's; and
// neither is offered to agents to chat with.
func TestVoyageModelsStayOnTheirRoutes(t *testing.T) {
	s, f := voyaged(t)
	if code, raw := postRetrieval(t, s, "/v1/embeddings", `{"model":"rerank-3","input":"hi"}`); code != 404 {
		t.Fatalf("a reranker on embeddings: %d %s", code, raw)
	}
	if code, raw := postRetrieval(t, s, "/v1/rerank", `{"model":"voyage-4","query":"hi","documents":["hi"]}`); code != 404 {
		t.Fatalf("an embedding model on rerank: %d %s", code, raw)
	}
	if n := len(f.got("POST /v1/embeddings")) + len(f.got("POST /v1/rerank")); n != 0 {
		t.Fatalf("Voyage was asked %d times", n)
	}
	p, err := provider.Find("voyage")
	if err != nil {
		t.Fatal(err)
	}
	if ms := p.Exposed(); len(ms) != 0 {
		t.Fatalf("offered to agents: %+v", ms)
	}
	if _, model, ok := resolveRetrievalModel("rerank-3-lite", catalog.ServesRerank); !ok || model != "rerank-3-lite" {
		t.Fatalf("rerank-3-lite resolved %q %v", model, ok)
	}
}

// What Voyage refuses comes back with its code and its words.
func TestVoyageRefusalSaysWhy(t *testing.T) {
	s, _ := voyaged(t)
	p, _ := provider.Find("voyage")
	p.Key = "pa-wrong"
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	code, raw := postRetrieval(t, s, "/v1/embeddings", `{"model":"voyage-4","input":"hi"}`)
	if code != 401 || !strings.Contains(raw, "Provided API key is invalid.") {
		t.Fatalf("%d %s", code, raw)
	}
}
