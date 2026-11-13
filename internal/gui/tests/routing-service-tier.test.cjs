// Run with Node's test runner and Playwright on the module path; see README.md.
// The Routing page's request row says the service tier a request went out
// at (Anotiaww on X: a Codex /fast turn, service_tier "priority", showed
// nothing of it): one quiet chip beside the model, "fast" for priority,
// "Ultrafast", "flex", its title naming the tier and, when the reply said
// another, what it said. A request in flight shows the tier it asked for;
// one that went out without it (a provider that drops the tier) and one
// that asked none show no chip. A group member sent fast keeps its one
// chip and its own title. Chromium and WebKit, 1000 and 480 wide, in
// every language, the API faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const at = (i) => new Date(now.getTime() - (i + 1) * 9e3).toISOString();
const seat = { id: "codex", provider: "openai", name: "OpenAI", who: "Codex's own sign-in", kind: "account", agent: "codex", model: "gpt-6.1-sol" };
// newest first
const RUNS = [
  { tier: "priority", try: { tier: "priority" } }, // Codex /fast on its own sign-in
  { tier: "priority", try: { tier: "priority", tierServed: "default" } }, // the reply said another
  { tier: "ultrafast", try: { tier: "ultrafast" } },
  { tier: "flex", try: { tier: "flex" } },
  { tier: "priority", try: {} }, // went out without it
  { try: {} }, // asked none
  { try: { fast: true, tier: "priority" } }, // a group member sent fast
  { tier: "priority", live: true, try: {} }, // on its way
];
const routes = RUNS.map((run, i) => ({
  id: 100 - i, seq: 100 - i, time: at(i), agent: "codex", model: "gpt-6.1-sol", provider: "openai", ...(run.tier ? { tier: run.tier } : {}),
  order: [seat], done: !run.live, status: run.live ? 0 : 200, ms: 5000, ttft: 900, tokens: 1000,
  tries: [{ id: seat.id, model: "gpt-6.1-sol", start: at(i), done: !run.live, status: run.live ? 0 : 200, ms: 5000, ...run.try }],
}));

const W = {
  en: { fast: "fast", ultra: "Ultrafast", flex: "flex",
    f: (t) => `Sent in its vendor's fast mode (service tier ${t}): quicker, at a higher price`,
    x: (t) => `Sent at its vendor's flex tier (service tier ${t}): slower, at a lower price`,
    r: (t) => `Its reply said it ran at ${t}.`, g: "Sent in its vendor's fast mode, as the group says" },
  zh: { fast: "快速", ultra: "极速", flex: "弹性",
    f: (t) => `以服务商的快速模式发送（service tier ${t}）：更快，价格更高`,
    x: (t) => `以服务商的弹性档发送（service tier ${t}）：更慢，价格更低`,
    r: (t) => `回复说它以 ${t} 运行。`, g: "按分组设置，以服务商的快速模式发送" },
  "zh-TW": { fast: "快速", ultra: "極速", flex: "彈性",
    f: (t) => `以服務商的快速模式傳送（service tier ${t}）：更快，價格更高`,
    x: (t) => `以服務商的彈性檔傳送（service tier ${t}）：更慢，價格更低`,
    r: (t) => `回覆說它以 ${t} 執行。`, g: "按分組設定，以服務商的快速模式傳送" },
  ja: { fast: "高速", ultra: "超高速", flex: "Flex",
    f: (t) => `ベンダーの高速モードで送りました（service tier ${t}）。速くなりますが、料金は高くなります`,
    x: (t) => `ベンダーの Flex 階層で送りました（service tier ${t}）。遅くなりますが、料金は安くなります`,
    r: (t) => `応答では ${t} で実行されたとのことです。`, g: "グループの設定どおり、ベンダーの高速モードで送ります" },
  de: { fast: "schnell", ultra: "Ultrafast", flex: "Flex",
    f: (t) => `Im Schnellmodus des Anbieters gesendet (Service-Tier ${t}): schneller, zu höherem Preis`,
    x: (t) => `In der Flex-Stufe des Anbieters gesendet (Service-Tier ${t}): langsamer, zu niedrigerem Preis`,
    r: (t) => `Laut Antwort lief sie mit ${t}.`, g: "Im Schnellmodus des Anbieters gesendet, gemäß Gruppe" },
};

function serve(lang) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 100, totals: { requests: routes.length, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(W)) for (const width of [1000, 480]) {
    test(`${engine} ${lang} ${width}px: a request row says the service tier it went at`, async (t) => {
      const w = W[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-req").nth(routes.length - 1).waitFor();
      const chips = await page.locator(".rt-req").evaluateAll((rows) => rows.map((r) => {
        const cs = [...r.querySelectorAll(".ef.tier")];
        const c = cs[0], b = c?.getBoundingClientRect(), rb = r.getBoundingClientRect();
        return c ? { n: cs.length, text: c.textContent.trim(), title: c.title, inside: b.width > 0 && b.left >= rb.left - 1 && b.right <= rb.right + 1 } : null;
      }));
      assert.deepEqual(chips.map((c) => c && [c.n, c.text, c.title]), [
        [1, w.fast, w.f("priority")],
        [1, w.fast, `${w.f("priority")}\n${w.r("default")}`],
        [1, w.ultra, w.f("ultrafast")],
        [1, w.flex, w.x("flex")],
        null,
        null,
        [1, w.fast, w.g],
        [1, w.fast, w.f("priority")],
      ]);
      assert(chips.every((c) => !c || c.inside), "each chip sits inside its row");
      assert.deepEqual(errors, []);
    });
  }
}
