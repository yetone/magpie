package provider

import (
	"net/url"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
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

// SiliconFlow's international site is another account: its endpoint, key
// page and console, while the preset's default stays the China site.
func TestSiliconFlowInternational(t *testing.T) {
	p, err := ParseImport("magpie://import?preset=siliconflow&region=intl")
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != "https://api.siliconflow.com/v1" || p.KeysURL != "https://cloud.siliconflow.com/account/ak" || p.Website != "https://cloud.siliconflow.com" {
		t.Fatalf("intl: got %q %q %q", p.Chat, p.KeysURL, p.Website)
	}
	if src, ok := balanceSourceOf(p); !ok || !strings.Contains(src.url, "api.siliconflow.com") {
		t.Fatalf("intl balance: %+v %v", src, ok)
	}
	d, err := FromPreset("siliconflow")
	if err != nil {
		t.Fatal(err)
	}
	if d.Chat != "https://api.siliconflow.cn/v1" || d.KeysURL != "https://cloud.siliconflow.cn/account/ak" {
		t.Fatalf("default: got %q %q", d.Chat, d.KeysURL)
	}
	pr := Preset("siliconflow")
	if r := d.regionOf(pr); r == nil || r.ID != "cn" {
		t.Fatalf("a China provider saved before the regions sits at %+v", r)
	}
	if r := p.regionOf(pr); r == nil || r.ID != "intl" {
		t.Fatalf("the international one sits at %+v", r)
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

// Bailian's decision regions are different hosts. The CLI's region= and
// plan= put the provider on that host; an import link that names the same
// region kept the preset's Beijing workspace host, while the key page
// followed the region.
func TestParseImportBailianDecisionRegion(t *testing.T) {
	sg, err := ParseImport("magpie://import?preset=bailian-decision&region=ap-southeast-1")
	if err != nil {
		t.Fatal(err)
	}
	if sg.Decide != "https://{WorkspaceId}.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1" {
		t.Fatalf("Singapore decide = %q", sg.Decide)
	}
	if sg.KeysURL != "https://modelstudio.console.alibabacloud.com/?tab=playground#/api-key" {
		t.Fatalf("Singapore keys = %q", sg.KeysURL)
	}
	plan, err := ParseImport("magpie://import?preset=bailian-decision&region=token-plan")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Decide != "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1" {
		t.Fatalf("Token Plan decide = %q", plan.Decide)
	}
	if plan.KeysURL != "https://bailian.console.aliyun.com/cn-beijing/subscription/token-plan/personal" {
		t.Fatalf("Token Plan keys = %q", plan.KeysURL)
	}
	bj, err := ParseImport("magpie://import?preset=bailian-decision&region=cn-beijing")
	if err != nil {
		t.Fatal(err)
	}
	if bj.Decide != "https://{WorkspaceId}.cn-beijing.maas.aliyuncs.com/compatible-mode/v1" {
		t.Fatalf("Beijing decide = %q", bj.Decide)
	}
}

// A Gemini-compatible relay is added in the app, the TUI and
// `magpie provider set` with a Gemini URL alone (#1346, #2201); a link
// naming one was refused as giving no base URL, and a gemini= beside
// chat= was dropped without a word.
func TestParseImportGemini(t *testing.T) {
	p, err := ParseImport("magpie://import?name=Gemini%20Relay&gemini=https://relay.example/v1beta&key=sk-g")
	if err != nil {
		t.Fatal(err)
	}
	if p.Gemini != "https://relay.example/v1beta" || p.Chat != "" || p.Responses != "" || p.Anthropic != "" {
		t.Fatalf("gemini only: got %+v", p)
	}
	p, err = ParseImport("magpie://import?name=X&chat=https://relay.example/v1/&gemini=https://relay.example/v1beta")
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != "https://relay.example/v1" || p.Gemini != "https://relay.example/v1beta" {
		t.Fatalf("chat and gemini: got %+v", p)
	}
	if _, err := ParseImport("magpie://import?name=X&gemini=not-a-url"); err == nil {
		t.Fatal("a bad gemini= was accepted")
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

// Google Vertex AI is asked at the user's own Google Cloud project with
// their own credentials, none of which a link has: one naming it is
// refused, saying how it is added, rather than offered as a provider that
// wants a key and can't be saved.
func TestParseImportVertex(t *testing.T) {
	for _, link := range []string{"magpie://import?preset=google-vertex", "magpie://import?preset=Google-Vertex&key=AQ.abc&name=Vertex"} {
		_, err := ParseImport(link)
		if err == nil || !strings.Contains(err.Error(), "magpie provider add google-vertex project=") {
			t.Errorf("%s: %v", link, err)
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

// A long name in Chinese is cut to 80 bytes where a character begins, not
// through one: half a character is invalid UTF-8 the GUI shows as � (#2416).
func TestParseImportNameCutKeepsCharactersWhole(t *testing.T) {
	q := url.Values{
		"name": {strings.Repeat("鱼", 30) + "-relay"},
		"chat": {"https://relay.example/v1"},
		"key":  {"sk-relay"},
	}
	p, err := ParseImport("magpie://import?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(p.Name) || len(p.Name) > 80 || p.Name != strings.Repeat("鱼", 26) {
		t.Fatalf("name %q (%d bytes)", p.Name, len(p.Name))
	}
}

// A link names its balance query, which is asked with the key: only on a
// host its endpoints are on, over https, by GET or POST.
func TestParseImportBalance(t *testing.T) {
	q := url.Values{
		"name":           {"Relay"},
		"chat":           {"https://relay.example/v1"},
		"balance":        {"https://relay.example/api/usage/token"},
		"balance_path":   {"data.total_available"},
		"balance_method": {"post"},
	}
	p, err := ParseImport("magpie://import?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if p.BalanceURL != "https://relay.example/api/usage/token" || p.BalancePath != "data.total_available" || p.BalanceMethod != "POST" {
		t.Fatalf("balance %q %q %q", p.BalanceURL, p.BalancePath, p.BalanceMethod)
	}
	// a preset's endpoint host counts as the link's own
	q = url.Values{"preset": {"deepseek"}, "balance": {"https://api.deepseek.com/user/balance"}, "balance_method": {"GET"}}
	if p, err = ParseImport("magpie://import?" + q.Encode()); err != nil || p.BalanceURL == "" || p.BalanceMethod != "" {
		t.Fatalf("preset: %+v %v", p.BalanceURL, err)
	}
	for _, bad := range []url.Values{
		{"balance": {"https://evil.example/steal"}},
		{"balance": {"https://relay.example.evil.example/b"}},
		{"balance": {"http://relay.example/api/usage/token"}},
		{"balance": {"https://user@relay.example/b"}},
		{"balance": {"https://relay.example/b"}, "balance_method": {"DELETE"}},
		{"balance": {"https://relay.example/b"}, "balance_path": {"a\nb"}},
		{"balance_path": {"data.balance"}},
		{"balance_method": {"POST"}},
	} {
		bad.Set("name", "Relay")
		bad.Set("chat", "https://relay.example/v1")
		if p, err := ParseImport("magpie://import?" + bad.Encode()); err == nil {
			t.Errorf("%v: took balance %q", bad, p.BalanceURL)
		}
	}
	// none named, none set
	if p, err := ParseImport("magpie://import?name=Relay&chat=https://relay.example/v1"); err != nil || p.BalanceURL != "" || p.BalancePath != "" || p.BalanceMethod != "" {
		t.Fatalf("no balance: %+v %v", p, err)
	}
}
