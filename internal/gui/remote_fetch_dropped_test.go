package gui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// CryptoSarom on X: a remote magpie's editor, open while the other
// magpie's list dropped a routing group, held the group among its picks;
// the background follow had set it aside already, so Fetch models again
// dropped nothing, the editor kept it, and its Save put it back. Fetch
// models now names every pick set aside, and the editor takes them out.
func TestRemoteFetchNamesPicksSetAsideBefore(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("APPDATA", filepath.Join(h, "AppData"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(h, ".data"))
	var list atomic.Value
	list.Store(`{"data":[{"id":"fake/m-alpha"},{"id":"group/smart"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(rw, r)
			return
		}
		_, _ = rw.Write([]byte(list.Load().(string)))
	}))
	t.Cleanup(srv.Close)
	mux := http.NewServeMux()
	providerRoutes(mux, nil)

	if err := provider.Save(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Chat: srv.URL, Key: "sk-office",
		Models: []string{"fake/m-alpha", "group/smart"}}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("office")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	// group Smart removed there; the follow fetches the list in the background
	list.Store(`{"data":[{"id":"fake/m-alpha"}]}`)
	if p, err = provider.Find("office"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/models", strings.NewReader(`{"id":"office"}`)))
	if w.Code != 200 {
		t.Fatalf("Fetch models: %d %s", w.Code, w.Body)
	}
	var r struct {
		Dropped []string `json:"dropped"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.Dropped, []string{"group/smart"}) {
		t.Fatalf("Fetch models after the follow dropped group/smart names %v, so the open editor keeps it", r.Dropped)
	}
}
