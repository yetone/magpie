package provider

// A preset is a vendor magpie already knows: adding one only asks for the key.

// Kind groups presets in the picker.
type Kind string

const (
	KindVendor Kind = "vendor" // the model's own maker
	KindRelay  Kind = "relay"  // an aggregator / API relay reselling many vendors
	KindLocal  Kind = "local"  // something running on this machine
)

// PresetDef describes one preset.
type PresetDef struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Icon      string   `json:"icon"`
	Kind      Kind     `json:"kind"`
	Chat      string   `json:"chat,omitempty"`
	Responses string   `json:"responses,omitempty"`
	Anthropic string   `json:"anthropic,omitempty"`
	Decide    string   `json:"decide,omitempty"` // a decision API: the provider only routes (see decide.go)
	Catalog   string   `json:"catalog,omitempty"`
	Website   string   `json:"website,omitempty"`
	KeysURL   string   `json:"keysUrl,omitempty"`
	NoKey     bool     `json:"noKey,omitempty"`     // local servers: a key is optional
	Sponsored bool     `json:"sponsored,omitempty"` // shown first, with a tag
	Note      string   `json:"note,omitempty"`      // one line under the name
	Regions   []Region `json:"regions,omitempty"`   // base-URL choices (a relay's regional endpoints, a vendor's plans)
	// RegionLabel names what the Regions choose between, "Region" if unset.
	RegionLabel string `json:"regionLabel,omitempty"`
	// HeaderHints name optional request headers the vendor documents, which
	// the editor offers to add; their values are the user's to fill in.
	HeaderHints []string `json:"headerHints,omitempty"`
}

// Region is one base-URL option of a preset that offers several. The first
// is the default; picking another in the editor swaps the endpoints.
type Region struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Chat      string `json:"chat,omitempty"`
	Responses string `json:"responses,omitempty"`
	Anthropic string `json:"anthropic,omitempty"`
}

// presets are ordered as they appear in the picker.
var presets = []PresetDef{
	{ID: "anthropic", Name: "Anthropic", Icon: "claude-color", Kind: KindVendor, Catalog: "anthropic",
		Anthropic: "https://api.anthropic.com",
		Website:   "https://console.anthropic.com", KeysURL: "https://console.anthropic.com/settings/keys",
		// a key that reaches several workspaces names the one each request is for
		HeaderHints: []string{"anthropic-workspace-id"}},
	{ID: "openai", Name: "OpenAI", Icon: "openai", Kind: KindVendor, Catalog: "openai",
		Chat: "https://api.openai.com/v1", Responses: "https://api.openai.com/v1",
		Website: "https://platform.openai.com", KeysURL: "https://platform.openai.com/api-keys"},
	{ID: "google", Name: "Google Gemini", Icon: "gemini-color", Kind: KindVendor, Catalog: "google",
		Chat:    "https://generativelanguage.googleapis.com/v1beta/openai",
		Note:    "Gemini Developer API",
		Website: "https://aistudio.google.com", KeysURL: "https://aistudio.google.com/apikey"},
	{ID: "deepseek", Name: "DeepSeek", Icon: "deepseek-color", Kind: KindVendor, Catalog: "deepseek",
		Chat: "https://api.deepseek.com/v1", Responses: "https://api.deepseek.com/v1", Anthropic: "https://api.deepseek.com/anthropic",
		Website: "https://platform.deepseek.com", KeysURL: "https://platform.deepseek.com/api_keys"},
	{ID: "xai", Name: "xAI", Icon: "xai", Kind: KindVendor, Catalog: "xai",
		Chat: "https://api.x.ai/v1", Responses: "https://api.x.ai/v1", Anthropic: "https://api.x.ai",
		Website: "https://console.x.ai", KeysURL: "https://console.x.ai"},
	{ID: "moonshot", Name: "Kimi", Icon: "kimi", Kind: KindVendor, Catalog: "moonshotai",
		Chat: "https://api.moonshot.ai/v1", Anthropic: "https://api.moonshot.ai/anthropic",
		Website: "https://platform.moonshot.ai", KeysURL: "https://platform.moonshot.ai/console/api-keys"},
	{ID: "moonshot-cn", Name: "Kimi (China)", Icon: "kimi", Kind: KindVendor, Catalog: "moonshotai",
		Chat: "https://api.moonshot.cn/v1", Anthropic: "https://api.moonshot.cn/anthropic",
		Website: "https://platform.moonshot.cn", KeysURL: "https://platform.moonshot.cn/console/api-keys"},
	// a Kimi Code membership's own endpoints (k3, kimi-for-coding …): Kimi
	// lets members use them from third-party tools, keyed at its console
	{ID: "kimi-code", Name: "Kimi Code", Icon: "kimi", Kind: KindVendor, Catalog: "kimi-code-plan-global",
		Chat: "https://api.kimi.ai/coding/v1", Anthropic: "https://api.kimi.ai/coding",
		Note:    "Membership",
		Website: "https://www.kimi.com/code", KeysURL: "https://www.kimi.com/code/console"},
	{ID: "kimi-code-cn", Name: "Kimi Code (China)", Icon: "kimi", Kind: KindVendor, Catalog: "kimi-code-plan-cn",
		Chat: "https://api.kimi.com/coding/v1", Anthropic: "https://api.kimi.com/coding",
		Note:    "Membership",
		Website: "https://www.kimi.com/code", KeysURL: "https://www.kimi.com/code/console"},
	{ID: "zhipu", Name: "Zhipu GLM", Icon: "zhipu-color", Kind: KindVendor, Catalog: "zhipuai",
		Chat: "https://open.bigmodel.cn/api/paas/v4", Anthropic: "https://open.bigmodel.cn/api/anthropic",
		Website: "https://open.bigmodel.cn", KeysURL: "https://open.bigmodel.cn/usercenter/proj-mgmt/apikeys",
		// a GLM Coding Plan is served at its own OpenAI endpoint: a plan's key
		// sent to the pay-as-you-go one is told it has no balance
		RegionLabel: "Plan", Regions: []Region{
			{ID: "api", Name: "Pay as you go", Chat: "https://open.bigmodel.cn/api/paas/v4", Anthropic: "https://open.bigmodel.cn/api/anthropic"},
			{ID: "coding", Name: "Coding Plan", Chat: "https://open.bigmodel.cn/api/coding/paas/v4", Anthropic: "https://open.bigmodel.cn/api/anthropic"},
		}},
	{ID: "zai", Name: "Z.ai", Icon: "zai", Kind: KindVendor, Catalog: "zhipuai",
		Chat: "https://api.z.ai/api/paas/v4", Anthropic: "https://api.z.ai/api/anthropic",
		Website: "https://z.ai", KeysURL: "https://z.ai/manage-apikey/apikey-list",
		RegionLabel: "Plan", Regions: []Region{
			{ID: "api", Name: "Pay as you go", Chat: "https://api.z.ai/api/paas/v4", Anthropic: "https://api.z.ai/api/anthropic"},
			{ID: "coding", Name: "Coding Plan", Chat: "https://api.z.ai/api/coding/paas/v4", Anthropic: "https://api.z.ai/api/anthropic"},
		}},
	{ID: "minimax", Name: "MiniMax", Icon: "minimax-color", Kind: KindVendor, Catalog: "minimax",
		Chat: "https://api.minimax.io/v1", Anthropic: "https://api.minimax.io/anthropic",
		Website: "https://platform.minimax.io", KeysURL: "https://platform.minimax.io/user-center/basic-information/interface-key"},
	{ID: "minimax-cn", Name: "MiniMax (China)", Icon: "minimax-color", Kind: KindVendor, Catalog: "minimax",
		Chat: "https://api.minimaxi.com/v1", Anthropic: "https://api.minimaxi.com/anthropic",
		Website: "https://platform.minimaxi.com", KeysURL: "https://platform.minimaxi.com/user-center/basic-information/interface-key"},
	{ID: "qwen", Name: "Qwen", Icon: "qwen-color", Kind: KindVendor, Catalog: "alibaba",
		Chat: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", Anthropic: "https://dashscope-intl.aliyuncs.com/apps/anthropic",
		Note:    "DashScope · intl",
		Website: "https://modelstudio.console.alibabacloud.com", KeysURL: "https://modelstudio.console.alibabacloud.com/?tab=playground#/api-key"},
	{ID: "qwen-cn", Name: "Qwen (China)", Icon: "qwen-color", Kind: KindVendor, Catalog: "alibaba",
		Chat: "https://dashscope.aliyuncs.com/compatible-mode/v1", Anthropic: "https://dashscope.aliyuncs.com/apps/anthropic",
		Note:    "DashScope · China",
		Website: "https://bailian.console.aliyun.com", KeysURL: "https://bailian.console.aliyun.com/?tab=model#/api-key"},
	{ID: "mistral", Name: "Mistral", Icon: "mistral-color", Kind: KindVendor, Catalog: "mistral",
		Chat:    "https://api.mistral.ai/v1",
		Website: "https://console.mistral.ai", KeysURL: "https://console.mistral.ai/api-keys"},
	{ID: "groq", Name: "Groq", Icon: "groq", Kind: KindVendor, Catalog: "groq",
		Chat: "https://api.groq.com/openai/v1", Responses: "https://api.groq.com/openai/v1",
		Website: "https://console.groq.com", KeysURL: "https://console.groq.com/keys"},
	// Ollama's own hosted models: the local server's API, at ollama.com with a key
	{ID: "ollama-cloud", Name: "Ollama Cloud", Icon: "ollama", Kind: KindVendor, Catalog: "ollama-cloud",
		Chat: "https://ollama.com/v1", Anthropic: "https://ollama.com",
		Note:    "cloud models, with an API key",
		Website: "https://docs.ollama.com/cloud", KeysURL: "https://ollama.com/settings/keys"},

	{ID: "openrouter", Name: "OpenRouter", Icon: "openrouter", Kind: KindRelay, Catalog: "openrouter",
		Chat: "https://openrouter.ai/api/v1", Anthropic: "https://openrouter.ai/api",
		Website: "https://openrouter.ai", KeysURL: "https://openrouter.ai/keys",
		// app attribution, for OpenRouter's rankings and analytics
		HeaderHints: []string{"HTTP-Referer", "X-OpenRouter-Title"}},
	{ID: "opencode-go", Name: "OpenCode Go", Icon: "opencode", Kind: KindRelay, Catalog: "opencode-go",
		Chat: "https://opencode.ai/zen/go/v1", Responses: "https://opencode.ai/zen/go/v1", Anthropic: "https://opencode.ai/zen/go",
		Note:    "open coding models, $10/month",
		Website: "https://opencode.ai/docs/go", KeysURL: "https://opencode.ai/auth"},
	{ID: "opencode-zen", Name: "OpenCode Zen", Icon: "opencode", Kind: KindRelay, Catalog: "opencode",
		Chat: "https://opencode.ai/zen/v1", Responses: "https://opencode.ai/zen/v1", Anthropic: "https://opencode.ai/zen",
		Website: "https://opencode.ai/docs/zen", KeysURL: "https://opencode.ai/auth"},
	{ID: "together", Name: "Together AI", Icon: "together-color", Kind: KindRelay, Catalog: "togetherai",
		Chat:    "https://api.together.xyz/v1",
		Website: "https://api.together.ai", KeysURL: "https://api.together.ai/settings/api-keys"},
	{ID: "fireworks", Name: "Fireworks", Icon: "fireworks-color", Kind: KindRelay, Catalog: "fireworks-ai",
		Chat:    "https://api.fireworks.ai/inference/v1",
		Website: "https://fireworks.ai", KeysURL: "https://app.fireworks.ai/settings/users/api-keys"},
	{ID: "siliconflow", Name: "SiliconFlow", Icon: "siliconcloud-color", Kind: KindRelay, Catalog: "siliconflow",
		Chat:    "https://api.siliconflow.cn/v1",
		Website: "https://cloud.siliconflow.cn", KeysURL: "https://cloud.siliconflow.cn/account/ak"},
	{ID: "aihubmix", Name: "AiHubMix", Icon: "aihubmix-color", Kind: KindRelay,
		Chat: "https://aihubmix.com/v1", Anthropic: "https://aihubmix.com",
		Website: "https://aihubmix.com", KeysURL: "https://console.aihubmix.com/token"},
	{ID: "302ai", Name: "302.AI", Icon: "ai302-color", Kind: KindRelay,
		Chat: "https://api.302.ai/v1", Anthropic: "https://api.302.ai",
		Website: "https://302.ai", KeysURL: "https://302.ai/api-keys/list"},
	{ID: "yylx", Name: "鱼鱼连线", Icon: "yylx", Kind: KindRelay,
		Chat: "https://app.yylx.io/v1", Anthropic: "https://app.yylx.io",
		Website: "https://yylx.io", KeysURL: "https://app.yylx.io/keys",
		Regions: []Region{
			{ID: "auto", Name: "Auto", Chat: "https://app.yylx.io/v1", Anthropic: "https://app.yylx.io"},
			{ID: "global", Name: "Global", Chat: "https://global.yylx.io/v1", Anthropic: "https://global.yylx.io"},
			{ID: "cn", Name: "China Mainland", Chat: "https://cn.yylx.io/v1", Anthropic: "https://cn.yylx.io"},
		}},

	// Jev answers no conversation: it decides which of a routing group's
	// models takes a turn, and how hard it thinks
	{ID: "typesafe", Name: "TypeSafe Jev", Icon: "typesafe", Kind: KindVendor,
		Decide:  "https://api.typesafe.ai/v1",
		Note:    "routes groups · picks model and effort",
		Website: "https://typesafe.ai", KeysURL: "https://console.typesafe.ai/keys"},
	{ID: "ollama", Name: "Ollama", Icon: "ollama", Kind: KindLocal, NoKey: true,
		Chat: "http://localhost:11434/v1", Anthropic: "http://localhost:11434",
		Note: "your local models", Website: "https://ollama.com"},
	{ID: "lmstudio", Name: "LM Studio", Icon: "lmstudio", Kind: KindLocal, NoKey: true,
		Chat: "http://localhost:1234/v1",
		Note: "local server on :1234", Website: "https://lmstudio.ai"},
}

// Presets lists every preset, sponsored ones first within their kind.
func Presets() []PresetDef {
	out := make([]PresetDef, 0, len(presets))
	for _, p := range presets {
		if p.Sponsored {
			out = append(out, p)
		}
	}
	for _, p := range presets {
		if !p.Sponsored {
			out = append(out, p)
		}
	}
	return out
}

// Preset finds a preset by id.
func Preset(id string) *PresetDef {
	for i := range presets {
		if presets[i].ID == id {
			return &presets[i]
		}
	}
	return nil
}

// FromPreset builds a provider from a preset; the caller adds the key.
func FromPreset(id string) (Provider, error) {
	pr := Preset(id)
	if pr == nil {
		return Provider{}, errorf("no preset %q — magpie presets lists them", id)
	}
	return Provider{
		ID: pr.ID, Name: pr.Name, Icon: pr.Icon, Preset: pr.ID,
		Chat: pr.Chat, Responses: pr.Responses, Anthropic: pr.Anthropic, Decide: pr.Decide,
		Catalog: pr.Catalog, Website: pr.Website, KeysURL: pr.KeysURL,
	}, nil
}

// IconForCatalog names the logo of the vendor behind a models.dev
// provider id, or "" when no preset covers it.
func IconForCatalog(catalogID string) string {
	for _, p := range presets {
		if p.Catalog == catalogID {
			return p.Icon
		}
	}
	return ""
}
