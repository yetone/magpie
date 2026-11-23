package provider

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// useZhipuPlanCatalog puts testdata/zhipu_plan_catalog.json in the catalog
// cache: models.dev's zhipuai, zhipuai-coding-plan, zai-coding-plan and
// openrouter entries for the GLM models below, as synced 2026-11-23, the
// day GLM-5.4 reached the Coding Plan (and the plans' catalogs alone).
func useZhipuPlanCatalog(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	b, err := os.ReadFile(filepath.Join("testdata", "zhipu_plan_catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

// ApocalypseYun on X: a GLM Coding Plan's newly listed glm-5.4 read as a
// text model of no known window. Zhipu's /models gives ids alone, and the
// plan's models were looked up in zhipuai, pay as you go's catalog, which
// doesn't have GLM-5.4 yet; models.dev's zhipuai-coding-plan does, taking
// images with a 1M window. Each plan region of Zhipu and Z.ai now looks
// its models up in its plan's catalog first, then zhipuai; pay as you go
// is unchanged, a provider saved with the old catalog follows its region,
// and the plan's $0 is never read as a price.
func TestZhipuPlanModelsFromPlanCatalog(t *testing.T) {
	useZhipuPlanCatalog(t)
	// as the plans' /models list them: ids, nothing else
	listed := []catalog.Model{{ID: "glm-5.3", Name: "glm-5.3"}, {ID: "glm-5.4", Name: "glm-5.4"}, {ID: "glm-5.3-flash", Name: "glm-5.3-flash"}}
	for _, c := range []struct {
		preset, region string
		images, known  bool // glm-5.4's image input, and whether its window is known
	}{
		{"zhipu", "coding", true, true},
		{"zai", "coding", true, true},
		// pay as you go's catalog doesn't list GLM-5.4: nothing is borrowed
		{"zhipu", "api", false, false},
		{"zai", "api", false, false},
	} {
		pr := Preset(c.preset)
		var r Region
		for _, x := range pr.Regions {
			if x.ID == c.region {
				r = x
			}
		}
		if r.ID == "" {
			t.Fatalf("%s has no region %q", c.preset, c.region)
		}
		// saved with the preset's catalog, as every provider was before
		p := Provider{ID: c.preset, Name: pr.Name, Preset: c.preset, Catalog: pr.Catalog, Key: "k",
			Chat: r.Chat, Responses: r.Responses, Anthropic: r.Anthropic, Models: []string{"glm-5.3", "glm-5.4", "glm-5.3-flash"}}
		if err := catalog.SaveLive(c.preset, "", listed); err != nil {
			t.Fatal(err)
		}
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
		got, err := Find(c.preset)
		if err != nil {
			t.Fatal(err)
		}
		byID := map[string]catalog.Model{}
		for _, m := range got.Exposed() {
			byID[m.ID] = m
		}
		m, ok := byID["glm-5.4"]
		if !ok {
			t.Fatalf("%s/%s: glm-5.4 not exposed", c.preset, c.region)
		}
		if m.Images != c.images {
			t.Errorf("%s/%s at %q: glm-5.4 Images = %v, want %v", c.preset, c.region, got.Catalog, m.Images, c.images)
		}
		if known := m.Context == 1_000_000; known != c.known {
			t.Errorf("%s/%s: glm-5.4 Context = %d, want known %v", c.preset, c.region, m.Context, c.known)
		}
		// the plan's catalog says GLM-5.3 is text-only, as pay as you go's does
		if g := byID["glm-5.3"]; g.Images || g.ImageInput == nil {
			t.Errorf("%s/%s: glm-5.3 Images %v ImageInput %v, want a known text model", c.preset, c.region, g.Images, g.ImageInput)
		}
		if g := byID["glm-5.3-flash"]; !g.Images {
			t.Errorf("%s/%s: glm-5.3-flash takes no images", c.preset, c.region)
		}
		// a call is priced at pay as you go's price, never the plan's $0
		want := catalog.Price{Input: 1.19516, Output: 4.18306, CacheRead: 0.29879}
		if pr, ok := got.ListPrice("glm-5.3"); !ok || !pr.Same(want) {
			t.Errorf("%s/%s: glm-5.3 priced %+v %v, want %+v", c.preset, c.region, pr, ok, want)
		}
		if pr, ok := got.ListPrice("glm-5.4"); ok {
			t.Errorf("%s/%s: glm-5.4 priced %+v, want unpriced (its plan's $0 isn't a price)", c.preset, c.region, pr)
		}
		if err := Delete(c.preset); err != nil {
			t.Fatal(err)
		}
	}

	// another vendor serving the same id keeps its own catalog's word: a
	// relay on OpenRouter's catalog, which doesn't list glm-5.4, doesn't
	// borrow the plan's
	if err := catalog.SaveLive("relay", "", []catalog.Model{{ID: "glm-5.4", Name: "glm-5.4"}, {ID: "z-ai/glm-5.3", Name: "z-ai/glm-5.3"}}); err != nil {
		t.Fatal(err)
	}
	relay := Provider{ID: "relay", Name: "Relay", Catalog: "openrouter", Key: "k", Chat: "https://relay.example/v1", Models: []string{"glm-5.4", "z-ai/glm-5.3"}}
	if err := Save(relay); err != nil {
		t.Fatal(err)
	}
	got, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Catalogs(), []string{"openrouter"}) {
		t.Errorf("relay catalogs %v", got.Catalogs())
	}
	for _, m := range got.Exposed() {
		if m.Images || m.Context == 1_000_000 && m.ID == "glm-5.4" {
			t.Errorf("relay %s: Images %v Context %d, want its own catalog's", m.ID, m.Images, m.Context)
		}
	}
}

// Each region of Zhipu and Z.ai names its catalogs: a provider saved with
// the preset's, or moved from the other region, follows the region it is
// at, and one the user named stays theirs.
func TestZhipuRegionCatalogs(t *testing.T) {
	useZhipuPlanCatalog(t)
	want := map[string]map[string][]string{
		"zhipu": {"coding": {"zhipuai-coding-plan", "zhipuai"}, "api": {"zhipuai"}},
		"zai":   {"coding": {"zai-coding-plan", "zhipuai"}, "api": {"zhipuai"}},
	}
	for preset, regions := range want {
		pr := Preset(preset)
		for _, r := range pr.Regions {
			cats, ok := regions[r.ID]
			if !ok {
				t.Fatalf("%s region %q isn't known to the test", preset, r.ID)
			}
			froms := []string{pr.Catalog}
			for _, x := range pr.Regions {
				froms = append(froms, x.Catalog)
			}
			for _, from := range froms {
				p := normalize(Provider{ID: preset, Preset: preset, Key: "k", Chat: r.Chat, Responses: r.Responses, Anthropic: r.Anthropic, Catalog: from})
				if got := p.Catalogs(); !slices.Equal(got, cats) {
					t.Errorf("%s/%s saved with %q: catalogs %v, want %v", preset, r.ID, from, got, cats)
				}
			}
			p := normalize(Provider{ID: preset, Preset: preset, Key: "k", Chat: r.Chat, Anthropic: r.Anthropic, Catalog: "openrouter"})
			if got := p.Catalogs(); !slices.Equal(got, []string{"openrouter"}) {
				t.Errorf("%s/%s, the user's own catalog: %v", preset, r.ID, got)
			}
		}
	}
}
