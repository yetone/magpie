package gui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// The add form's Fetch models (#578) asks the vendor for its list with the
// URL and key typed, before the provider is saved: the list of models
// agents chat with comes back, nothing is saved — no provider, no list
// kept — and a vendor refusing says why.
func TestProviderListBeforeSave(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var mu sync.Mutex
	var asked []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer sk-typed" {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"message":"invalid api key"}}`)
			return
		}
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, `{"data":[{"id":"gpt-5.5"},{"id":"claude-sonnet-5"},{"id":"text-embedding-3"}]}`)
	}))
	defer up.Close()
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/list", strings.NewReader(body)))
		return w
	}
	w := post(`{"typed":true,"name":"Relay","key":"sk-typed","chat":"` + up.URL + `/v1"}`)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var got struct {
		Models    []struct{ ID, Name string }
		Retrieval int
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// counted apart, for a list of them alone to say so (heatonz)
	if got.Retrieval != 1 {
		t.Fatalf("retrieval = %d, want 1: %s", got.Retrieval, w.Body)
	}
	var ids []string
	for _, m := range got.Models {
		ids = append(ids, m.ID)
	}
	// the models agents chat with: an embedding model is left out
	if s := strings.Join(ids, " "); s != "gpt-5.5 claude-sonnet-5" && s != "claude-sonnet-5 gpt-5.5" {
		t.Fatalf("listed %s", s)
	}
	if ps := provider.All(); len(ps) != 0 {
		t.Fatalf("list saved %v", ps)
	}
	for _, id := range []string{"", "relay"} {
		if ms, _, ok := catalog.Live(id); ok || len(ms) != 0 {
			t.Fatalf("list kept %q's: %v", id, ms)
		}
	}
	if !strings.Contains(strings.Join(asked, "\n"), "GET /v1/models Bearer sk-typed") {
		t.Fatalf("asked %v", asked)
	}

	// a key the vendor refuses: the form is told why
	w = post(`{"typed":true,"key":"sk-wrong","chat":"` + up.URL + `/v1"}`)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "401") {
		t.Fatalf("refused: %d %s", w.Code, w.Body)
	}
	// no URL: nothing to ask
	if w = post(`{"typed":true,"key":"sk-typed"}`); w.Code != 400 {
		t.Fatalf("no URL: %d %s", w.Code, w.Body)
	}
}
