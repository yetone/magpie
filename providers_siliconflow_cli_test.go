package main

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// CLI region selection is persisted with the matching console and catalogue.
func TestSiliconFlowRegionPairs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, c := range []struct{ region, host, catalog, website string }{
		{"cn", "https://api.siliconflow.cn/v1", "siliconflow-cn", "https://cloud.siliconflow.cn"},
		{"intl", "https://api.siliconflow.com/v1", "siliconflow", "https://cloud.siliconflow.com"},
	} {
		p, err := provider.FromPreset("siliconflow")
		if err != nil {
			t.Fatal(err)
		}
		p.Key = "sk-" + c.region
		if err := applyPairs(&p, []string{"region=" + c.region}); err != nil {
			t.Fatal(err)
		}
		id, err := provider.Add(p)
		if err != nil {
			t.Fatal(err)
		}
		got, err := provider.Find(id)
		if err != nil || got.Chat != c.host || got.Catalog != c.catalog || got.Website != c.website || got.KeysURL != c.website+"/account/ak" {
			t.Fatalf("%s CLI provider: %+v, %v", c.region, got, err)
		}
	}
}
