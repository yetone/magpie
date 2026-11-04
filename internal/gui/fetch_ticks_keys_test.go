package gui

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// young02036 on Discord (v0.1.2965): the editor's Fetch models ticks each
// account's available models, and an account added gets its own ticked.
// The provider's first key stayed on All models through every Fetch, and
// so did any key added without its own list. Through the GUI's own
// routes: add keys, Fetch models, set one to All models, save the editor,
// fetch again.
func TestFetchModelsTicksEveryKeysModels(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("APPDATA", filepath.Join(h, "AppData"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(h, ".data"))
	var mu sync.Mutex
	lists := map[string][]string{"sk-a": {"m1", "m2", "m3", "m4"}, "sk-b": {"m1", "m2"}, "sk-c": {"m3", "m5"}}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		ids, ok := lists[key]
		mu.Unlock()
		if !ok {
			http.Error(rw, `{"error":{"message":"invalid key"}}`, http.StatusUnauthorized)
			return
		}
		var b []string
		for _, id := range ids {
			b = append(b, `{"id":"`+id+`"}`)
		}
		_, _ = rw.Write([]byte(`{"data":[` + strings.Join(b, ",") + `]}`))
	}))
	t.Cleanup(srv.Close)
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(path, body string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	models := func(key string) []string {
		t.Helper()
		p, err := provider.Find("relay")
		if err != nil {
			t.Fatal(err)
		}
		return p.AccountModels[provider.KeyID(key)]
	}
	post("/api/provider/save", `{"id":"relay","name":"Relay","chat":"`+srv.URL+`/v1","key":"sk-a","new":true}`)
	post("/api/provider/models", `{"id":"relay","typed":true}`)
	post("/api/keys/add", `{"id":"relay","key":"sk-b"}`)
	post("/api/keys/add", `{"id":"relay","key":"sk-c"}`)
	post("/api/provider/models", `{"id":"relay","typed":true}`)
	for key, want := range map[string][]string{"sk-a": {"m1", "m2", "m3", "m4"}, "sk-b": {"m1", "m2"}, "sk-c": {"m3", "m5"}} {
		if got := models(key); !slices.Equal(got, want) {
			t.Errorf("%s after Fetch models: %v, want its own list %v ticked", key, got, want)
		}
	}

	// the user sets sk-c to All models and un-ticks m2 for sk-b, then saves
	// the editor; neither is undone by the save or a Fetch
	post("/api/provider/accountmodels", `{"id":"relay","account":"`+provider.KeyID("sk-c")+`","allow":[]}`)
	post("/api/provider/accountmodels", `{"id":"relay","account":"`+provider.KeyID("sk-b")+`","allow":["m1"]}`)
	post("/api/provider/save", `{"id":"relay","from":"relay","name":"Relay","chat":"`+srv.URL+`/v1","key":""}`)
	post("/api/provider/models", `{"id":"relay","typed":true}`)
	if got := models("sk-c"); got != nil {
		t.Errorf("sk-c set to All models, then saved and fetched: %v", got)
	}
	if got := models("sk-b"); !slices.Equal(got, []string{"m1"}) {
		t.Errorf("sk-b's un-tick undone: %v", got)
	}
}
