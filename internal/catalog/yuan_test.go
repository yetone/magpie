package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

// A models.dev provider's currency, which its prices (always in dollars,
// converted from the yuan for China's sellers) don't say.
func TestPricedInYuan(t *testing.T) {
	// models.dev's own names and API URLs for these ids, as of 2026-11-13
	freshCache(t)
	b, err := os.ReadFile(filepath.Join("testdata", "models.dev-yuan-2026-11-13.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(CachePath(), b, 0o644); err != nil {
		t.Fatal(err)
	}
	Reset()
	for id, want := range map[string]bool{
		"deepseek": true, "zhipuai": true, "moonshotai-cn": true, "kimi-code-plan-cn": true,
		"stepfun": true, "minimax-cn": true, "alibaba-cn": true, "siliconflow-cn": true, "volcengine": true,
		"xiaomi-token-plan-cn": true, " DeepSeek ": true,
		"openai": false, "anthropic": false, "moonshotai": false, "zai": false, "minimax": false,
		"openrouter": false, "xiaomi": false, "": false, "no-such-provider": false,
	} {
		if got := PricedInYuan(id); got != want {
			t.Errorf("PricedInYuan(%q) = %v, want %v", id, got, want)
		}
	}
	if h := APIHost("deepseek"); h != "api.deepseek.com" {
		t.Errorf("APIHost(deepseek) = %q", h)
	}
	if h := APIHost("no-such-provider"); h != "" {
		t.Errorf("APIHost(no-such-provider) = %q, want none", h)
	}
	for base, want := range map[string]bool{"https://open.bigmodel.cn/api": true, "https://api.z.ai/api": false, "http://x.cn:8080/v1": true, "https://cn.example.com": false, "": false} {
		if got := YuanHost(base); got != want {
			t.Errorf("YuanHost(%q) = %v, want %v", base, got, want)
		}
	}
}
