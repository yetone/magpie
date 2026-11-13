package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

// A Claude account's list fetches the catalog again every 45 minutes
// (Asked), most often to the same bytes. Each fetch forgot the parsed
// catalog, told every agent to rewrite its list (Changed) and, as usage
// keyed its index on the file's mtime, had the next usage query read the
// whole session history again. The same bytes are only dated now; bytes
// that changed, in either file, are kept and told as before.
func TestSameCatalogFetchedAgainChangesNothing(t *testing.T) {
	freshCache(t)
	var mu sync.Mutex
	api := `{"openai":{"id":"openai","models":{"gpt-6.1-sol":{"id":"gpt-6.1-sol","cost":{"input":2,"output":10}}}}}`
	labs := `{"openai/gpt-6.1-sol":{"id":"gpt-6.1-sol","limit":{"context":400000}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api.json":
			w.Write([]byte(api))
		case "/models.json":
			w.Write([]byte(labs))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	pointAt(t, srv.URL+"/api.json")
	told := 0
	was := Changed
	Changed = func() { told++ }
	t.Cleanup(func() { Changed = was; labsTried.Store(false) })

	again := func() {
		t.Helper()
		if err := Sync(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	again()
	if told != 1 {
		t.Fatalf("a first fetch told the agents %d times", told)
	}
	key := CacheKey()
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(CachePath(), old, old); err != nil {
		t.Fatal(err)
	}
	if !Stale() {
		t.Fatal("a catalog two days old isn't stale")
	}
	again()
	if told != 1 {
		t.Error("the same catalog fetched again told the agents")
	}
	if got := CacheKey(); got != key {
		t.Error("the same catalog fetched again changed its key")
	}
	if Stale() {
		t.Error("the same catalog fetched again stayed stale: its time wasn't now")
	}

	mu.Lock()
	labs = `{"openai/gpt-6.1-sol":{"id":"gpt-6.1-sol","limit":{"context":800000}}}`
	mu.Unlock()
	again()
	if told != 2 {
		t.Error("a lab catalog that changed wasn't told")
	}
	if got := CacheKey(); got == key {
		t.Error("a lab catalog that changed kept the key")
	}
	key = CacheKey()

	mu.Lock()
	api = `{"openai":{"id":"openai","models":{"gpt-6.1-sol":{"id":"gpt-6.1-sol","cost":{"input":3,"output":10}}}}}`
	mu.Unlock()
	again()
	if told != 3 {
		t.Error("a catalog that changed wasn't told")
	}
	if got := CacheKey(); got == key {
		t.Error("a catalog that changed kept the key")
	}
	if pr, ok := PriceOf("openai", "gpt-6.1-sol"); !ok || pr.Input != 3 {
		t.Errorf("a catalog that changed wasn't read again: %+v %v", pr, ok)
	}
}
