package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// Voyage AI's preset (heatonz on Discord): api.voyageai.com/v1 with a
// key, no list to ask, its embedding and rerank models named by the
// preset and marked so, none of them a chat model.
func TestVoyagePreset(t *testing.T) {
	detectHome(t)
	p, err := FromPreset("voyage")
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != "https://api.voyageai.com/v1" || p.Responses != "" || p.Anthropic != "" || p.Icon != "voyage-color" || p.KeysURL != "https://dashboard.voyageai.com/organization/api-keys" {
		t.Fatalf("preset %+v", p)
	}
	if pr := Preset("voyage"); pr.Kind != KindVendor || pr.NoKey || !pr.NoList || len(pr.Models) != 0 {
		t.Fatalf("preset def %+v", pr)
	}
	embed, rerank := p.PlanEmbeddings(), p.PlanRerankers()
	if len(embed) == 0 || embed[0].ID != "voyage-4-large" || len(rerank) == 0 || rerank[0].ID != "rerank-3" {
		t.Fatalf("embeddings %v, rerankers %v", embed, rerank)
	}
	for _, m := range embed {
		if m.Serves != catalog.ServesEmbedding || catalog.ServesOf(m.ID) != catalog.ServesEmbedding {
			t.Errorf("%s: Serves %q, ServesOf %q", m.ID, m.Serves, catalog.ServesOf(m.ID))
		}
	}
	for _, m := range rerank {
		if m.Serves != catalog.ServesRerank || catalog.ServesOf(m.ID) != catalog.ServesRerank {
			t.Errorf("%s: Serves %q, ServesOf %q", m.ID, m.Serves, catalog.ServesOf(m.ID))
		}
	}
	all, err := p.ListAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(embed)+len(rerank) || len(catalog.Chat(all)) != 0 {
		t.Fatalf("ListAll %v", all)
	}
	// another preset's retrieval models are untouched: none at the preset
	// level, Volcengine's plans' own
	if o, _ := FromPreset("openai"); len(o.PlanRetrieval("")) != 0 {
		t.Fatalf("openai's %v", o.PlanRetrieval(""))
	}
}

// A saved Voyage provider's Test asks an embedding model at its
// /embeddings, as the gateway does, and a reranker's at /rerank, never
// /chat/completions or /models.
func TestVoyageTestAsksItsRetrievalAPIs(t *testing.T) {
	detectHome(t)
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		asked = append(asked, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer pa-voyage" {
			w.WriteHeader(401)
			io.WriteString(w, `{"detail":"Provided API key is invalid."}`)
			return
		}
		switch r.URL.Path {
		case "/v1/embeddings":
			io.WriteString(w, `{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],"model":"voyage-4-large","usage":{"total_tokens":1}}`)
		case "/v1/rerank":
			io.WriteString(w, `{"object":"list","data":[{"relevance_score":0.5,"index":0}],"model":"rerank-3","usage":{"total_tokens":2}}`)
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"detail":"Not Found"}`)
		}
	}))
	defer srv.Close()
	p, _ := FromPreset("voyage")
	p.Chat, p.Key = srv.URL+"/v1", "pa-voyage"
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	saved, err := Find("voyage")
	if err != nil {
		t.Fatal(err)
	}
	p = *saved
	if rs := p.Test(context.Background()); len(rs) != 1 || !rs[0].OK || rs[0].Model != "voyage-4-large" {
		t.Fatalf("Test: %+v", rs)
	}
	if rs := p.TestModels(context.Background(), []string{"rerank-3", "voyage-code-3"}); len(rs) != 2 || !rs[0].OK || !rs[1].OK {
		t.Fatalf("TestModels: %+v", rs)
	}
	mu.Lock()
	defer mu.Unlock()
	slices.Sort(asked)
	if !slices.Equal(asked, []string{"POST /v1/embeddings", "POST /v1/embeddings", "POST /v1/rerank"}) {
		t.Fatalf("asked %v", asked)
	}
}
