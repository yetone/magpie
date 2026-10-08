package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// library is a vendor with an embeddings model and a reranker; what each
// path was sent is kept.
type library struct {
	mu   sync.Mutex
	sent map[string][]string
}

func (l *library) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	l.mu.Lock()
	if l.sent == nil {
		l.sent = map[string][]string{}
	}
	l.sent[r.URL.Path] = append(l.sent[r.URL.Path], string(body))
	l.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer key" {
		w.WriteHeader(401)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/v1/embeddings":
		if strings.Contains(string(body), "chat-model") {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"chat-model is not an embedding model"}}`)
			return
		}
		io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,-0.2]},{"object":"embedding","index":1,"embedding":[0.3,0.4]}],"model":"embed-1","usage":{"prompt_tokens":5,"total_tokens":5}}`)
	case "/v1/rerank":
		// Jina's shape, the documents given back
		io.WriteString(w, `{"model":"rerank-1","results":[{"index":1,"relevance_score":0.9,"document":{"text":`+strings.SplitN(strings.SplitN(string(body), `"documents":[`, 2)[1], ",", 2)[0]+`}},{"index":0,"relevance_score":0.1}],"usage":{"total_tokens":12}}`)
	default:
		w.WriteHeader(404)
	}
}

func (l *library) got(path string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.sent[path]...)
}

func shelved(t *testing.T) (*Server, *library) {
	t.Helper()
	fresh(t)
	l := &library{}
	up := httptest.NewServer(l)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "lib", Name: "Lib", Chat: up.URL + "/v1", Key: "key", Models: []string{"embed-1", "rerank-1", "chat-model"}}); err != nil {
		t.Fatal(err)
	}
	return New(), l
}

func postRetrieval(t *testing.T, s *Server, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// #765: an embeddings request goes to the model's provider as it was sent,
// the model its own id, and the vendor's answer comes back; the call is
// in the usage ledger with its tokens.
func TestEmbeddingsGoToTheModelsProvider(t *testing.T) {
	s, l := shelved(t)
	code, raw := postRetrieval(t, s, "/v1/embeddings", `{"model":"lib/embed-1","input":["hello","world"],"encoding_format":"float","dimensions":2}`)
	if code != 200 || !strings.Contains(raw, `"embedding":[0.1,-0.2]`) || !strings.Contains(raw, `"prompt_tokens":5`) {
		t.Fatalf("%d %s", code, raw)
	}
	sent := l.got("/v1/embeddings")
	if len(sent) != 1 || !strings.Contains(sent[0], `"model":"embed-1"`) || !strings.Contains(sent[0], `"input":["hello","world"]`) || !strings.Contains(sent[0], `"dimensions":2`) || !strings.Contains(sent[0], `"encoding_format":"float"`) {
		t.Fatalf("vendor was sent %v", sent)
	}
	// the bare /embeddings an SDK given magpie's root as its base posts to
	if code, raw := postRetrieval(t, s, "/embeddings", `{"model":"embed-1","input":"hi"}`); code != 200 {
		t.Fatalf("bare path: %d %s", code, raw)
	}
	var recs []usage.Record
	for i := 0; i < 50; i++ {
		if recs = usage.Load(time.Time{}); len(recs) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(recs) < 1 || recs[0].Operation != "embeddings" || recs[0].Provider != "lib" || recs[0].Model != "embed-1" || recs[0].Requested != "lib/embed-1" || recs[0].Input != 5 || recs[0].Status != 200 {
		t.Fatalf("usage %+v", recs)
	}
}

func TestVolcengineEmbeddingResponses(t *testing.T) {
	type response struct {
		Object string `json:"object"`
		Model  string `json:"model"`
		Data   []struct {
			Object    string    `json:"object"`
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
		Usage struct {
			Prompt int `json:"prompt_tokens"`
			Total  int `json:"total_tokens"`
		} `json:"usage"`
	}
	for name, fixture := range map[string]string{"Coding Plan": volcengineCodingEmbeddingResponse, "Agent Plan": volcengineAgentEmbeddingResponse} {
		var out response
		if err := json.Unmarshal([]byte(fixture), &out); err != nil {
			t.Fatal(err)
		}
		if out.Object != "list" || !strings.HasPrefix(out.Model, "doubao-embedding-vision") || len(out.Data) != 1 || out.Data[0].Object != "embedding" || len(out.Data[0].Embedding) != 2048 || out.Usage.Prompt != 23 || out.Usage.Total != 23 {
			t.Fatalf("%s embedding response: %+v", name, out)
		}
	}
}

func TestPresetPlanEmbeddingResolvesOutsideChatModels(t *testing.T) {
	fresh(t)
	p, err := provider.FromPreset("volcengine")
	if err != nil {
		t.Fatal(err)
	}
	pr := provider.Preset("volcengine")
	p.ID = "ark-agent"
	p.Chat, p.Responses, p.Anthropic = pr.Regions[1].Chat, pr.Regions[1].Responses, pr.Regions[1].Anthropic
	p.Key = "key"
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	embed := p.PlanEmbeddings()
	if len(embed) == 0 {
		t.Fatal("Agent Plan has no preset embedding model")
	}
	for _, m := range p.Exposed() {
		if m.ID == embed[0].ID {
			t.Fatalf("embedding model exposed for chat: %+v", m)
		}
	}
	got, model, ok := resolveRetrievalModel(embed[0].ID)
	if !ok || got.ID != p.ID || model != embed[0].ID {
		t.Fatalf("bare embedding resolved to %q/%q, %v", got.ID, model, ok)
	}
}

func TestRerankGoesToTheModelsProvider(t *testing.T) {
	s, l := shelved(t)
	code, raw := postRetrieval(t, s, "/v1/rerank", `{"model":"lib/rerank-1","query":"magpie","documents":["a crow","a magpie"],"top_n":2,"return_documents":true}`)
	if code != 200 || !strings.Contains(raw, `"relevance_score":0.9`) {
		t.Fatalf("%d %s", code, raw)
	}
	sent := l.got("/v1/rerank")
	if len(sent) != 1 || !strings.Contains(sent[0], `"model":"rerank-1"`) || !strings.Contains(sent[0], `"query":"magpie"`) || !strings.Contains(sent[0], `"top_n":2`) {
		t.Fatalf("vendor was sent %v", sent)
	}
}

// What the vendor refuses comes back with its code and its words; a model
// magpie doesn't know is a 404 before any vendor is asked.
func TestRetrievalFailures(t *testing.T) {
	s, l := shelved(t)
	if code, raw := postRetrieval(t, s, "/v1/embeddings", `{"model":"lib/chat-model","input":"hi"}`); code != 400 || !strings.Contains(raw, "chat-model is not an embedding model") {
		t.Fatalf("vendor refusal: %d %s", code, raw)
	}
	if code, raw := postRetrieval(t, s, "/v1/embeddings", `{"model":"nobody/embed","input":"hi"}`); code != 404 || !strings.Contains(raw, `nobody/embed`) {
		t.Fatalf("unknown model: %d %s", code, raw)
	}
	if code, raw := postRetrieval(t, s, "/v1/embeddings", `{"input":"hi"}`); code != 400 || !strings.Contains(raw, "name the model") {
		t.Fatalf("no model: %d %s", code, raw)
	}
	if code, _ := postRetrieval(t, s, "/v1/embeddings", `not json`); code != 400 {
		t.Fatalf("not json: %d", code)
	}
	if n := len(l.got("/v1/embeddings")); n != 1 {
		t.Fatalf("vendor asked %d times, want 1", n)
	}
}

// With secrets kept, what is embedded or ranked has them masked on the
// way out, and a document the reranker gives back has them again.
func TestRetrievalRedacts(t *testing.T) {
	s, l := shelved(t)
	if err := settings.Save(settings.Settings{Redact: true}); err != nil {
		t.Fatal(err)
	}
	secret := "sk-ant-api03-" + strings.Repeat("Ab3x", 20)
	if code, raw := postRetrieval(t, s, "/v1/embeddings", `{"model":"lib/embed-1","input":"key `+secret+`"}`); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if sent := l.got("/v1/embeddings"); len(sent) != 1 || strings.Contains(sent[0], secret) {
		t.Fatalf("vendor was sent %v", sent)
	}
	code, raw := postRetrieval(t, s, "/v1/rerank", `{"model":"lib/rerank-1","query":"q","documents":["`+secret+`","b"],"return_documents":true}`)
	if code != 200 || !strings.Contains(raw, secret) {
		t.Fatalf("%d %s", code, raw)
	}
	if sent := l.got("/v1/rerank"); len(sent) != 1 || strings.Contains(sent[0], secret) {
		t.Fatalf("vendor was sent %v", sent)
	}
}

// #773: a routing group's members are tried in turn for embeddings and
// rerank as for a chat: one out of quota (429) or one without the API
// (404) passes the request to the next, and when all fail the last one's
// refusal comes back with those tried first.
func TestRetrievalGroupFailsOver(t *testing.T) {
	s, l := shelved(t)
	var asked []string
	var mu sync.Mutex
	broke := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/v1/rerank" {
			w.WriteHeader(404) // a chat vendor: no rerank
			io.WriteString(w, `<!DOCTYPE html>`)
			return
		}
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"free-models-per-day"}}`)
	}))
	t.Cleanup(broke.Close)
	if err := provider.Save(provider.Provider{ID: "free", Name: "Free", Chat: broke.URL + "/v1", Key: "k", Models: []string{"embed-1:free", "rerank-1:free"}}); err != nil {
		t.Fatal(err)
	}
	for _, g := range []provider.Group{
		{ID: "emb", Name: "emb", Members: []string{"free/embed-1:free", "lib/embed-1"}, Routing: "order"},
		{ID: "rr", Name: "rr", Members: []string{"free/rerank-1:free", "lib/rerank-1"}, Routing: "order"},
		{ID: "dead", Name: "dead", Members: []string{"free/embed-1:free"}, Routing: "order"},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	if code, raw := postRetrieval(t, s, "/v1/embeddings", `{"model":"group/emb","input":"hi"}`); code != 200 || !strings.Contains(raw, `"embedding":[0.1,-0.2]`) {
		t.Fatalf("embeddings: %d %s", code, raw)
	}
	if sent := l.got("/v1/embeddings"); len(sent) != 1 || !strings.Contains(sent[0], `"model":"embed-1"`) {
		t.Fatalf("lib was sent %v", sent)
	}
	if code, raw := postRetrieval(t, s, "/v1/rerank", `{"model":"group/rr","query":"q","documents":["a","b"]}`); code != 200 || !strings.Contains(raw, `"relevance_score":0.9`) {
		t.Fatalf("rerank: %d %s", code, raw)
	}
	mu.Lock()
	got := slices.Clone(asked)
	mu.Unlock()
	if !slices.Equal(got, []string{"/v1/embeddings", "/v1/rerank"}) {
		t.Fatalf("the failing member was asked %v", got)
	}
	if code, raw := postRetrieval(t, s, "/v1/embeddings", `{"model":"group/dead","input":"hi"}`); code != 429 || !strings.Contains(raw, "free-models-per-day") {
		t.Fatalf("all failing: %d %s", code, raw)
	}
}
