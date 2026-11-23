package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"testing"
)

// The catalogs as models.dev served them on 11-23, cut to openrouter,
// vercel, openai and google-vertex: api.json (v1) and the experimental v2
// catalog's embedding models (/experimental/v2.0/api.json?type=embedding).
// v1's openrouter lists none of OpenRouter's 40 embedding models (v2_only),
// and vercel lists google/gemini-embedding-001 with no price.
const (
	embeddingsV1_11_23 = "testdata/models.dev-2026-11-23-embeddings-v1.json"
	embeddingsV2_11_23 = "testdata/models.dev-v2-2026-11-23-embeddings.json"
)

// modelsDevServer serves the two fixtures as models.dev does, v2 down
// while *down is set, and counts v2's asks.
func modelsDevServer(t *testing.T, down *bool) (url string, v2Asks func() int) {
	t.Helper()
	v1, err := os.ReadFile(embeddingsV1_11_23)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := os.ReadFile(embeddingsV2_11_23)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api.json":
			w.Write(v1)
		case "/experimental/v2.0/api.json":
			mu.Lock()
			asked++
			mu.Unlock()
			if r.URL.Query().Get("type") != "embedding" {
				http.Error(w, "the whole v2 catalog asked for", http.StatusBadRequest)
				return
			}
			if *down {
				http.Error(w, "down", http.StatusBadGateway)
				return
			}
			w.Write(v2)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() int { mu.Lock(); defer mu.Unlock(); return asked }
}

// Winnie7572 on X (#7321): 415 of 420 OpenRouter embedding calls counted
// unpriced, as models.dev's v1 catalog leaves every embedding model out
// (v2_only) and magpie read v1 alone. After a Sync, OpenRouter's are
// priced as v2 prices them, which is OpenRouter's own word
// (/api/v1/embeddings/models: qwen3-embedding-8b's prompt 0.00000001 a
// token), as is the price v1 leaves out of an embedding model it lists
// (Vercel's gemini-embedding-001). One priced by the character (Vertex's
// text-multilingual-embedding-002) has no token price, and the chat lists
// are what they were.
func TestEmbeddingModelsPricedFromV2(t *testing.T) {
	useCatalogs(t, "", "")
	down := false
	url, _ := modelsDevServer(t, &down)
	was := modelsDevURL
	modelsDevURL = url + "/api.json"
	t.Cleanup(func() { modelsDevURL = was; labsTried.Store(false); embeddingsTried.Store(false) })

	if err := Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		providers []string
		id        string
		want      Price
	}{
		{[]string{"openrouter"}, "qwen/qwen3-embedding-8b", Price{Input: 0.01}},
		{[]string{"openrouter"}, "cohere/embed-5-fast", Price{Input: 0.08}},
		{[]string{"openrouter"}, "baai/bge-m3", Price{Input: 0.01}},
		{[]string{"openrouter"}, "openai/text-embedding-3-small", Price{Input: 0.02}},
		{[]string{"vercel"}, "google/gemini-embedding-001", Price{Input: 0.15}},
		// v1's own price, unchanged
		{[]string{"openai"}, "text-embedding-3-small", Price{Input: 0.02}},
		{[]string{"openrouter"}, "openai/gpt-4o-mini", Price{Input: 0.15, Output: 0.6, CacheRead: 0.075}},
	} {
		got, ok := PricedBy(c.providers, c.id)
		if !ok || !got.Same(c.want) {
			t.Errorf("%v %s priced %+v (%v), want %+v", c.providers, c.id, got, ok, c.want)
		}
	}
	if got, ok := PriceOf("openrouter", "qwen/qwen3-embedding-8b"); !ok || got.Input != 0.01 {
		t.Errorf("PriceOf(openrouter, qwen/qwen3-embedding-8b) = %+v %v", got, ok)
	}
	if got, ok := PriceOf("google-vertex", "text-multilingual-embedding-002"); ok {
		t.Errorf("a price by the character was taken for one by the token: %+v", got)
	}
	for _, pid := range []string{"openrouter", "vercel", "openai", "google-vertex"} {
		for _, m := range Provider(pid) {
			if slices.Contains(m.OutputModalities, "embedding") || m.ID == "baai/bge-m3" {
				t.Errorf("%s: embedding model %s listed as a chat model", pid, m.ID)
			}
		}
	}
	if got := NameOf("qwen/qwen3-embedding-8b"); got != "Qwen3 Embedding 8B" {
		t.Errorf("NameOf = %q", got)
	}
}

// A v2 fetch that fails keeps the embedding models kept before (a failed
// read is unknown, not "none"), and never fails the Sync: v1 answers
// alone, as it did. A catalog kept before magpie read v2 is fetched again
// at once, not a day later.
func TestEmbeddingModelsKeptWhenV2Fails(t *testing.T) {
	useCatalogs(t, "", "")
	down := false
	url, v2Asks := modelsDevServer(t, &down)
	was := modelsDevURL
	modelsDevURL = url + "/api.json"
	t.Cleanup(func() { modelsDevURL = was; labsTried.Store(false); embeddingsTried.Store(false) })

	embeddingsTried.Store(false)
	if err := Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v2Asks() != 1 {
		t.Fatalf("Sync asked v2 %d times", v2Asks())
	}
	key := CacheKey()
	down = true
	if err := Sync(context.Background()); err != nil {
		t.Fatal("a v2 fetch failing failed Sync:", err)
	}
	if got, ok := PricedBy([]string{"openrouter"}, "qwen/qwen3-embedding-8b"); !ok || got.Input != 0.01 {
		t.Errorf("a failed v2 fetch lost the kept price: %+v %v", got, ok)
	}
	if CacheKey() != key {
		t.Error("a failed v2 fetch changed the catalog's key")
	}

	os.Remove(EmbeddingsPath())
	Reset()
	if _, ok := PricedBy([]string{"openrouter"}, "qwen/qwen3-embedding-8b"); ok {
		t.Fatal("priced with no v2 file kept")
	}
	if CacheKey() == key {
		t.Error("the v2 file gone kept the catalog's key: the ledger isn't priced again")
	}
	embeddingsTried.Store(false)
	if !Stale() {
		t.Error("a catalog without v2's embedding models isn't stale")
	}
	down = false
	if err := Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if Stale() {
		t.Error("stale after v2 came")
	}
	if _, ok := PricedBy([]string{"openrouter"}, "qwen/qwen3-embedding-8b"); !ok {
		t.Error("not priced after v2 came")
	}
}

// v1's word stands wherever it gives one: an embedding model v1 prices
// keeps v1's price though v2 says another, and only a model of v2's type
// "embedding" is added. (Literal rows: in the real 11-23 catalogs every
// embedding model both price is priced the same.)
func TestEmbeddingsLeaveV1Prices(t *testing.T) {
	in := func(p float64) *float64 { return &p }
	all := map[string]mdProvider{"x": {ID: "x", Models: map[string]mdModel{
		"e1": {ID: "e1", Cost: &Price{Input: 0.5}},
	}}}
	v2 := map[string]v2Provider{
		"x": {Models: map[string]v2Model{
			"e1":    {ID: "e1", Type: "embedding"},
			"e2":    {ID: "e2", Type: "embedding"},
			"tts-1": {ID: "tts-1", Type: "speech"},
		}},
		"y": {Models: map[string]v2Model{"e3": {ID: "e3", Type: "embedding"}}},
	}
	for _, id := range []string{"e1", "e2", "tts-1"} {
		m := v2["x"].Models[id]
		m.Cost = &struct {
			Text *struct {
				Input  *v2Part `json:"input"`
				Output *v2Part `json:"output"`
			} `json:"text"`
		}{Text: &struct {
			Input  *v2Part `json:"input"`
			Output *v2Part `json:"output"`
		}{Input: &v2Part{Price: in(0.1)}}}
		v2["x"].Models[id] = m
	}
	addEmbeddings(all, v2)
	if got := all["x"].Models["e1"].Cost.Input; got != 0.5 {
		t.Errorf("v1's price %v changed to v2's", got)
	}
	if got := all["x"].Models["e2"]; got.Cost == nil || got.Cost.Input != 0.1 || !slices.Equal(got.Modalities.Output, []string{"embedding"}) {
		t.Errorf("e2 = %+v", got)
	}
	if _, ok := all["x"].Models["tts-1"]; ok {
		t.Error("a speech model was added")
	}
	if _, ok := all["y"]; ok {
		t.Error("a provider v1 doesn't list was added")
	}
}
