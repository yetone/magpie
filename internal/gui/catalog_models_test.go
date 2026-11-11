package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// The model editor's Same as picker lists the synced models.dev catalog,
// one entry a model, from the provider that lists its window and price.
func TestCatalogModelsListsTheSyncedCatalogOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{
	  "moonshotai":{"id":"moonshotai","name":"Moonshot AI","models":{"kimi-k3":{"id":"kimi-k3","name":"Kimi K3","cost":{"input":0.6,"output":2.5},"limit":{"context":262144,"output":32768},"modalities":{"input":["text","image"],"output":["text"]}}}},
	  "relay":{"id":"relay","name":"Relay","models":{"moonshotai/kimi-k3":{"id":"moonshotai/kimi-k3","name":"Kimi K3"}, "other-1":{"id":"other-1","name":"Other"}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	mux := http.NewServeMux()
	catalogModelsRoutes(mux)
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, httptest.NewRequest("GET", "/api/catalog/models", nil))
	var got []catalogModel
	if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil {
		t.Fatal(err, rw.Body.String())
	}
	if len(got) != 2 {
		t.Fatalf("%+v; want kimi-k3 once and other-1", got)
	}
	var k catalogModel
	for _, m := range got {
		if m.ID == "kimi-k3" {
			k = m
		}
	}
	if k.Input != 0.6 || k.Cost != 2.5 || k.Context != 262144 || k.Output != 32768 || !k.Images || k.Maker != "Moonshot AI" {
		t.Errorf("kimi-k3 = %+v; want the provider that lists its window and price", k)
	}
}
