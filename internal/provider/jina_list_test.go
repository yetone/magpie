package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// A Jina AI list saved by an older magpie, its ids as the list says them
// ("jina-ai/jina-embeddings-v3"), which Jina's API doesn't take, is
// fetched again once a run with no schedule set, so the user's next
// request finds the ids it takes without a Refresh (fenglian980510 on X);
// a list saved with them is left alone.
func TestOldJinaListFetchedOnce(t *testing.T) {
	autolistHome(t)
	t.Cleanup(func() {
		listsTried.Lock()
		clear(listsTried.m)
		listsTried.Unlock()
	})
	body, err := os.ReadFile("../catalog/testdata/jina-models-2026-11-26.json")
	if err != nil {
		t.Fatal(err)
	}
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer jina_k" || r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		asked.Add(1)
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = rewrite{srv}
	t.Cleanup(func() { http.DefaultClient.Transport = old })

	const base = "https://api.jina.ai/v1"
	if err := Save(Provider{ID: "jina", Name: "Jina AI", Chat: base, Key: "jina_k"}); err != nil {
		t.Fatal(err)
	}
	// as an older magpie saved it
	if err := catalog.SaveLive("jina", base, []catalog.Model{
		{ID: "jina-ai/jina-embeddings-v3", Serves: catalog.ServesEmbedding},
		{ID: "jina-ai/jina-clip-v2"},
		{ID: "jina-ai/jina-vlm"},
	}); err != nil {
		t.Fatal(err)
	}
	if !catalog.LiveKinded("jina") {
		t.Fatal("the old list's setup: it is kinded, so only its ids are old")
	}
	now := time.Now()
	if got := refreshDueLists(context.Background(), now); !slices.Equal(got, []string{"jina"}) || asked.Load() != 1 {
		t.Fatalf("asked %v, %d times, want [jina] once", got, asked.Load())
	}
	var ids []string
	for _, m := range catalog.LiveServing("jina", catalog.ServesEmbedding) {
		ids = append(ids, m.ID)
	}
	if !slices.Contains(ids, "jina-embeddings-v3") || !slices.Contains(ids, "jina-clip-v2") ||
		slices.ContainsFunc(ids, func(id string) bool { return strings.HasPrefix(id, "jina-ai/") }) {
		t.Fatalf("embedding models after the fetch: %v", ids)
	}
	listsTried.Lock()
	clear(listsTried.m)
	listsTried.Unlock()
	if got := refreshDueLists(context.Background(), now.Add(time.Hour)); len(got) != 0 || asked.Load() != 1 {
		t.Fatalf("a list saved with Jina's ids asked again: %v", got)
	}
}
