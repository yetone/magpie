package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// Settings' currency "By provider" shows a provider's costs in yuan when
// it sells in yuan (onkashiko on X: DeepSeek, Kimi CN, Zhipu, StepFun CN in
// CNY, the rest in USD). A twin abroad sharing a yuan seller's catalog
// (Z.ai's zhipuai, Kimi's moonshotai), or a relay made from a yuan preset
// at another site, stays in dollars.
func TestPricesInYuan(t *testing.T) {
	// models.dev's own names and API URLs for these ids, as of 2026-11-13,
	// in a cache of this test's own
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	b, err := os.ReadFile(filepath.Join("..", "catalog", "testdata", "models.dev-yuan-2026-11-13.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), b, 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	for _, c := range []struct {
		name string
		p    Provider
		want bool
	}{
		{"deepseek", Provider{ID: "deepseek", Preset: "deepseek", Chat: "https://api.deepseek.com/v1"}, true},
		{"deepseek, endpoints left to the preset", Provider{ID: "ds", Preset: "deepseek"}, true},
		{"a relay made from deepseek's preset", Provider{ID: "deepseek", Preset: "deepseek", Chat: "https://relay.example/v1"}, false},
		{"zhipu", Provider{ID: "zhipu", Preset: "zhipu", Chat: "https://open.bigmodel.cn/api/paas/v4", Catalog: "zhipuai"}, true},
		{"z.ai shares zhipu's catalog", Provider{ID: "zai", Preset: "zai", Chat: "https://api.z.ai/api/paas/v4", Catalog: "zhipuai"}, false},
		{"kimi china", Provider{ID: "moonshot-cn", Preset: "moonshot-cn", Chat: "https://api.moonshot.cn/v1", Catalog: "moonshotai"}, true},
		{"kimi", Provider{ID: "moonshot", Preset: "moonshot", Chat: "https://api.moonshot.ai/v1", Catalog: "moonshotai"}, false},
		{"kimi code china", Provider{ID: "kimi-code-cn", Preset: "kimi-code-cn", Chat: "https://api.kimi.com/coding/v1", Catalog: "kimi-code-plan-cn"}, true},
		{"stepfun china", Provider{ID: "stepfun-cn", Preset: "stepfun-cn", Chat: "https://api.stepfun.com/step_plan/v1"}, true},
		{"stepfun", Provider{ID: "stepfun", Preset: "stepfun", Chat: "https://api.stepfun.ai/step_plan/v1"}, false},
		{"minimax china", Provider{ID: "minimax-cn", Preset: "minimax-cn", Chat: "https://api.minimaxi.com/v1", Catalog: "minimax"}, true},
		{"xiaomi's china plan", Provider{ID: "xiaomi", Preset: "xiaomi", Chat: "https://token-plan-cn.xiaomimimo.com/v1", Catalog: "xiaomi-token-plan-cn, xiaomi"}, true},
		{"xiaomi's singapore plan", Provider{ID: "xiaomi", Preset: "xiaomi", Chat: "https://token-plan-sgp.xiaomimimo.com/v1", Catalog: "xiaomi-token-plan-sgp, xiaomi"}, false},
		{"volcengine", Provider{ID: "ark", Preset: "volcengine", Chat: "https://ark.cn-beijing.volces.com/api/v3"}, true},
		{"anthropic", Provider{ID: "anthropic", Preset: "anthropic", Anthropic: "https://api.anthropic.com"}, false},
		{"openai", Provider{ID: "openai", Preset: "openai", Chat: "https://api.openai.com/v1", Catalog: "openai"}, false},
		{"openrouter", Provider{ID: "openrouter", Preset: "openrouter", Chat: "https://openrouter.ai/api/v1", Catalog: "openrouter"}, false},
		{"a .cn relay of its own", Provider{ID: "mine", Chat: "https://api.example.cn/v1"}, true},
		{"a .cn host with a port", Provider{ID: "mine", Chat: "http://llm.example.cn:8080/v1"}, true},
		{"its own, at deepseek with deepseek's catalog", Provider{ID: "mine", Chat: "https://api.deepseek.com/v1", Catalog: "deepseek"}, true},
		{"its own relay given deepseek's catalog", Provider{ID: "mine", Chat: "https://relay.example/v1", Catalog: "deepseek"}, false},
		{"a plugin's china id", Provider{ID: "qoder-cn"}, true},
		{"its own, nothing said", Provider{ID: "mine", Chat: "https://relay.example/v1"}, false},
	} {
		if got := c.p.PricesInYuan(); got != c.want {
			t.Errorf("%s: PricesInYuan() = %v, want %v", c.name, got, c.want)
		}
	}
}
