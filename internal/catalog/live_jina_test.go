package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
)

// Jina AI's /v1/models, its real reply of 2026-11-26: OpenRouter's shape
// for a provider's list, each row's output_modalities at its top level,
// ["embeddings"] for an embedding model. Any list of that shape is read
// for it: jina-clip-v2 and jina-colbert-v2, whose ids say nothing of
// embeddings, serve /v1/embeddings and are no chat models, and the
// models that answer in text (jina-vlm, the reader-lm ones) stay chat
// models. Its rerankers say text; their ids say rerank. Only at
// api.jina.ai is the "jina-ai/" before each id dropped
// (TestJinaListIDsAreItsAPIs).
func TestFetchReadsTopLevelOutputModalities(t *testing.T) {
	body, err := os.ReadFile("testdata/jina-models-2026-11-26.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()
	ms, _, err := FetchAt(context.Background(), srv.URL+"/v1", "k", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Model{}
	for _, m := range ms {
		by[m.ID] = m
	}
	for id, serves := range map[string]string{
		"jina-ai/jina-embeddings-v3":                 ServesEmbedding,
		"jina-ai/jina-embeddings-v5-omni-small":      ServesEmbedding,
		"jina-ai/jina-clip-v2":                       ServesEmbedding,
		"jina-ai/jina-colbert-v2":                    ServesEmbedding,
		"jina-ai/jina-code-embeddings-1.5b":          ServesEmbedding,
		"jina-ai/jina-reranker-v3":                   ServesRerank,
		"jina-ai/jina-reranker-v2-base-multilingual": ServesRerank,
		"jina-ai/jina-vlm":                           "",
		"jina-ai/reader-lm-1.5b":                     "",
		"jina-ai/jina-ocr-v1":                        "",
	} {
		m, ok := by[id]
		if !ok || m.Serves != serves || m.Draws || m.Films {
			t.Errorf("%s: %+v (want serves %q)", id, m, serves)
		}
	}
	if m := by["jina-ai/jina-vlm"]; m.ImageInput == nil || !*m.ImageInput {
		t.Errorf("jina-vlm takes images, its input_modalities say: %+v", m)
	}
}

// At api.jina.ai each model is the id Jina's API takes: its OpenAPI spec
// picks every request's schema by a bare id ("jina-embeddings-v3"), while
// its list says "jina-ai/jina-embeddings-v3" (fenglian980510 on X). A list
// saved before with those ids is fetched again once (LiveOutdated).
func TestJinaListIDsAreItsAPIs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	body, err := os.ReadFile("testdata/jina-models-2026-11-26.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()
	old := http.DefaultClient.Transport
	t.Cleanup(func() { http.DefaultClient.Transport = old })
	http.DefaultClient.Transport = toServer{srv}

	const base = "https://api.jina.ai/v1"
	ms, _, err := FetchAt(context.Background(), base, "k", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	for _, want := range []string{"jina-embeddings-v3", "jina-clip-v2", "jina-reranker-v3", "jina-vlm", "ReaderLM-v2"} {
		if !slices.Contains(ids, want) {
			t.Errorf("no %s in %v", want, ids)
		}
	}
	if slices.ContainsFunc(ids, func(id string) bool { return len(id) > 8 && id[:8] == "jina-ai/" }) {
		t.Errorf("an id Jina's API doesn't take: %v", ids)
	}

	if err := SaveLive("jina", base, []Model{{ID: "jina-ai/jina-embeddings-v3", Serves: ServesEmbedding}, {ID: "jina-ai/jina-vlm"}}); err != nil {
		t.Fatal(err)
	}
	if !LiveOutdated("jina") {
		t.Error("a Jina list saved with its jina-ai/ ids isn't fetched again")
	}
	if err := SaveLive("jina", base, ms); err != nil {
		t.Fatal(err)
	}
	if LiveOutdated("jina") {
		t.Error("a Jina list saved as read now is fetched again")
	}
	// another vendor's slashed ids are its own
	if err := SaveLive("relay", "https://relay.example/v1", []Model{{ID: "jina-ai/jina-embeddings-v3", Serves: ServesEmbedding}}); err != nil {
		t.Fatal(err)
	}
	if LiveOutdated("relay") {
		t.Error("a relay's jina-ai/ ids are read as Jina's")
	}
}

// toServer sends every request to srv, whatever host it names.
type toServer struct{ srv *httptest.Server }

func (t toServer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = "http", t.srv.Listener.Addr().String()
	return http.DefaultTransport.RoundTrip(r)
}
