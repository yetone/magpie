// Run with Node's test runner and Playwright on the module path; see README.md.
// jiangguochao666 on X asked for each provider's free models to show in
// Routing and the pickers. A model its provider lists at $0 (zero,
// provider.PricedFree: OpenRouter's foo:free, NVIDIA NIM's) says free, with
// a hover that says why, in an agent's model picker, in the provider
// editor's model list, and in a routing group's Add a model picker. The
// paid model beside it says nothing, and one its plan serves free keeps
// the plan's hover. Every language, Chromium and WebKit, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: { edit: "Edit", free: "free", zero: "free: listed at $0 on this provider", plan: "free: it doesn't use the plan's credits" },
  zh: { edit: "编辑", free: "免费", zero: "免费：此服务商标价为 $0", plan: "免费：不消耗套餐积分" },
  "zh-TW": { edit: "編輯", free: "免費", zero: "免費：此服務商標價為 $0", plan: "免費：不消耗套餐積分" },
  ja: { edit: "編集", free: "無料", zero: "無料：このプロバイダーでの価格は $0 です", plan: "無料：プランのクレジットを消費しません" },
  de: { edit: "Bearbeiten", free: "kostenlos", zero: "kostenlos: bei diesem Anbieter mit $0 gelistet", plan: "kostenlos: verbraucht keine Credits des Tarifs" },
};

// the agent's picker: the gateway's Option.Zero and Option.Free
const options = [
  { value: "magpie/or/nemotron-lite", ref: "or/nemotron-lite", label: "Nemotron Lite", group: "OpenRouter", icon: "openrouter", zero: true },
  { value: "magpie/or/glm-5", ref: "or/glm-5", label: "GLM-5", group: "OpenRouter", icon: "openrouter" },
  { value: "magpie/wb/kimi-k3", ref: "wb/kimi-k3", label: "Kimi K3", group: "WorkBuddy", icon: "generic", free: true },
];
// the editor's list: modelJSON.Zero
const fetched = new Date(Date.now() - 3600e3).toISOString();
const relay = {
  id: "or", name: "OpenRouter", icon: "openrouter", host: "openrouter.ai", chat: "https://openrouter.ai/api/v1", responses: "", anthropic: "", catalog: "openrouter",
  models: [{ id: "nemotron-lite", name: "nemotron-lite", on: true, zero: true }, { id: "glm-5", name: "glm-5", on: true }],
  chosen: ["nemotron-lite", "glm-5"], fetched, agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};
// the Routing page's models: modelRef.Zero
const models = [
  { id: "or/nemotron-lite", name: "nemotron-lite", provider: "or", providerName: "OpenRouter", icon: "openrouter", zero: true },
  { id: "or/glm-5", name: "glm-5", provider: "or", providerName: "OpenRouter", icon: "openrouter" },
];

function serve(lang) {
  const groups = () => ({
    models, deciders: [], pools: [],
    groups: [{ id: "g", name: "G", members: ["or/glm-5"], off: [], routing: "order", ready: true, memberInfo: [{ id: "or/glm-5", ready: true, provider: "or", name: "OpenRouter", model: "glm-5" }] }],
  });
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", icon: "codex-color", wired: false, fields: [{ key: "model", label: "model", value: "magpie/or/glm-5", options }] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname.startsWith("/api/groups/")) return json(groups());
    if (url.pathname === "/api/providers") return json({ providers: [relay], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// a picker row's free tag: its text, its hover, and whether it sits in the row
const tagOf = (row) => row.evaluate((li) => {
  const f = li.querySelector(".badge.free");
  if (!f) return null;
  const r = li.getBoundingClientRect(), b = f.getBoundingClientRect();
  return { text: f.textContent, title: f.title, inside: b.width > 4 && b.left >= r.left && b.right <= r.right + 0.5 };
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const lang of Object.keys(words)) {
      const w = words[lang];
      test(`${engine} ${width} ${lang}: a model listed at $0 says free in the agent's picker, the editor and a group's picker`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 1100 }, reducedMotion: "reduce" })).newPage();
        t.after(() => browser.close());
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        const sideways = () => page.evaluate(() => document.documentElement.scrollWidth > innerWidth);

        // the agent's model picker
        await page.goto("http://magpie.test/");
        await page.locator('#nav [data-view="agents"]').click();
        await page.locator('#agents [data-key="model"]').first().click();
        const row = (label) => page.locator("#list li:not(.group):not(.custom)", { hasText: label }).first();
        await row("Nemotron Lite").waitFor();
        const zero = await tagOf(row("Nemotron Lite"));
        assert.ok(zero, "the $0 model has no free tag in the agent's picker");
        assert.equal(zero.text, w.free);
        assert.equal(zero.title, w.zero);
        assert.ok(zero.inside, "the free tag is squeezed or out of its row");
        assert.equal(await tagOf(row("GLM-5")), null, "the paid model says free");
        const plan = await tagOf(row("Kimi K3"));
        assert.equal(plan?.title, w.plan, "a plan's free model lost its own hover");
        assert.ok(!(await sideways()), "the page scrolls sideways");
        await page.keyboard.press("Escape");

        // the provider editor's model list
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "OpenRouter" }).click();
        const editor = page.locator("#modal:not([hidden]) .editor");
        const chip = (id) => editor.locator(".mchips .mchip", { has: page.locator("span", { hasText: new RegExp(`^${id}$`) }) });
        await chip("nemotron-lite").waitFor();
        const free = chip("nemotron-lite").locator(".badge.free");
        assert.equal(await free.count(), 1, "the $0 model has no free tag in the editor");
        assert.equal((await free.textContent()).trim(), w.free);
        const hover = await chip("nemotron-lite").getAttribute("title");
        assert.ok(hover.split("\n")[0].endsWith(" · " + w.zero), `the chip's hover doesn't say why it is free: ${hover}`);
        assert.equal(await chip("glm-5").locator(".badge.free").count(), 0, "the paid model says free in the editor");
        assert.ok(!(await sideways()), "the page scrolls sideways");
        await page.keyboard.press("Escape");
        await page.locator("#modal").waitFor({ state: "hidden" });

        // a routing group's Add a model picker
        await page.goto("http://magpie.test/?view=groups");
        const card = page.locator('.rt-group[data-id="g"]');
        await card.waitFor();
        await card.locator("button", { hasText: w.edit }).click();
        await page.locator(".rt-gedit .rt-madds button.rt-gadd").click();
        const opt = (name) => page.locator("#pop li", { has: page.locator(".v", { hasText: new RegExp(`^${name}$`) }) }).first();
        await opt("nemotron-lite").waitFor();
        const rz = await tagOf(opt("nemotron-lite"));
        assert.ok(rz, "the $0 model has no free tag in the group's picker");
        assert.equal(rz.text, w.free);
        assert.equal(rz.title, w.zero);
        assert.ok(rz.inside, "the free tag is squeezed or out of its row");
        assert.equal(await tagOf(opt("glm-5")), null, "the paid model says free in the group's picker");
        assert.ok(!(await sideways()), "the page scrolls sideways");

        const stripes = await page.evaluate(() => [...document.querySelectorAll(".badge.free")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
        assert.deepEqual(stripes, [], "no border stripes");
        assert.deepEqual(errors, []);
      });
    }
  }
}
