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
// A partner whose product is a plugin, not a model endpoint, is listed at
// the top of the Plugins page's Discover instead of in the add sheet:
//
//   plugin     its npm package's name ("@acme/magpie-notes"), with no
//              version; it has no chat, responses, anthropic, regions or
//              keysUrl. id, name, iconUrl, notes, langs, website, from and
//              until are as above.
//
// At most 12 of each kind are listed. An app from before plugin partners
// leaves them out, as entries with no endpoint.
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
      en: "One plan, one K3 quota for every agent",
      zh: "一份订阅，所有 agent 共享 K3 额度",
      "zh-TW": "一份訂閱，所有 agent 共享 K3 額度",
      ja: "1つのプランで、全 agent が K3 枠を共有",
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
      en: "Enterprise AI relay to the top models",
      zh: "企业级 AI 中转，直连全球主流模型",
      "zh-TW": "企業級 AI 中轉，直連全球主流模型",
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
      en: "Enterprise AI gateway to global models",
      zh: "企业级 AI 网关，畅连全球大模型",
      "zh-TW": "企業級 AI 閘道，暢連全球大模型",
    },
    from: "2026-10-25T00:00:00+08:00",
    until: "2026-12-25T00:00:00+08:00",
  },
  {
    id: "corecode",
    name: "CoreCode",
    chat: "https://corecode.live/v1",
    responses: "https://corecode.live/v1",
    anthropic: "https://corecode.live",
    keysUrl: "https://corecode.live/keys?ref=magpie",
    website: "https://corecode.live/?ref=magpie",
    iconUrl: "https://usemagpie.ai/img/partner-corecode.png",
    searches: true,
    notes: {
      en: "High caching full-strength Claude. Get $3 free",
      zh: "高缓存满血 Claude，可开票，注册送 $3",
      "zh-TW": "高緩存滿血 Claude，可開票，註冊送 $3",
    },
    from: "2026-10-26T00:00:00+08:00",
    until: "2026-12-26T00:00:00+08:00",
  },
  {
    id: "sidrune",
    name: "Sidrune AI",
    regions: [
      { id: "cn", name: "China", chat: "https://cn.sidrune.ai/v1", responses: "https://cn.sidrune.ai/v1", anthropic: "https://cn.sidrune.ai", keysUrl: "https://cn.sidrune.ai/register?promo=MAGPIE3", website: "https://cn.sidrune.ai/?promo=MAGPIE3" },
      { id: "global", name: "Global", chat: "https://global.sidrune.ai/v1", responses: "https://global.sidrune.ai/v1", anthropic: "https://global.sidrune.ai", keysUrl: "https://global.sidrune.ai/register", website: "https://global.sidrune.ai/" },
    ],
    keysUrl: "https://cn.sidrune.ai/register?promo=MAGPIE3",
    website: "https://cn.sidrune.ai/?promo=MAGPIE3",
    iconUrl: "https://usemagpie.ai/img/partner-sidrune.png",
    notes: {
      en: "Claude at 85% off",
      zh: "满血 Claude 低至 1.5 折，中国区注册送 $3",
      "zh-TW": "滿血 Claude 低至 1.5 折，中國區註冊送 $3",
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
      en: "Claude, GPT, DeepSeek from 6% of list",
      zh: "低至 0.6 折，Claude/GPT/DeepSeek 全有",
      "zh-TW": "低至 0.6 折，Claude/GPT/DeepSeek 全有",
    },
    from: "2026-10-26T00:00:00+08:00",
    until: "2026-11-26T00:00:00+08:00",
  },
];
