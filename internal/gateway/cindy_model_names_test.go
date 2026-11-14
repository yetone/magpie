package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// Cindy's model discovery (parseModelsListResponse in its app) names a row
// by display_name before name, and showed magpie's GLM-5.3-Flash of three
// providers three times the same (cdredfox on X). Cindy is told each model
// by the name the agents' lists give it: with its provider's after it, and
// with the provider left out, plain only where no other has that name. A
// name the user gave a model is kept as they wrote it under "own". Another
// client's display_name stays the name alone.
func TestCindyDiscoveryNamesModelsApart(t *testing.T) {
	fresh(t)
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	data := `{"or":{"models":{"glm-5.3-flash":{"id":"glm-5.3-flash","name":"GLM-5.3-Flash"},"kimi":{"id":"kimi","name":"Kimi"}}},"zp":{"models":{"glm-5.3-flash":{"id":"glm-5.3-flash","name":"GLM-5.3-Flash"}}},"sf":{"models":{"glm-flash":{"id":"glm-flash","name":"GLM-5.3-Flash"}}}}`
	if err := os.WriteFile(catalog.CachePath(), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	for _, p := range []provider.Provider{
		{ID: "or", Name: "OpenRouter", Catalog: "or", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"glm-5.3-flash", "kimi"}},
		{ID: "zp", Name: "Zhipu", Catalog: "zp", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"glm-5.3-flash"}},
		{ID: "sf", Name: "SiliconFlow", Catalog: "sf", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"glm-flash"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SetModelName("sf/glm-flash", "My Flash"); err != nil {
		t.Fatal(err)
	}
	list := func(cindy bool) map[string]string {
		req := httptest.NewRequest("GET", "/v1/models", nil)
		if cindy {
			// Cindy's import link gives it the cindy key (CindyLink)
			req.Header.Set("Authorization", "Bearer "+TokenFor("cindy"))
		}
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, req)
		var out struct {
			Data []struct {
				ID      string `json:"id"`
				Display string `json:"display_name"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, m := range out.Data {
			got[m.ID] = m.Display
		}
		return got
	}
	for _, c := range []struct{ mode, or, zp, sf, kimi string }{
		{provider.SuffixOn, "GLM-5.3-Flash · OpenRouter", "GLM-5.3-Flash · Zhipu", "My Flash · SiliconFlow", "Kimi · OpenRouter"},
		{provider.SuffixOwn, "GLM-5.3-Flash · OpenRouter", "GLM-5.3-Flash · Zhipu", "My Flash", "Kimi · OpenRouter"},
		{provider.SuffixOff, "GLM-5.3-Flash · OpenRouter", "GLM-5.3-Flash · Zhipu", "My Flash", "Kimi"},
	} {
		if err := provider.SetSuffixMode(c.mode); err != nil {
			t.Fatal(err)
		}
		got := list(true)
		if got["or/glm-5.3-flash"] != c.or || got["zp/glm-5.3-flash"] != c.zp || got["sf/glm-flash"] != c.sf || got["or/kimi"] != c.kimi {
			t.Fatalf("%s: Cindy told %v", c.mode, got)
		}
		other := list(false)
		if other["or/glm-5.3-flash"] != "GLM-5.3-Flash" || other["zp/glm-5.3-flash"] != "GLM-5.3-Flash" || other["or/kimi"] != "Kimi" {
			t.Fatalf("%s: another client told %v", c.mode, other)
		}
	}
}
