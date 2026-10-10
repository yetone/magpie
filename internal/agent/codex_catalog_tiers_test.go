package agent

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/codexcat"
	"github.com/yetone/magpie/internal/provider"
)

// The Codex CLI that names magpie its provider reads its models from
// magpie-models.json (model_catalog_json), which Codex prefers over any
// model_catalog_url. Its /fast is there only when the model's entry has
// service_tiers. A provider the user added by its address was offered
// Fast in the gateway's lists (#1234) but every entry in the file had
// service_tiers: [], so /fast never showed in the CLI (#1530, yulong-ge).
func TestCodexCatalogTiersReachDisk(t *testing.T) {
	home := syncHome(t)
	// the reporter's: a provider added by its address serving a GPT model
	if err := provider.Save(provider.Provider{ID: "sub", Name: "Sub", Key: "k", Responses: "http://127.0.0.1:1/v1", Models: []string{"gpt-6.1-sol", "glm-4.6"}}); err != nil {
		t.Fatal(err)
	}
	cx := codex(home)
	if err := cx.Fields[0].Set("sub/gpt-6.1-sol"); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Models []struct {
			Slug  string `json:"slug"`
			Tiers []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"service_tiers"`
		} `json:"models"`
	}
	raw := readFile(filepath.Join(home, ".codex", "magpie-models.json"))
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	tiers := map[string]string{}
	for _, m := range got.Models {
		s := ""
		for _, x := range m.Tiers {
			s += x.ID + ":" + x.Name + " "
		}
		tiers[m.Slug] = s
	}
	for slug, want := range map[string]string{"sub/gpt-6.1-sol": "priority:Fast ", "sub/glm-4.6": "", "relay/glm-4.6": ""} {
		if g, ok := tiers[slug]; !ok {
			t.Errorf("%s is not in magpie-models.json: %s", slug, raw)
		} else if g != want {
			t.Errorf("magpie-models.json offers %s %q, want %q", slug, g, want)
		}
	}

	// the file offers each model what GET /v1/codex/models offers it
	shown, _ := provider.CatalogFor("codex")
	if disk, api := codexcat.ServiceTiers(magpieModels("codex")), codexcat.ServiceTiers(provider.CodexCatalog(shown)); !reflect.DeepEqual(disk, api) {
		t.Errorf("tiers in magpie-models.json %v, in /v1/codex/models %v", disk, api)
	}
}
