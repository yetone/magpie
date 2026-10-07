package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

var siliconFlowRegions = []struct {
	region, host, catalog, website string
}{
	{"cn", "https://api.siliconflow.cn", "siliconflow-cn", "https://cloud.siliconflow.cn"},
	{"intl", "https://api.siliconflow.com", "siliconflow", "https://cloud.siliconflow.com"},
}

// A region selects the API, key console and catalogue together, including
// providers imported from another app and links shared by the vendor.
func TestSiliconFlowRegions(t *testing.T) {
	for _, c := range siliconFlowRegions {
		t.Run(c.region, func(t *testing.T) {
			p, err := ParseImport("magpie://import?preset=siliconflow&region=" + c.region)
			if err != nil {
				t.Fatal(err)
			}
			if p.Chat != c.host+"/v1" || p.Responses != "" || p.Anthropic != "" || p.Catalog != c.catalog || p.Website != c.website || p.KeysURL != c.website+"/account/ak" {
				t.Fatalf("regional provider: %+v", p)
			}
			im, why := imported("SiliconFlow", "sk-test", endpoints{chat: c.host + "/v1/"}, nil)
			if why != "" || im.Preset != "siliconflow" || im.Chat != p.Chat || im.Catalog != p.Catalog || im.KeysURL != p.KeysURL || im.Website != p.Website {
				t.Fatalf("app import: %+v, %s", im, why)
			}
			im, why = imported("Custom path", "sk-test", endpoints{chat: c.host + "/custom/v1"}, nil)
			if why != "" || im.Preset != "" || im.Chat != c.host+"/custom/v1" || im.Catalog != c.catalog {
				t.Fatalf("custom-path import: %+v, %s", im, why)
			}
			if IconForCatalog(c.catalog) != "siliconcloud-color" {
				t.Fatal("regional catalogue lost SiliconFlow's icon")
			}
		})
	}
	p, err := FromPreset("siliconflow")
	if err != nil || p.Chat != siliconFlowRegions[0].host+"/v1" || p.Catalog != "siliconflow-cn" {
		t.Fatalf("China default: %+v, %v", p, err)
	}
	if _, err := ParseImport("magpie://import?preset=siliconflow&region=nowhere"); err == nil {
		t.Fatal("unknown region accepted")
	}
}

// Old China providers keep their ID, keys and picks while their metadata
// follows the region. A catalogue supplied by the user stays theirs.
func TestSiliconFlowSavedProviders(t *testing.T) {
	isolate(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	old := `{"providers":[{"id":"siliconflow","name":"SiliconFlow","preset":"siliconflow","key":"sk-cn","chat":"https://api.siliconflow.cn/v1","catalog":"siliconflow","models":["Qwen/Qwen2.5-7B-Instruct"]}]}`
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := Find("siliconflow")
	if err != nil {
		t.Fatal(err)
	}
	if p.Catalog != "siliconflow-cn" || p.Key != "sk-cn" || p.Chat != siliconFlowRegions[0].host+"/v1" || !slices.Equal(p.Models, []string{"Qwen/Qwen2.5-7B-Instruct"}) {
		t.Fatalf("old China provider: %+v", p)
	}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	intl, err := ParseImport("magpie://import?preset=siliconflow&region=intl&key=sk-intl")
	if err != nil {
		t.Fatal(err)
	}
	id, err := Add(intl)
	if err != nil {
		t.Fatal(err)
	}
	if id == p.ID {
		t.Fatal("international account replaced the China provider")
	}
	q, err := Find(id)
	if err != nil || q.Key != "sk-intl" || q.Chat != intl.Chat || q.Catalog != "siliconflow" {
		t.Fatalf("international provider: %+v, %v", q, err)
	}
	p, err = Find("siliconflow")
	if err != nil || p.Key != "sk-cn" || p.Chat != siliconFlowRegions[0].host+"/v1" {
		t.Fatalf("China provider after international add: %+v, %v", p, err)
	}
	for _, c := range siliconFlowRegions {
		p.Chat = c.host + "/v1"
		p.Catalog = "siliconflow"
		n := normalize(*p)
		if n.Catalog != c.catalog || n.Website != c.website || n.KeysURL != c.website+"/account/ak" {
			t.Fatalf("switch to %s: %+v", c.region, n)
		}
		p.Catalog = "deepseek"
		if n := normalize(*p); n.Catalog != "deepseek" {
			t.Fatalf("custom catalogue overwritten: %q", n.Catalog)
		}
	}
}

// Each account asks its own host with its own key and uses that region's
// metadata, even when the two accounts serve the same model ID.
func TestSiliconFlowRegionalModels(t *testing.T) {
	isolate(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte(`{
	"siliconflow-cn":{"models":{"Qwen/Qwen2.5-7B-Instruct":{"id":"Qwen/Qwen2.5-7B-Instruct","name":"China Qwen","cost":{"input":0.1,"output":0.2}}}},
	"siliconflow":{"models":{"Qwen/Qwen2.5-7B-Instruct":{"id":"Qwen/Qwen2.5-7B-Instruct","name":"Global Qwen","cost":{"input":0.3,"output":0.4}}}}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys := map[string]string{"api.siliconflow.cn": "Bearer sk-cn", "api.siliconflow.com": "Bearer sk-intl"}
		if want, ok := keys[r.Header.Get("X-Host")]; !ok || r.Header.Get("Authorization") != want || r.URL.Path != "/v1/models" {
			t.Errorf("wrong regional request: %s %v", r.URL, r.Header)
			http.Error(w, "wrong region or key", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"object":"list","data":[{"id":"Qwen/Qwen2.5-7B-Instruct","object":"model","created":0,"owned_by":""}]}`))
	}))
	defer srv.Close()
	oldTransport := http.DefaultClient.Transport
	http.DefaultClient.Transport = rewrite{srv}
	t.Cleanup(func() { http.DefaultClient.Transport = oldTransport })
	for i, c := range siliconFlowRegions {
		p, err := ParseImport("magpie://import?preset=siliconflow&region=" + c.region + "&key=sk-" + c.region)
		if err != nil {
			t.Fatal(err)
		}
		id, err := Add(p)
		if err != nil {
			t.Fatal(err)
		}
		p.ID = id
		if _, err := p.Fetch(context.Background()); err != nil {
			t.Fatal(err)
		}
		wantName := []string{"China Qwen", "Global Qwen"}[i]
		if got := p.Available(); len(got) != 1 || got[0].Name != wantName {
			t.Fatalf("%s metadata: %+v", c.region, got)
		}
		price, ok := p.ListPrice("Qwen/Qwen2.5-7B-Instruct")
		if want := []float64{0.1, 0.3}[i]; !ok || price.Input != want {
			t.Fatalf("%s input price: %+v, %v", c.region, price, ok)
		}
	}
}
