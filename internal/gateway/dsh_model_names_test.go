package gateway

import (
	"cmp"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// DeepSeek Harness's model discovery reads /v1/models as llm-pi-ai's
// readListing does, a row's name first, then its display_name, and is told
// each model by the name the agents' lists give it: two providers' models
// of one name had the same name there (LeiWang_2000 on X, Kimi-K3.1-Pro of
// WorkBuddy and of Qoder CN). With the provider left out of names, the two
// keep theirs, and a model of a name of its own is plain. Another client
// is told no name, and display_name stays the name alone.
func TestDshDiscoveryNamesModelsApart(t *testing.T) {
	fresh(t)
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	data := `{"wb":{"models":{"kimi-k3.1-pro":{"id":"kimi-k3.1-pro","name":"Kimi-K3.1-Pro"},"glm-5":{"id":"glm-5","name":"GLM-5"}}},"qc":{"models":{"kmodel_latest":{"id":"kmodel_latest","name":"Kimi-K3.1-Pro"}}}}`
	if err := os.WriteFile(catalog.CachePath(), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	for _, p := range []provider.Provider{
		{ID: "wb", Name: "WorkBuddy", Catalog: "wb", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"kimi-k3.1-pro", "glm-5"}},
		{ID: "qc", Name: "Qoder CN", Catalog: "qc", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"kmodel_latest"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	list := func(ua string) (named, plain map[string]string) {
		req := httptest.NewRequest("GET", "/v1/models", nil)
		req.Header.Set("User-Agent", ua)
		if ua == dshUA {
			// the gateway's tests set no agent list, so its User-Agent
			// alone names no agent here (dshAsk)
			req.Header.Set("Authorization", "Bearer "+TokenFor("dsh"))
		}
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, req)
		var out struct {
			Data []struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				Display string `json:"display_name"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		named, plain = map[string]string{}, map[string]string{}
		for _, m := range out.Data {
			named[m.ID] = cmp.Or(m.Name, m.Display) // readListing's label()
			plain[m.ID] = m.Display
			if m.Name != "" && ua != dshUA {
				t.Fatalf("%s told %s a name: %q", ua, m.ID, m.Name)
			}
		}
		return named, plain
	}
	for _, c := range []struct{ mode, wb, qc, glm string }{
		{provider.SuffixOn, "Kimi-K3.1-Pro · WorkBuddy", "Kimi-K3.1-Pro · Qoder CN", "GLM-5 · WorkBuddy"},
		{provider.SuffixOff, "Kimi-K3.1-Pro · WorkBuddy", "Kimi-K3.1-Pro · Qoder CN", "GLM-5"},
	} {
		if err := provider.SetSuffixMode(c.mode); err != nil {
			t.Fatal(err)
		}
		named, plain := list(dshUA)
		if named["wb/kimi-k3.1-pro"] != c.wb || named["qc/kmodel_latest"] != c.qc || named["wb/glm-5"] != c.glm {
			t.Fatalf("%s: dsh named %v", c.mode, named)
		}
		if plain["wb/kimi-k3.1-pro"] != "Kimi-K3.1-Pro" || plain["qc/kmodel_latest"] != "Kimi-K3.1-Pro" {
			t.Fatalf("%s: display_name %v", c.mode, plain)
		}
		list("curl/8.7.1")
	}
}
