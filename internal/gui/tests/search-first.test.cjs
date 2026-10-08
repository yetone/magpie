// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Models › Web search (#928, hypooo): with both a provider that
// searches and a search API set up, "Search first with" picks which goes
// first — Model search, as before, or Search APIs, the other asked when it
// fails. A click saves searchFirst with the rest of the settings kept, the
// Search APIs row then says they are asked first, and the page isn't moved.
// With only one of the two the row isn't there. In English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: { name: "Search first with", model: "Model search", apis: "Search APIs", before: "Asked before DeepSeek · deepseek-flash" },
  zh: { name: "优先使用", model: "模型代搜", apis: "搜索 API", before: "先用这些搜索，都失败时再由 DeepSeek · deepseek-flash 代搜" },
};

function serve(lang, saved, { provider = true } = {}) {
  const settings = { lang, theme: "light", redact: true, searcher: "ds",
    searchProvider: provider ? "DeepSeek · deepseek-flash" : "",
    searchVendors: [{ id: "tavily", name: "Tavily", keysURL: "https://app.tavily.com/home" }],
    searchAPIs: [{ vendor: "tavily", name: "Tavily", key: "tvly…abcd", ready: true }],
    searchChoices: [{ id: "ds", name: "DeepSeek", icon: "deepseek", small: "deepseek-flash", models: [] }] };
  const state = { agents: [], profiles: [], settings };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/settings") {
      if (r.request().method() === "POST") {
        saved.push(JSON.parse(r.request().postData()));
        Object.assign(settings, saved.at(-1));
      }
      return json(settings);
    }
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404 }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: web search can go to the search APIs first`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 900 } })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const saved = [];
      await page.route("**/*", serve(lang, saved));
      await page.goto("http://magpie.test/?view=settings&tab=models");
      const row = page.locator("#searchList #searchFirstRow");
      await row.waitFor();
      await row.scrollIntoViewIfNeeded();
      assert.equal(await row.locator(".name").textContent(), w.name);
      const model = row.locator(".segs button", { hasText: w.model });
      const apis = row.locator(".segs button", { hasText: w.apis });
      assert(await model.evaluate((b) => b.classList.contains("on")), "model search first at first");
      assert(!await apis.evaluate((b) => b.classList.contains("on")));

      const before = await page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]);
      const b = await apis.boundingBox();
      await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
      for (let i = 0; i < 50 && !saved.length; i++) await page.waitForTimeout(50);
      assert.equal(saved.length, 1, "saved once");
      assert.equal(saved[0].searchFirst, "api");
      assert.equal(saved[0].searcher, "ds", "the rest kept");
      assert.equal(saved[0].redact, true);
      // the save's answer draws Settings again, every option replaced: what is read
      // and measured below is the page drawn from it
      await page.waitForFunction(() => prefsBusy === 0);
      await page.locator("#searchFirstRow .segs button.on", { hasText: w.apis }).waitFor();
      assert((await page.locator("#searchList .row.search-add .sub").innerText()).includes(w.before));
      assert.deepEqual(await page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]), before, "the click moved nothing");

      // and back
      const m = await page.locator("#searchFirstRow .segs button", { hasText: w.model }).boundingBox();
      await page.mouse.click(m.x + m.width / 2, m.y + m.height / 2);
      for (let i = 0; i < 50 && saved.length < 2; i++) await page.waitForTimeout(50);
      assert.equal(saved.at(-1).searchFirst, "");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: with no provider that searches there is nothing to choose`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 900 } })).newPage();
      page.setDefaultTimeout(5000);
      await page.route("**/*", serve(lang, [], { provider: false }));
      await page.goto("http://magpie.test/?view=settings&tab=models");
      await page.locator("#searchList .row.search-add").waitFor();
      assert.equal(await page.locator("#searchFirstRow").count(), 0);
    });
  }
}
