// Run with Node's test runner and Playwright on the module path; see README.md.
// Presets in a provider's Headers (l3123109756 on X): a small Presets menu
// fills in what a common client sends. A header the user already set keeps
// its value, a blank row is filled, none of the presets imitates Claude
// Code. In English, Chinese, Traditional Chinese, Japanese and German,
// Chromium and WebKit, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const gw = {
  id: "azgw", name: "Azure Gateway", preset: "", icon: "generic", chat: "", responses: "https://gw.example.com/v1", anthropic: "", catalog: "", baseAPI: "responses",
  models: [{ id: "gpt-5.1", name: "gpt-5.1", on: true }], agents: [], fallback: [], headers: { "User-Agent": "mine/1", "X-Title": "" },
  key: { set: true, masked: "sk-…one" }, keyList: [{ id: "k1", name: "", masked: "sk-…one", on: true, active: true }],
  balanceToken: { takes: false, set: false }, proxy: "",
};

function serve(lang, calls) {
  const providers = { providers: [gw], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/provider/")) {
      calls.push([url.pathname.slice(14), route.request().postDataJSON()]);
      if (url.pathname === "/api/provider/save") return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const label = { en: "Presets", zh: "预设", "zh-TW": "預設", ja: "プリセット", de: "Vorlagen" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(label)) {
    for (const width of [900, 420]) {
      test(`${engine} ${lang} ${width}px: Presets fills a client's headers and keeps the user's own`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], calls = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, calls));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Azure Gateway" }).click();
        const ed = page.locator(".editor");
        await ed.locator(".headers").waitFor({ state: "attached" });

        // a click Playwright scrolls to would close the menu with that scroll
        await ed.locator(".headers .hpresets").scrollIntoViewIfNeeded();
        await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
        await ed.locator(".headers .hpresets").click();
        assert.equal(await ed.locator(".headers .hpresets").textContent(), label[lang]);
        await page.locator(".proto-menu .pm-name").first().waitFor();
        const items = await page.locator(".proto-menu .pm-name").allTextContents();
        assert.deepEqual(items, ["Codex CLI", "Gemini CLI", "OpenCode", "Kilo Code", "Cline", "Roo Code", "Cherry Studio"]);
        assert.ok(!items.some((n) => /claude/i.test(n)), "no Claude Code preset");
        await page.locator(".proto-menu .pm-item", { hasText: "Kilo Code" }).click();
        assert.equal(await page.locator(".proto-menu").count(), 0, "the menu closes");

        await ed.locator(".bar").getByRole("button", { name: { en: "Save", zh: "保存", "zh-TW": "儲存", ja: "保存", de: "Speichern" }[lang], exact: true }).click();
        for (let i = 0; i < 60 && !calls.some((c) => c[0] === "save"); i++) await page.waitForTimeout(50);
        const saved = calls.find((c) => c[0] === "save");
        assert.ok(saved, "saved");
        assert.deepEqual(saved[1].headers, {
          "User-Agent": "mine/1", // the user's own value stays
          "X-Title": "Kilo Code", // an empty one is filled
          "HTTP-Referer": "https://kilocode.ai",
          "X-KILOCODE-EDITORNAME": "Kilo CLI 7.8.27",
        });
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), "no sideways scroll");
        assert.deepEqual(errors, []);
      });
    }
  }
}
