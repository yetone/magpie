package provider

import (
	"net/url"
	"slices"
	"testing"
)

func TestParseImportPreset(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=deepseek&key=sk-abc123&models=deepseek-v4-pro,%20deepseek-v4-flash")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "deepseek" || p.Preset != "deepseek" || p.Key != "sk-abc123" || p.Chat == "" {
		t.Fatalf("got %+v", p)
	}
	if !slices.Equal(p.Models, []string{"deepseek-v4-pro", "deepseek-v4-flash"}) {
		t.Fatalf("models %q", p.Models)
	}
}

// A region with a key page of its own (Qianfan's pay as you go, on the
// IAM page) names it in the parsed provider, before it is ever saved.
func TestParseImportRegionKeys(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=baidu-qianfan&region=api")
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != "https://qianfan.baidubce.com/v2" || p.KeysURL != "https://console.bce.baidu.com/iam/#/iam/apikey/list" {
		t.Fatalf("got %q %q", p.Chat, p.KeysURL)
	}
}

func TestParseImportCustom(t *testing.T) {
	q := url.Values{
		"name":      {"My Relay"},
		"chat":      {"https://relay.example/v1/"},
		"anthropic": {"https://relay.example"},
		"key":       {"sk-relay"},
		"website":   {"https://relay.example"},
		"keys":      {"javascript:alert(1)"},
		"catalog":   {"openai"},
	}
	p, err := ParseImport("magpie://import?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "my-relay" || p.Name != "My Relay" || p.Chat != "https://relay.example/v1" || p.Anthropic != "https://relay.example" || p.Preset != "" {
		t.Fatalf("got %+v", p)
	}
	if p.Website != "https://relay.example" || p.KeysURL != "" {
		t.Fatalf("pages: %q %q", p.Website, p.KeysURL)
	}
}

func TestParseImportRegion(t *testing.T) {
	var pr *PresetDef
	for i := range presets {
		if len(presets[i].Regions) > 1 {
			pr = &presets[i]
			break
		}
	}
	if pr == nil {
		t.Skip("no preset with regions")
	}
	r := pr.Regions[1]
	p, err := ParseImport("magpie://import?preset=" + pr.ID + "&region=" + r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != r.Chat || p.Anthropic != r.Anthropic || p.Preset != pr.ID {
		t.Fatalf("got %+v, want region %+v", p, r)
	}
	if _, err := ParseImport("magpie://import?preset=" + pr.ID + "&region=nowhere"); err == nil {
		t.Fatal("unknown region accepted")
	}
}

func TestParseImportRejects(t *testing.T) {
	for _, link := range []string{
		"https://usemagpie.ai/import?preset=deepseek",
		"magpie://delete?id=deepseek",
		"magpie://import",
		"magpie://import?preset=nope",
		"magpie://import?name=X",
		"magpie://import?name=X&chat=http://relay.example/v1",
		"magpie://import?name=X&chat=https://user:pw@relay.example/v1",
		"magpie://import?name=X&chat=file:///etc/passwd",
		"magpie://import?name=magpie&chat=https://x.example/v1",
		"magpie://import?name=%E2%80%94&chat=https://x.example/v1",
		"magpie://import?preset=deepseek&key=sk%0Aevil",
	} {
		if p, err := ParseImport(link); err == nil {
			t.Errorf("%s: accepted as %+v", link, p)
		}
	}
}

func TestParseImportIcon(t *testing.T) {
	q := url.Values{
		"name": {"My Relay"},
		"chat": {"https://relay.example/v1"},
		"icon": {"https://relay.example/logo.svg?v=2"},
	}
	p, err := ParseImport("magpie://import?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if p.IconURL != "https://relay.example/logo.svg?v=2" {
		t.Fatalf("iconUrl = %q", p.IconURL)
	}
	// an explicit icon wins over the catalog's own logo
	q.Set("catalog", "openai")
	if p, err = ParseImport("magpie://import?" + q.Encode()); err != nil || p.IconURL == "" {
		t.Fatalf("with a catalog: %+v, %v", p, err)
	}
	for _, bad := range []string{
		"http://relay.example/logo.svg", // plain http
		"file:///etc/passwd",
		"https://user:pw@relay.example/logo.svg",
		"https://relay.example/logo.svg#x",
		"https://localhost/logo.svg",
		"https://127.0.0.1/logo.svg",
		"not a url",
	} {
		q.Set("icon", bad)
		if _, err := ParseImport("magpie://import?" + q.Encode()); err == nil {
			t.Errorf("icon %q accepted", bad)
		}
	}
}

func TestParseImportLocalHTTP(t *testing.T) {
	for _, host := range []string{"localhost:11434", "127.0.0.1:8080", "192.168.1.20:8000", "box.local"} {
		if _, err := ParseImport("magpie://import?name=Local&chat=http://" + host + "/v1"); err != nil {
			t.Errorf("%s: %v", host, err)
		}
	}
}

func TestParseImportForms(t *testing.T) {
	for _, link := range []string{"magpie:import?preset=deepseek", "magpie:///import?preset=deepseek", "MAGPIE://import/?preset=deepseek"} {
		if _, err := ParseImport(link); err != nil {
			t.Errorf("%s: %v", link, err)
		}
	}
}
