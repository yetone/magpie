// The add sheet's Partners: the services that pay to be listed first in
// magpie's "Add provider" sheet, under a heading that says they sponsor
// magpie. The app fetches this list from /api/partners every six hours and
// keeps it (internal/provider/partners.go), so a change here reaches users
// with `npx wrangler deploy`, no release. At most 12 whose term hasn't
// ended are listed, in this order.
//
// An entry is a preset (internal/provider/presets.go) and a few fields more:
//
//   id         a-z, 0-9 and "-", 2-41 characters; never a built-in preset's
//              id. It is kept in the providers users add from it: don't
//              change it, and don't give it to someone else later.
//   name       the row's name;   short  a shorter one, when name is long
//   chat       OpenAI chat completions base URL (…/v1)
//   responses  OpenAI Responses base URL;   anthropic  Anthropic messages base URL
//              (at least one of the three, or regions)
//   regions    [{id, name, chat, responses, anthropic, keysUrl, website}]: the
//              first is the default
//   keysUrl    where the user makes a key (a referral link is fine)
//   website    the service's home page
//   iconUrl    its logo (png, svg, webp, jpeg; square), fetched and kept by
//              each app
//   notes      the tagline by language: {en, zh, "zh-TW", ja, de}; en stands
//              in for a language without one
//   langs      the app languages it is listed in (["zh"]); none is all
//   from, until  RFC 3339 times bounding when it is listed (the paid term)
//
// Every URL is https. An entry that breaks a rule is left out by the app.
//
// The app gets keysUrl and website (a region's too) as usemagpie.ai/go/…
// links, which count the click and send the browser on (worker.js). The
// providers users add keep those links, so an entry stays here after its
// term ends: /api/partners leaves it out, /go still finds it. Remove one
// only when nobody can still be using it.
export const PARTNERS = [
  {
    id: "kimi",
    name: "Kimi Code",
    regionLabel: "Plan",
    regions: [
      { id: "cn", name: "China", chat: "https://api.kimi.com/coding/v1", anthropic: "https://api.kimi.com/coding", catalog: "kimi-code-plan-cn", keysUrl: "https://www.kimi.com/code?aff=magpie", website: "https://www.kimi.com/code?aff=magpie" },
      { id: "global", name: "Global", chat: "https://api.kimi.ai/coding/v1", anthropic: "https://api.kimi.ai/coding", catalog: "kimi-code-plan-global", keysUrl: "https://www.kimi.ai/code?aff=magpie", website: "https://www.kimi.ai/code?aff=magpie" },
      { id: "api-cn", name: "Pay as you go · China", chat: "https://api.moonshot.cn/v1", anthropic: "https://api.moonshot.cn/anthropic", catalog: "moonshotai", keysUrl: "https://platform.kimi.com?aff=magpie", website: "https://platform.kimi.com?aff=magpie" },
      { id: "api", name: "Pay as you go · Global", chat: "https://api.moonshot.ai/v1", anthropic: "https://api.moonshot.ai/anthropic", catalog: "moonshotai", keysUrl: "https://platform.kimi.ai?aff=magpie", website: "https://platform.kimi.ai?aff=magpie" },
    ],
    catalog: "kimi-code-plan-cn",
    keysUrl: "https://www.kimi.com/code?aff=magpie",
    website: "https://www.kimi.com/code?aff=magpie",
    iconUrl: "https://usemagpie.ai/img/partner-kimi.png",
    notes: {
      en: "One subscription, one K3 quota shared by every agent",
      zh: "一份订阅，所有 agent 共享 K3 额度",
      "zh-TW": "一份訂閱，所有 agent 共享 K3 額度",
      ja: "1つのサブスクリプションで、すべての agent が K3 の枠を共有",
      de: "Ein Abo, ein K3-Kontingent für alle Agents",
    },
    from: "2026-10-23T00:00:00+08:00",
  },
  {
    id: "apikeyfan",
    name: "APIKEY.FAN",
    regions: [
      { id: "main", name: "Default", chat: "https://api.apikey.fan/v1", responses: "https://api.apikey.fan/v1", anthropic: "https://api.apikey.fan" },
      { id: "slb", name: "Direct", chat: "https://slb.apikey.fan/v1", responses: "https://slb.apikey.fan/v1", anthropic: "https://slb.apikey.fan" },
    ],
    chat: "https://api.apikey.fan/v1",
    responses: "https://api.apikey.fan/v1",
    anthropic: "https://api.apikey.fan",
    keysUrl: "https://apikey.fan/?aff=magpie",
    website: "https://apikey.fan/?aff=magpie",
    iconUrl: "https://usemagpie.ai/img/partner-apikeyfan.png",
    notes: {
      en: "Enterprise-grade AI API gateway to the world's leading models",
      zh: "企业级 AI 中转站，连接全球主流大模型",
      "zh-TW": "企業級 AI 中轉站，連接全球主流大模型",
    },
    langs: ["zh", "en"],
    from: "2026-10-24T00:00:00+08:00",
    until: "2026-12-24T00:00:00+08:00",
  },
  {
    id: "wandapi",
    name: "WandAPI",
    chat: "https://api.wandapi.ai/v1",
    responses: "https://api.wandapi.ai/v1",
    anthropic: "https://api.wandapi.ai",
    keysUrl: "https://wandapi.ai/r/magpieapp",
    website: "https://wandapi.ai",
    iconUrl: "https://usemagpie.ai/img/partner-wandapi.png",
    notes: {
      en: "Enterprise-grade AI gateway, seamless access to global AI models",
      zh: "企业级 AI 网关，畅连全球 AI 大模型",
      "zh-TW": "企業級 AI 閘道，暢連全球 AI 大模型",
    },
    from: "2026-10-25T00:00:00+08:00",
    until: "2026-11-25T00:00:00+08:00",
  },
  {
    id: "corecode",
    name: "CoreCode",
    chat: "https://corecode.live/v1",
    responses: "https://corecode.live/v1",
    anthropic: "https://corecode.live",
    keysUrl: "https://corecode.live/keys?ref=magpie",
    website: "https://corecode.live/sign-in?ref=magpie",
    iconUrl: "https://usemagpie.ai/img/partner-corecode.png",
    notes: {
      en: "Full-strength Claude. Low prices. Enterprise-grade caching. Invoices available. Sign up for magpie and get $3 free.",
      zh: "满血 Claude，超低折扣，企业级缓存优化，可开票。magpie 用户注册送 $3。",
      "zh-TW": "滿血 Claude，超低折扣，企業級快取優化，可開票。magpie 用戶註冊送 $3。",
    },
    from: "2026-10-26T00:00:00+08:00",
    until: "2026-11-26T00:00:00+08:00",
  },
  {
    id: "sidrune",
    name: "Sidrune AI",
    regions: [
      { id: "cn", name: "China", chat: "https://sidrune.ai/v1", responses: "https://sidrune.ai/v1", anthropic: "https://sidrune.ai", keysUrl: "https://sidrune.ai/register?promo=MAGPIE3", website: "https://sidrune.ai/" },
      { id: "global", name: "Global", chat: "https://global.sidrune.ai/v1", responses: "https://global.sidrune.ai/v1", anthropic: "https://global.sidrune.ai", keysUrl: "https://global.sidrune.ai/register?promo=MAGPIE3", website: "https://global.sidrune.ai/" },
    ],
    keysUrl: "https://sidrune.ai/register?promo=MAGPIE3",
    website: "https://sidrune.ai/",
    iconUrl: "https://usemagpie.ai/img/partner-sidrune.png",
    notes: {
      en: "Stable Claude API Access with 85% off; $3 free credit upon registration",
      zh: "低至1.5折，稳定使用满血Claude；注册送$3，可开发票。",
      "zh-TW": "低至1.5折，穩定使用滿血Claude；註冊送$3，可開發票。",
    },
    langs: ["zh", "en"],
    from: "2026-10-26T00:00:00+08:00",
    until: "2026-11-26T00:00:00+08:00",
  },
  {
    id: "fastai",
    name: "FastAIToken",
    chat: "https://www.fastaitoken.com/v1",
    responses: "https://www.fastaitoken.com/v1",
    anthropic: "https://www.fastaitoken.com",
    keysUrl: "https://www.fastaitoken.com/register?aff=MAGPIE&utm=soft",
    website: "https://www.fastaitoken.com/register?aff=MAGPIE&utm=soft",
    iconUrl: "https://usemagpie.ai/img/partner-fastai.png",
    notes: {
      en: "One-stop access to Claude, GPT and DeepSeek, from 6% of list price",
      zh: "Claude / GPT / Deepseek 一站式接入方案，最低0.6折，支持对公",
      "zh-TW": "Claude / GPT / Deepseek 一站式接入方案，最低0.6折，支援對公",
    },
    from: "2026-10-26T00:00:00+08:00",
    until: "2026-11-26T00:00:00+08:00",
  },
];
