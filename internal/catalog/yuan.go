package catalog

import (
	"net/url"
	"strings"
)

// yuanSellers are the models.dev providers that sell in China's yuan
// whose id and name don't say so: DeepSeek's own API (its balance comes
// back in CNY), Volcengine's Ark, Tencent's, Zhipu's bigmodel.cn under the
// plain zhipuai, Qiniu's, Ant's Bailing. Xiaomi's plain "xiaomi" is left
// out: its pay-as-you-go is sold abroad too.
var yuanSellers = map[string]bool{
	"deepseek": true, "volcengine": true, "volcengine-coding-plan": true,
	"tencent-tokenhub": true, "tencent-token-plan": true, "tencent-coding-plan": true,
	"zhipuai": true, "zhipuai-coding-plan": true, "qiniu-ai": true, "bailing": true,
}

// PricedInYuan is whether a models.dev provider sells in China's yuan
// rather than in dollars: one of yuanSellers, or one whose id ends in
// -cn, whose name says "(China)" or whose API is on a .cn host. Its
// models.dev prices are still in dollars, converted from the yuan; only
// how a cost of it is shown (Settings' currency "provider") asks this.
func PricedInYuan(id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return false
	}
	if yuanSellers[id] || strings.HasSuffix(id, "-cn") || strings.Contains(id, "-cn-") {
		return true
	}
	p, ok := load()[id]
	if !ok {
		return false
	}
	return strings.Contains(p.Name, "(China)") || YuanHost(p.API)
}

// APIHost is the host models.dev says a provider serves its models at
// (api.deepseek.com), lower case; "" when it isn't in the catalog.
func APIHost(id string) string {
	p, ok := load()[strings.ToLower(strings.TrimSpace(id))]
	if !ok {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(p.API))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// YuanHost is whether a base URL is on a .cn host, a mainland Chinese
// vendor's, which sells in yuan.
func YuanHost(base string) bool {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return false
	}
	return strings.HasSuffix(strings.ToLower(u.Hostname()), ".cn")
}
