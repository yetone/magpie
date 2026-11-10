// Run with Node's test runner and Playwright on the module path; see README.md.
// A custom provider added with only a Gemini URL is added (#2201, anleyu0:
// 自定义供应商选择 Gemini 协议，填写完地址后会认定没有填写地址): Gemini compatible
// picked, its URL typed as the Base URL, Add sends it as the provider's
// gemini URL with baseAPI gemini, and "A base URL is needed" isn't said.
// With no URL at all it still is. In English, Chinese, Traditional Chinese,
// Japanese and German, Chromium and WebKit, at 1100px and 420px.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const G = "https://relay.example.com/proxy/v1beta";

function serve(lang, posts) {
  const providers = { providers: [{ id: "anthropic", name: "Anthropic", icon: "anthropic", preset: "anthropic", models: [], agents: [], key: { set: true, masked: "sk-…ab12" } }], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname.startsWith("/api/provider/")) {
      posts.push({ path: url.pathname, body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    try { await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { name: "e.g. My Relay", add: "Add", need: "A base URL is needed" },
  zh: { name: "例如 My Relay", add: "添加", need: "需要填写 Base URL" },
  "zh-TW": { name: "例如 My Relay", add: "新增", need: "需要填寫 Base URL" },
  ja: { name: "例：My Relay", add: "追加", need: "ベース URL が必要です" },
  de: { name: "z. B. Mein Relay", add: "Hinzufügen", need: "Eine Basis-URL ist erforderlich" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(L)) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: a custom provider with a Gemini URL alone is added`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        t.after(() => browser.close());
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=providers");
        const S = L[lang];
        await page.locator("#addProvider").click();
        await page.locator("#addSheet .row-head .custom").click();
        const ed = page.locator(".editor.new");
        await ed.locator(".base-url").waitFor();
        await ed.locator(`input[placeholder="${S.name}"]`).fill("Gem Relay");
        await ed.locator('input[type="password"]').first().fill("AIza-typed");
        await ed.locator('.segs.base-apis .opt[data-api="gemini"]').click();
        const add = ed.locator(":scope > .bar").getByRole("button", { name: S.add, exact: true });

        // no URL: still said
        await add.click();
        await ed.locator(".editor-error").waitFor();
        assert.equal((await ed.locator(".editor-error").textContent()).trim(), S.need);
        assert(!posts.some((p) => p.path === "/api/provider/save"), "nothing saved without a URL");

        // Gemini's URL alone: added
        await ed.locator(".base-url").fill(G);
        await add.click();
        for (let i = 0; i < 60 && !posts.some((p) => p.path === "/api/provider/save"); i++) await page.waitForTimeout(50);
        const save = posts.find((p) => p.path === "/api/provider/save");
        assert(save, "added: " + (await ed.locator(".editor-error").textContent().catch(() => "")));
        assert.equal(save.body.gemini, G);
        assert.equal(save.body.baseAPI, "gemini");
        assert(!save.body.chat && !save.body.responses && !save.body.anthropic, "no other URL made up");
        assert.deepEqual(errors, []);
      });
    }
  }
}
