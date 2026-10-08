package provider

import (
	"cmp"
	"slices"
)

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
	KeyHint   string   `json:"keyHint,omitempty"`   // the key field's placeholder, when it says more than NoKey's
	Sponsored bool     `json:"sponsored,omitempty"` // shown first, with a tag
	Note      string   `json:"note,omitempty"`      // one line under the name
	Short     string   `json:"short,omitempty"`     // the add sheet's name for it, when Name is long
	Regions   []Region `json:"regions,omitempty"`   // base-URL choices (a relay's regional endpoints, a vendor's plans)
	// RegionLabel names what the Regions choose between, "Region" if unset.
	RegionLabel string `json:"regionLabel,omitempty"`
	// HeaderHints name optional request headers the vendor documents, which
	// the editor offers to add; their values are the user's to fill in.
	HeaderHints []string `json:"headerHints,omitempty"`
	// Only, for a plan behind a key that also buys much else, is what its
	// models' ids start with: the rest of the vendor's list is left out.
	// Models are the plan's, for when the list has none of them.
	Only   string   `json:"only,omitempty"`
	Models []string `json:"models,omitempty"`
	// NoList: the vendor has no list of models to ask for (Bedrock's
	// runtime serves no /models), so Models are its list
	NoList bool `json:"noList,omitempty"`
	// Endpoint, for a vendor reached at the user's own resource (Azure
	// OpenAI), is an example of its address: the preset gives no URL, and
	// the editor asks for the one the user's resource is at, with
	// EndpointHint under it.
	Endpoint     string `json:"endpoint,omitempty"`
	EndpointHint string `json:"endpointHint,omitempty"`
	// EndpointNeeded is what the editor says when no endpoint was given.
	EndpointNeeded string `json:"endpointNeeded,omitempty"`
	// Hosts: a vendor serving other makers' models as well as its own
	// (Groq, Ollama Cloud, Azure), whose list is no maker's word on theirs
	Hosts bool `json:"-"`
}

// Region is one base-URL option of a preset that offers several. The first
// is the default; picking another in the editor swaps the endpoints.
type Region struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Chat      string `json:"chat,omitempty"`
	Responses string `json:"responses,omitempty"`
	Anthropic string `json:"anthropic,omitempty"`
	// Lists is set on a region that serves a model list although the
	// preset as a whole has none to ask for (NoList): a provider at its
	// endpoints is asked for it. KeysURL, when the region's keys are made
	// on another page than the preset's, is where its Get-a-key link goes.
	Lists   bool   `json:"lists,omitempty"`
	KeysURL string `json:"keysUrl,omitempty"`
	// Website, when the region's docs are another page than the preset's,
	// is where the editor's link goes.
	Website string `json:"website,omitempty"`
	// Catalog, for a region whose models another models.dev entry names
	// than the preset's (Tencent Cloud's pay as you go, beside its plan's
	// none), is that entry's id.
	Catalog string `json:"catalog,omitempty"`
	// Models are a plan's, for a region that is one beside pay as you go
	// (PresetDef.Models, for the region alone).
	Models []string `json:"models,omitempty"`
	// Decide is the region's decision API, for a preset that routes groups
	Decide string `json:"decide,omitempty"`
}

// WorkspaceID stands in a decision API's address for the user's Bailian
// workspace id (#647), which the editor and `workspace=` fill in.
const WorkspaceID = "{WorkspaceId}"

// bailianWorkspace is Bailian's System One root in a workspace of region.
func bailianWorkspace(region string) string {
	return "https://" + WorkspaceID + "." + region + ".maas.aliyuncs.com/compatible-mode/v1"
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
		// sent to the pay-as-you-go one is told it has no balance. The plan
		// serves the Responses API at /api/v1 as well: its tool pages give
		// it for Codex, wire_api = "responses" (#306)
		RegionLabel: "Plan", Regions: []Region{
			{ID: "api", Name: "Pay as you go", Chat: "https://open.bigmodel.cn/api/paas/v4", Anthropic: "https://open.bigmodel.cn/api/anthropic"},
			{ID: "coding", Name: "Coding Plan", Chat: "https://open.bigmodel.cn/api/coding/paas/v4", Responses: "https://open.bigmodel.cn/api/v1", Anthropic: "https://open.bigmodel.cn/api/anthropic"},
		}},
	{ID: "zai", Name: "Z.ai", Icon: "zai", Kind: KindVendor, Catalog: "zhipuai",
		Chat: "https://api.z.ai/api/paas/v4", Anthropic: "https://api.z.ai/api/anthropic",
		Website: "https://z.ai", KeysURL: "https://z.ai/manage-apikey/apikey-list",
		RegionLabel: "Plan", Regions: []Region{
			{ID: "api", Name: "Pay as you go", Chat: "https://api.z.ai/api/paas/v4", Anthropic: "https://api.z.ai/api/anthropic"},
			{ID: "coding", Name: "Coding Plan", Chat: "https://api.z.ai/api/coding/paas/v4", Responses: "https://api.z.ai/api/v1", Anthropic: "https://api.z.ai/api/anthropic"},
		}},
	{ID: "minimax", Name: "MiniMax", Icon: "minimax-color", Kind: KindVendor, Catalog: "minimax",
		Chat: "https://api.minimax.io/v1", Anthropic: "https://api.minimax.io/anthropic",
		Website: "https://platform.minimax.io", KeysURL: "https://platform.minimax.io/user-center/basic-information/interface-key"},
	{ID: "minimax-cn", Name: "MiniMax (China)", Icon: "minimax-color", Kind: KindVendor, Catalog: "minimax",
		Chat: "https://api.minimaxi.com/v1", Anthropic: "https://api.minimaxi.com/anthropic",
		Website: "https://platform.minimaxi.com", KeysURL: "https://platform.minimaxi.com/user-center/basic-information/interface-key"},
	// a Step Plan is served at its own endpoints (step_plan/…), its key
	// refused at the pay-as-you-go ones
	{ID: "stepfun", Name: "StepFun", Icon: "stepfun-color", Kind: KindVendor, Catalog: "stepfun-ai-step-plan, stepfun-ai",
		Chat: "https://api.stepfun.ai/step_plan/v1", Anthropic: "https://api.stepfun.ai/step_plan",
		Website: "https://platform.stepfun.ai", KeysURL: "https://platform.stepfun.ai/interface-key",
		RegionLabel: "Plan", Regions: []Region{
			{ID: "plan", Name: "Step Plan", Chat: "https://api.stepfun.ai/step_plan/v1", Anthropic: "https://api.stepfun.ai/step_plan"},
			{ID: "api", Name: "Pay as you go", Chat: "https://api.stepfun.ai/v1", Anthropic: "https://api.stepfun.ai"},
		}},
	{ID: "stepfun-cn", Name: "StepFun (China)", Icon: "stepfun-color", Kind: KindVendor, Catalog: "stepfun-step-plan, stepfun",
		Chat: "https://api.stepfun.com/step_plan/v1", Anthropic: "https://api.stepfun.com/step_plan",
		Website: "https://platform.stepfun.com", KeysURL: "https://platform.stepfun.com/interface-key",
		RegionLabel: "Plan", Regions: []Region{
			{ID: "plan", Name: "Step Plan", Chat: "https://api.stepfun.com/step_plan/v1", Anthropic: "https://api.stepfun.com/step_plan"},
			{ID: "api", Name: "Pay as you go", Chat: "https://api.stepfun.com/v1", Anthropic: "https://api.stepfun.com"},
		}},
	// Xiaomi MiMo (#174): a Token Plan's tp- key is spent at its region's
	// own host (China, Singapore, Europe), pay as you go at api.xiaomimimo.com;
	// each serves chat completions, Responses and Anthropic messages
	{ID: "xiaomi", Name: "Xiaomi MiMo", Icon: "mimocode", Kind: KindVendor, Catalog: "xiaomi-token-plan-cn, xiaomi",
		Chat: "https://token-plan-cn.xiaomimimo.com/v1", Responses: "https://token-plan-cn.xiaomimimo.com/v1", Anthropic: "https://token-plan-cn.xiaomimimo.com/anthropic",
		Note:    "Token Plan · pay as you go",
		Website: "https://platform.xiaomimimo.com", KeysURL: "https://platform.xiaomimimo.com/token-plan",
		Regions: []Region{
			{ID: "plan-cn", Name: "Plan · China", Chat: "https://token-plan-cn.xiaomimimo.com/v1", Responses: "https://token-plan-cn.xiaomimimo.com/v1", Anthropic: "https://token-plan-cn.xiaomimimo.com/anthropic"},
			{ID: "plan-sgp", Name: "Plan · Singapore", Chat: "https://token-plan-sgp.xiaomimimo.com/v1", Responses: "https://token-plan-sgp.xiaomimimo.com/v1", Anthropic: "https://token-plan-sgp.xiaomimimo.com/anthropic"},
			{ID: "plan-ams", Name: "Plan · Europe", Chat: "https://token-plan-ams.xiaomimimo.com/v1", Responses: "https://token-plan-ams.xiaomimimo.com/v1", Anthropic: "https://token-plan-ams.xiaomimimo.com/anthropic"},
			{ID: "api", Name: "Pay as you go", Chat: "https://api.xiaomimimo.com/v1", Responses: "https://api.xiaomimimo.com/v1", Anthropic: "https://api.xiaomimimo.com/anthropic"},
		}},
	// Baidu Qianfan: a personal and an enterprise Token Plan, each at its
	// own endpoints under qianfan.baidubce.com on a key of its own; pay as
	// you go is the v2 API at the host's root, which serves its model list
	// at /v2/models — the plans serve none (their /models is 404), so the
	// models given are the plans' union as each documents them, with
	// deepseek-v4-flash, deepseek-v3.2 and glm-5 the enterprise plan's
	// alone; qianfan-code-latest is whichever the console has picked.
	{ID: "baidu-qianfan", Name: "Baidu Qianfan", Icon: "baiducloud-color", Kind: KindVendor,
		Chat: "https://qianfan.baidubce.com/v2/tokenplan/personal", Responses: "https://qianfan.baidubce.com/v2/tokenplan/personal", Anthropic: "https://qianfan.baidubce.com/anthropic/tokenplan/personal",
		Note:    "Token Plan · pay as you go",
		Website: "https://cloud.baidu.com/doc/qianfan/s/Dmrabu8b6", KeysURL: "https://console.bce.baidu.com/qianfan/resource/token-plan",
		RegionLabel: "Plan", Regions: []Region{
			{ID: "personal", Name: "Token Plan Personal", Chat: "https://qianfan.baidubce.com/v2/tokenplan/personal", Responses: "https://qianfan.baidubce.com/v2/tokenplan/personal", Anthropic: "https://qianfan.baidubce.com/anthropic/tokenplan/personal"},
			{ID: "team", Name: "Token Plan Enterprise", Chat: "https://qianfan.baidubce.com/v2/tokenplan/team", Responses: "https://qianfan.baidubce.com/v2/tokenplan/team", Anthropic: "https://qianfan.baidubce.com/anthropic/tokenplan/team"},
			{ID: "api", Name: "Pay as you go", Chat: "https://qianfan.baidubce.com/v2", Responses: "https://qianfan.baidubce.com/v2", Anthropic: "https://qianfan.baidubce.com/anthropic",
				Lists: true, KeysURL: "https://console.bce.baidu.com/iam/#/iam/apikey/list"},
		},
		NoList: true,
		Models: []string{"qianfan-code-latest", "deepseek-v4.1-flash", "deepseek-v4-pro", "deepseek-v4-pro-0813",
			"deepseek-v4-flash", "deepseek-v4-flash-0731", "deepseek-v3.2", "glm-5.3", "glm-5.3-flash", "glm-5.2", "glm-5.1", "glm-5"}},
	// Tencent Cloud (Jorben on Discord: one Tencent Cloud, its plan and
	// pay as you go picked inside it, as Xiaomi MiMo's are), once three
	// presets, whose ids still name it, each at its region (presetAliases):
	//
	// Its Token Plan (TokenHub's subscription): a general and a Hy plan on
	// one sk-tp- key, served at their own endpoints under /plan, chat
	// completions and Anthropic messages only (its Codex page asks for
	// wire_api "chat"). models.dev lists just its Hy models, so the plan's
	// are given here, and none of its catalogs names them.
	//
	// TokenHub pay as you go: an API key of TokenHub's own (not the plan's
	// sk-tp-), at tokenhub.tencentmaas.com in Guangzhou and
	// tokenhub-intl.tencentmaas.com in Singapore. Each serves chat
	// completions and Responses under /v1 (Responses converted from chat
	// on its side for the models that speak chat alone) and Anthropic
	// messages at /v1/messages, with its list at /v1/models. It serves Hy
	// and other makers' models (DeepSeek, GLM, Kimi, MiniMax, Qwen …):
	// Hosts, and models.dev's tencent-tokenhub for their names and prices.
	{ID: "tencent-cloud", Name: "Tencent Cloud", Icon: "tencentcloud-color", Kind: KindVendor, Hosts: true,
		Chat: "https://api.lkeap.cloud.tencent.com/plan/v3", Anthropic: "https://api.lkeap.cloud.tencent.com/plan/anthropic",
		Note:    "Token Plan · pay as you go",
		Website: "https://cloud.tencent.com/document/product/1823/130060", KeysURL: "https://console.cloud.tencent.com/tokenhub/tokenplan",
		Regions: []Region{
			{ID: "plan", Name: "Plan · China", Chat: "https://api.lkeap.cloud.tencent.com/plan/v3", Anthropic: "https://api.lkeap.cloud.tencent.com/plan/anthropic",
				Website: "https://cloud.tencent.com/document/product/1823/130060", KeysURL: "https://console.cloud.tencent.com/tokenhub/tokenplan",
				Models: []string{"tc-code-latest", "glm-5.3", "glm-5.3-flash", "glm-5.2", "glm-5.1", "glm-5", "kimi-k3", "kimi-k2.7-code",
					"deepseek-v4-pro-202606", "deepseek-v4-flash-202605", "minimax-m3", "minimax-m2.7", "hy4-preview", "hy3"}},
			{ID: "cn", Name: "Pay as you go · China", Chat: "https://tokenhub.tencentmaas.com/v1", Responses: "https://tokenhub.tencentmaas.com/v1", Anthropic: "https://tokenhub.tencentmaas.com",
				Website: "https://cloud.tencent.com/document/product/1823/130078", KeysURL: "https://console.cloud.tencent.com/tokenhub/apikey", Catalog: "tencent-tokenhub"},
			{ID: "intl", Name: "Pay as you go · Global", Chat: "https://tokenhub-intl.tencentmaas.com/v1", Responses: "https://tokenhub-intl.tencentmaas.com/v1", Anthropic: "https://tokenhub-intl.tencentmaas.com",
				Website: "https://www.tencentcloud.com/document/product/1300/78939", KeysURL: "https://console.tencentcloud.com/tokenhub/apikey", Catalog: "tencent-tokenhub"},
		}},
	// Huawei Cloud MaaS's Token Plan: personal accounts in 西南-贵阳一, its
	// quota spent only at the plan's own endpoints under /plan (v2 for chat
	// completions, anthropic for messages; its Claude Code, OpenClaw, Cherry
	// Studio and CodeArts pages), a MaaS key to either. No Responses.
	{ID: "huaweicloud", Name: "Huawei Cloud MaaS", Short: "Huawei Cloud", Icon: "huaweicloud-color", Kind: KindVendor,
		Chat: "https://api.modelarts-maas.com/plan/v2", Anthropic: "https://api.modelarts-maas.com/plan/anthropic",
		Note:    "Token Plan · 西南-贵阳一",
		Website: "https://support.huaweicloud.com/Token-plan-maas/tokenplan-maas-0001.html", KeysURL: "https://console.huaweicloud.com/modelarts/?#/model-studio/authmanage",
		RegionLabel: "Plan", Regions: []Region{
			{ID: "plan", Name: "Token Plan", Chat: "https://api.modelarts-maas.com/plan/v2", Anthropic: "https://api.modelarts-maas.com/plan/anthropic"},
			{ID: "api", Name: "Pay as you go", Chat: "https://api.modelarts-maas.com/openai/v1", Anthropic: "https://api.modelarts-maas.com/anthropic"},
		},
		Models: []string{"glm-5.3", "glm-5.1", "kimi-k2.6", "deepseek-v4.1-flash", "deepseek-v4-flash"}},
	// Volcengine Ark (火山方舟): a Coding Plan's quota is spent only at
	// /api/coding/v3 (chat completions and Responses; its Codex page sets
	// wire_api "responses") and /api/coding (messages), an Agent Plan's only
	// at /api/plan/v3 (chat completions and Responses, its Hermes page) and
	// /api/plan, with a key of its own; pay-as-you-go is /api/v3, which
	// both plans' pages warn bills apart. Their versioned paths are used as
	// written: no /v1 goes on them. Coding's /models lists Ark's general
	// catalog, not the plan's models; Agent's returns 404. Only pay-as-you-go's
	// /models is asked.
	{ID: "volcengine", Name: "Volcengine Ark", Icon: "volcengine-color", Kind: KindVendor,
		Chat: "https://ark.cn-beijing.volces.com/api/coding/v3", Responses: "https://ark.cn-beijing.volces.com/api/coding/v3", Anthropic: "https://ark.cn-beijing.volces.com/api/coding",
		Note:    "火山方舟 · Coding / Agent Plan",
		Website: "https://www.volcengine.com/docs/82379/1925114", KeysURL: "https://ark.volcengine.com/region:cn-beijing/apikey",
		RegionLabel: "Plan", Regions: []Region{
			{ID: "coding", Name: "Coding Plan", Chat: "https://ark.cn-beijing.volces.com/api/coding/v3", Responses: "https://ark.cn-beijing.volces.com/api/coding/v3", Anthropic: "https://ark.cn-beijing.volces.com/api/coding"},
			{ID: "agent", Name: "Agent Plan", Chat: "https://ark.cn-beijing.volces.com/api/plan/v3", Responses: "https://ark.cn-beijing.volces.com/api/plan/v3", Anthropic: "https://ark.cn-beijing.volces.com/api/plan"},
			{ID: "api", Name: "Pay as you go", Chat: "https://ark.cn-beijing.volces.com/api/v3", Responses: "https://ark.cn-beijing.volces.com/api/v3", Lists: true},
		},
		NoList: true,
		// the plans' model names, lowercase as their quick-start pages list
		// them; ark-code-latest is whichever the console has picked
		Models: []string{"ark-code-latest", "doubao-seed-evolving", "doubao-seed-2.1-pro", "doubao-seed-2.1-lite", "doubao-seed-2.0-mini",
			"minimax-m3", "glm-5.3", "glm-5.3-flash", "deepseek-v4.1-flash", "deepseek-v4-flash", "deepseek-v4-pro",
			"kimi-k2.7-code", "kimi-k2.8-preview", "kimi-k3"}},
	{ID: "qwen", Name: "Qwen", Icon: "qwen-color", Kind: KindVendor, Catalog: "alibaba",
		Chat: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", Anthropic: "https://dashscope-intl.aliyuncs.com/apps/anthropic",
		Note:    "DashScope · intl",
		Website: "https://modelstudio.console.alibabacloud.com", KeysURL: "https://modelstudio.console.alibabacloud.com/?tab=playground#/api-key"},
	{ID: "qwen-cn", Name: "Qwen (China)", Icon: "qwen-color", Kind: KindVendor, Catalog: "alibaba",
		Chat: "https://dashscope.aliyuncs.com/compatible-mode/v1", Anthropic: "https://dashscope.aliyuncs.com/apps/anthropic",
		Note:    "DashScope · China",
		Website: "https://bailian.console.aliyun.com", KeysURL: "https://bailian.console.aliyun.com/?tab=model#/api-key"},
	// Alibaba Cloud Bailian's Token Plan (personal and team), a subscription
	// on a key of its own (sk-sp-) that only its own host takes, serving
	// chat completions and Anthropic messages. The models given are the
	// plan's text models as its overview lists them, for when it gives no
	// list.
	{ID: "qwen-token-plan", Name: "Qwen Token Plan", Short: "Qwen Plan", Icon: "qwen-color", Kind: KindVendor,
		Chat: "https://token-plan.maas.qianwenaiapi.com/compatible-mode/v1", Anthropic: "https://token-plan.maas.qianwenaiapi.com/apps/anthropic",
		Note:    "Bailian · subscription",
		Website: "https://help.aliyun.com/zh/model-studio/token-plan-overview", KeysURL: "https://bailian.console.aliyun.com/cn-beijing/subscription/token-plan/personal",
		Models: []string{"auto", "qwen3.8-max", "qwen3.8-flash", "qwen3.7-max", "qwen3.7-plus", "qwen3.6-flash",
			"deepseek-v4.1-flash", "deepseek-v4-pro", "deepseek-v4-pro-0813", "deepseek-v4-flash-0731", "glm-5.3", "glm-5.2"}},
	{ID: "mistral", Name: "Mistral", Icon: "mistral-color", Kind: KindVendor, Catalog: "mistral",
		Chat:    "https://api.mistral.ai/v1",
		Website: "https://console.mistral.ai", KeysURL: "https://console.mistral.ai/api-keys"},
	{ID: "groq", Name: "Groq", Icon: "groq", Kind: KindVendor, Catalog: "groq", Hosts: true,
		Chat: "https://api.groq.com/openai/v1", Responses: "https://api.groq.com/openai/v1",
		Website: "https://console.groq.com", KeysURL: "https://console.groq.com/keys"},
	// Amazon Bedrock (#176), with a Bedrock API key (AWS_BEARER_TOKEN_BEDROCK),
	// no SigV4: its runtime serves Claude on Anthropic's messages at
	// /anthropic/v1/messages and the other models on chat completions at
	// /openai/v1, and OpenAI's GPT models (not gpt-oss) on the Responses
	// API there too, each in the region picked. It has no list to ask, so the
	// models are given: Claude and GPT-6 as their global. inference
	// profiles, which every commercial region routes (GPT-6 has no in-region
	// id there), then the in-region ids of the others, which not every
	// region serves (gpt-oss isn't in ap-southeast-1); one kept in a
	// geography (us., eu., apac., jp., au.) is typed in by hand.
	{ID: "bedrock", Name: "Amazon Bedrock", Icon: "bedrock-color", Kind: KindVendor,
		Chat: bedrockChat("us-east-1"), Responses: bedrockChat("us-east-1"), Anthropic: bedrockAnthropic("us-east-1"),
		Note:    "Bedrock API key",
		Website: "https://aws.amazon.com/bedrock/", KeysURL: "https://console.aws.amazon.com/bedrock/home#/api-keys",
		Regions: bedrockRegions("us-east-1", "us-east-2", "us-west-2", "eu-central-1", "eu-west-1", "eu-west-3",
			"ap-northeast-1", "ap-southeast-1", "ap-southeast-2", "ap-south-1"),
		NoList: true,
		Models: []string{"global.anthropic.claude-opus-5-5", "global.anthropic.claude-sonnet-5-5", "global.anthropic.claude-sonnet-5", "global.anthropic.claude-opus-5",
			"global.anthropic.claude-fable-5-1", "global.anthropic.claude-opus-4-8", "global.anthropic.claude-opus-4-7",
			"global.anthropic.claude-haiku-4-5-20251001-v1:0",
			"global.openai.gpt-6-astra", "global.openai.gpt-6-sol", "global.openai.gpt-6-luna",
			"openai.gpt-oss-120b-1:0", "openai.gpt-oss-20b-1:0", "qwen.qwen3-coder-480b-a35b-v1:0", "deepseek.v3.2",
			"moonshotai.kimi-k2.5", "zai.glm-5", "minimax.minimax-m2.5"}},
	// Azure OpenAI, at the user's own resource (azure.go): its v1 API under
	// /openai/v1 for chat completions and Responses, the key in api-key,
	// its deployments' names as the model ids. It hosts other makers'
	// models (Kimi, DeepSeek, Grok …), which models.dev lists under it
	// with no levels, so its list is no maker's word on theirs
	{ID: AzurePreset, Name: "Azure OpenAI", Icon: "azure-color", Kind: KindVendor, Catalog: "azure, openai", Hosts: true,
		Note:         "your resource's endpoint and key",
		Endpoint:     "https://<resource>.openai.azure.com",
		EndpointHint: "Your resource's endpoint, from Keys and Endpoint in the Azure portal. magpie asks its v1 API; the model ids are your deployments' names.",
		Website:      "https://ai.azure.com", KeysURL: "https://portal.azure.com/#view/Microsoft_Azure_ProjectOxford/CognitiveServicesHub/~/OpenAI"},
	// Ollama's own hosted models: the local server's API, at ollama.com with a key
	{ID: "ollama-cloud", Name: "Ollama Cloud", Icon: "ollama", Kind: KindVendor, Catalog: "ollama-cloud", Hosts: true,
		Chat: "https://ollama.com/v1", Anthropic: "https://ollama.com",
		Note:    "cloud models, with an API key",
		Website: "https://docs.ollama.com/cloud", KeysURL: "https://ollama.com/settings/keys"},

	{ID: "openrouter", Name: "OpenRouter", Icon: "openrouter", Kind: KindRelay, Catalog: "openrouter",
		Chat: "https://openrouter.ai/api/v1", Anthropic: "https://openrouter.ai/api",
		// its decision models (liquid/d1, cloudflare/clef, Jev …), listed
		// apart from its chat models, answer System One at /systemone, so
		// a routing group can be classified by one with the same key
		Decide:  "https://openrouter.ai/api/v1",
		Website: "https://openrouter.ai", KeysURL: "https://openrouter.ai/keys",
		// app attribution, for OpenRouter's rankings and analytics
		HeaderHints: []string{"HTTP-Referer", "X-OpenRouter-Title"}},
	{ID: "opencode-go", Name: "OpenCode Go", Icon: "opencode", Kind: KindRelay, Catalog: "opencode-go",
		Chat: "https://opencode.ai/zen/go/v1", Responses: "https://opencode.ai/zen/go/v1", Anthropic: "https://opencode.ai/zen/go",
		Note:    "open coding models, $10/month",
		Website: "https://opencode.ai/docs/go", KeysURL: "https://opencode.ai/auth"},
	// Cline's plan for open models, at the Cline API with a Cline key: the
	// API lists only its paid models, not the plan's; the plan's and
	// Cline's free models come from the feed Cline's own clients list
	// them from (cline.go)
	{ID: "clinepass", Name: "ClinePass", Icon: "cline", Kind: KindRelay,
		Chat:    "https://api.cline.bot/api/v1",
		Note:    "open coding models, $9.99/month",
		Website: "https://docs.cline.bot/getting-started/clinepass", KeysURL: "https://app.cline.bot",
		Only: "cline-pass/",
		Models: []string{"cline-pass/glm-5.3", "cline-pass/glm-5.3-flash", "cline-pass/kimi-k3", "cline-pass/deepseek-v4-pro",
			"cline-pass/deepseek-v4.1-flash", "cline-pass/mimo-v2.5", "cline-pass/mimo-v2.5-pro", "cline-pass/minimax-m3",
			"cline-pass/muse-spark-1.3-contributor", "cline-pass/qwen3.8-max", "cline-pass/qwen3.7-max", "cline-pass/qwen3.7-plus"}},
	{ID: "opencode-zen", Name: "OpenCode Zen", Icon: "opencode", Kind: KindRelay, Catalog: "opencode", NoKey: true,
		KeyHint: "optional: free models need no key",
		Chat:    "https://opencode.ai/zen/v1", Responses: "https://opencode.ai/zen/v1", Anthropic: "https://opencode.ai/zen",
		// TypeSafe's Jev (jev-1.13, jev-1.13-free) is no chat model there:
		// Zen serves it on System One at /zen/v1/systemone alone, as its
		// docs' model table says (packages/web/src/content/docs/zen.mdx),
		// so it routes groups like TypeSafe's own (#609's mixed provider)
		Decide: "https://opencode.ai/zen/v1",
		// its free models (-free, big-pickle) are served to OpenCode
		// alone, which magpie asks them as (OpenCodeFree)
		Website: "https://opencode.ai/docs/zen", KeysURL: "https://opencode.ai/auth"},
	// Kilo Code's gateway, at the OpenRouter-style API its own clients use
	// (kilo.go): its free models (isFree, ":free") are served with no key,
	// as Kilo serves them signed out; a Kilo key reaches the rest
	{ID: "kilo", Name: "Kilo Gateway", Icon: "kilo", Kind: KindRelay, NoKey: true,
		Chat:    "https://api.kilo.ai/api/openrouter",
		Note:    "free models with no key",
		KeyHint: "optional: free models need no key",
		Website: "https://kilo.ai/docs/gateway", KeysURL: "https://app.kilo.ai"},
	// Command Code's Provider API: its Claude models on /messages alone, the
	// rest on chat and Responses, as its model list says (#93)
	{ID: "commandcode", Name: "Command Code", Icon: "commandcode", Kind: KindRelay,
		Chat: "https://api.commandcode.ai/provider/v1", Responses: "https://api.commandcode.ai/provider/v1", Anthropic: "https://api.commandcode.ai/provider",
		Website: "https://commandcode.ai/docs/provider", KeysURL: "https://commandcode.ai/settings/keys"},
	{ID: "together", Name: "Together AI", Icon: "together-color", Kind: KindRelay, Catalog: "togetherai",
		Chat:    "https://api.together.xyz/v1",
		Website: "https://api.together.ai", KeysURL: "https://api.together.ai/settings/api-keys"},
	{ID: "fireworks", Name: "Fireworks", Icon: "fireworks-color", Kind: KindRelay, Catalog: "fireworks-ai",
		Chat:    "https://api.fireworks.ai/inference/v1",
		Website: "https://fireworks.ai", KeysURL: "https://app.fireworks.ai/settings/users/api-keys"},
	{ID: "siliconflow", Name: "SiliconFlow", Icon: "siliconcloud-color", Kind: KindRelay, Catalog: "siliconflow",
		Chat:    "https://api.siliconflow.cn/v1",
		Website: "https://cloud.siliconflow.cn", KeysURL: "https://cloud.siliconflow.cn/account/ak"},
	// NVIDIA's hosted NIM endpoints (#197): chat completions only; its
	// /v1/responses answers for a few models alone, 404 for the rest
	{ID: "nvidia", Name: "NVIDIA NIM", Icon: "nvidia-color", Kind: KindRelay, Catalog: "nvidia",
		Chat:    "https://integrate.api.nvidia.com/v1",
		Website: "https://build.nvidia.com", KeysURL: "https://build.nvidia.com/settings/api-keys"},
	// 魔搭's API-Inference (#197), a ModelScope access token as the key; not
	// DashScope, the Qwen presets' API
	{ID: "modelscope", Name: "ModelScope", Icon: "modelscope-color", Kind: KindRelay, Catalog: "modelscope",
		Chat: "https://api-inference.modelscope.cn/v1", Responses: "https://api-inference.modelscope.cn/v1",
		Note:    "魔搭 · API-Inference",
		Website: "https://modelscope.cn/docs/model-service/API-Inference/intro", KeysURL: "https://modelscope.cn/my/myaccesstoken"},
	{ID: "aihubmix", Name: "AiHubMix", Icon: "aihubmix-color", Kind: KindRelay,
		Chat: "https://aihubmix.com/v1", Anthropic: "https://aihubmix.com",
		Website: "https://aihubmix.com", KeysURL: "https://console.aihubmix.com/token"},
	// one key for every vendor's models: Chat on its converter, which
	// takes any of them; GPT on its own Responses API and Claude on its
	// own Messages API, each of which serves its family alone (the
	// model list's type_target says which is which)
	{ID: "pipellm", Name: "PipeLLM", Icon: "pipellm-color", Kind: KindRelay,
		Chat: "https://api.pipellm.ai/openai/v1", Responses: "https://api.pipellm.ai/v1", Anthropic: "https://api.pipellm.ai",
		Website: "https://www.pipellm.ai", KeysURL: "https://console.pipellm.ai"},
	{ID: "302ai", Name: "302.AI", Icon: "ai302-color", Kind: KindRelay,
		Chat: "https://api.302.ai/v1", Anthropic: "https://api.302.ai",
		Website: "https://302.ai", KeysURL: "https://302.ai/api-keys/list"},
	{ID: "cherryin", Name: "CherryIN", Icon: "cherryin-color", Kind: KindRelay,
		Chat: "https://open.cherryin.ai/v1", Responses: "https://open.cherryin.ai/v1", Anthropic: "https://open.cherryin.ai",
		Website: "https://open.cherryin.ai", KeysURL: "https://open.cherryin.ai/console/token"},
	{ID: "yylx", Name: "鱼鱼连线", Icon: "yylx", Kind: KindRelay,
		Chat: "https://app.yylx.io/v1", Anthropic: "https://app.yylx.io",
		Website: "https://yylx.io", KeysURL: "https://app.yylx.io/keys",
		Regions: []Region{
			{ID: "auto", Name: "Auto", Chat: "https://app.yylx.io/v1", Anthropic: "https://app.yylx.io"},
			{ID: "global", Name: "Global", Chat: "https://global.yylx.io/v1", Anthropic: "https://global.yylx.io"},
			{ID: "cn", Name: "China Mainland", Chat: "https://cn.yylx.io/v1", Anthropic: "https://cn.yylx.io"},
		}},

	// another computer's magpie, shared on its network (remote_magpie.go):
	// its providers, routing groups and usage stay there, each request
	// goes on in the API the agent spoke
	{ID: RemoteMagpiePreset, Name: "Remote magpie", Icon: "magpie", Kind: KindRelay,
		Note:           "another computer's magpie, shared on its network",
		Endpoint:       "http://192.168.1.20:3425",
		EndpointHint:   "The address and API key the other computer's magpie shows in Settings, under Share on local network. Its models and routing groups are listed here; each request goes on in the API the agent spoke.",
		EndpointNeeded: "The other magpie's address is needed"},

	// Jev answers no conversation: it decides which of a routing group's
	// models takes a turn, and how hard it thinks
	{ID: "typesafe", Name: "TypeSafe Jev", Icon: "typesafe", Kind: KindVendor,
		Decide:  "https://api.typesafe.ai/v1",
		Note:    "routes groups · picks model and effort",
		Website: "https://typesafe.ai", KeysURL: "https://console.typesafe.ai/keys"},
	// Jev as the gateways serve it, to a key made there
	{ID: "vercel-jev", Name: "Jev · Vercel AI Gateway", Icon: "vercel", Kind: KindRelay,
		Decide:  "https://ai-gateway.vercel.sh/typesafe",
		Note:    "routes groups · picks model and effort",
		Website: "https://vercel.com/ai-gateway/models/jev", KeysURL: "https://vercel.com/d?to=%2F%5Bteam%5D%2F%7E%2Fai-gateway%2Fapi-keys"},
	// Workers AI also serves Cloudflare's own decision models, Clef and
	// Clef Flash, on the same key
	{ID: "cloudflare-jev", Name: "Cloudflare Workers AI · Jev, Clef", Icon: "cloudflare-color", Kind: KindRelay,
		Decide:  "https://api.cloudflare.com/client/v4",
		Note:    "routes groups · picks model and effort",
		Website: "https://developers.cloudflare.com/ai/models/typesafe/jev/", KeysURL: "https://dash.cloudflare.com/profile/api-tokens"},
	// Alibaba Cloud Bailian's decision model (#647), on Jev's System One
	// API: at the host of the key's workspace (Beijing or Singapore), or
	// the Token Plan's for its sk-sp- keys
	{ID: "bailian-decision", Name: "Bailian Decision Model", Short: "Bailian Decision", Icon: "qwen-color", Kind: KindVendor,
		Decide:      bailianWorkspace("cn-beijing"),
		Note:        "routes groups · picks model and effort",
		RegionLabel: "Plan", Regions: []Region{
			{ID: "cn-beijing", Name: "Workspace · Beijing", Decide: bailianWorkspace("cn-beijing")},
			{ID: "ap-southeast-1", Name: "Workspace · Singapore", Decide: bailianWorkspace("ap-southeast-1"),
				KeysURL: "https://modelstudio.console.alibabacloud.com/?tab=playground#/api-key"},
			{ID: "token-plan", Name: "Token Plan · Beijing", Decide: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1",
				KeysURL: "https://bailian.console.aliyun.com/cn-beijing/subscription/token-plan/personal"},
		},
		Website: "https://help.aliyun.com/zh/model-studio/decision-model-preview", KeysURL: "https://bailian.console.aliyun.com/?tab=model#/api-key"},
	{ID: "ollama", Name: "Ollama", Icon: "ollama", Kind: KindLocal, NoKey: true,
		Chat: "http://localhost:11434/v1", Anthropic: "http://localhost:11434",
		Note: "your local models", Website: "https://ollama.com"},
	{ID: "lmstudio", Name: "LM Studio", Icon: "lmstudio", Kind: KindLocal, NoKey: true,
		Chat: "http://localhost:1234/v1",
		Note: "local server on :1234", Website: "https://lmstudio.ai"},
	{ID: "omlx", Name: "oMLX", Icon: "omlx", Kind: KindLocal, NoKey: true,
		Chat: "http://localhost:8000/v1", Responses: "http://localhost:8000/v1", Anthropic: "http://localhost:8000",
		Note: "local server on :8000", Website: "https://omlx.ai"},
}

func bedrockChat(region string) string {
	return "https://bedrock-runtime." + region + ".amazonaws.com/openai/v1"
}

// bedrockAnthropic is the base the gateway's /v1/messages goes on:
// …/anthropic/v1/messages is where the runtime serves Anthropic's API.
func bedrockAnthropic(region string) string {
	return "https://bedrock-runtime." + region + ".amazonaws.com/anthropic"
}

// bedrockRegions are the regions picked between, named by their codes, the
// ones the AWS console and a key's region are given in.
func bedrockRegions(ids ...string) []Region {
	out := make([]Region, len(ids))
	for i, id := range ids {
		out[i] = Region{ID: id, Name: id, Chat: bedrockChat(id), Responses: bedrockChat(id), Anthropic: bedrockAnthropic(id)}
	}
	return out
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

// presetAliases are ids presets carried before, each the preset since and
// the region it is (none: the preset's default): qianfan's first day's
// (qianfan-token-plan, v0.1.394), and Tencent Cloud's three presets, its
// Token Plan and TokenHub's China and global pay as you go, made one.
var presetAliases = map[string]struct{ preset, region string }{
	"qianfan-token-plan":  {"baidu-qianfan", ""},
	"tencent-token-plan":  {"tencent-cloud", "plan"},
	"tencent-tokenhub-cn": {"tencent-cloud", "cn"},
	"tencent-tokenhub":    {"tencent-cloud", "intl"},
}

// Preset finds a preset by id, or by an id it carried before
// (presetAliases).
func Preset(id string) *PresetDef {
	if a, ok := presetAliases[id]; ok {
		id = a.preset
	}
	for i := range presets {
		if presets[i].ID == id {
			return &presets[i]
		}
	}
	return nil
}

// FromPreset builds a provider from a preset; the caller adds the key. An
// id the preset carried before builds it at the region that id was.
func FromPreset(id string) (Provider, error) {
	pr := Preset(id)
	if pr == nil {
		return Provider{}, errorf("no preset %q — magpie presets lists them", id)
	}
	p := Provider{
		ID: pr.ID, Name: pr.Name, Icon: pr.Icon, Preset: pr.ID,
		Chat: pr.Chat, Responses: pr.Responses, Anthropic: pr.Anthropic, Decide: pr.Decide,
		Catalog: pr.Catalog, Website: pr.Website, KeysURL: pr.KeysURL,
	}
	if a, ok := presetAliases[id]; ok && a.region != "" {
		for _, r := range pr.Regions {
			if r.ID == a.region {
				p.atRegionOf(pr, r)
			}
		}
	}
	return p, nil
}

// atRegionOf puts the provider at a region of its preset pr: its
// endpoints, and its key page, docs and catalog where it has its own.
func (p *Provider) atRegionOf(pr *PresetDef, r Region) {
	p.Chat, p.Responses, p.Anthropic, p.Decide = r.Chat, r.Responses, r.Anthropic, cmp.Or(r.Decide, pr.Decide)
	p.KeysURL = cmp.Or(r.KeysURL, p.KeysURL)
	p.Website = cmp.Or(r.Website, p.Website)
	p.Catalog = cmp.Or(r.Catalog, pr.Catalog)
}

// IconForCatalog names the logo of the vendor behind a models.dev
// provider id, or "" when no preset covers it.
func IconForCatalog(catalogID string) string {
	for _, p := range presets {
		if p.Catalog == catalogID || slices.ContainsFunc(p.Regions, func(r Region) bool { return r.Catalog == catalogID }) {
			return p.Icon
		}
	}
	return ""
}
