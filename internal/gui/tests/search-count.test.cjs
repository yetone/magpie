// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Models › Web search (#5545, mdwsk88): "Results per search" is
// how many results a search that names no count (Claude Code's WebSearch,
// Codex's web_search) is answered with. It shows 6 when none is set, and
// a count set by hand in settings.json among the choices; a click sets it
// on its own (POST /api/settings/search-count), not with a save of the
// page, and moves nothing. In every language, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, reach } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: "Results per search",
  zh: "每次搜索的结果条数",
  "zh-TW": "每次搜尋的結果數",
  ja: "1 回の検索の結果数",
  de: "Ergebnisse pro Suche",
};

function serve(lang, saved, counts, searchCount) {
  const settings = { lang, theme: "light", redact: true, searcher: "ds", searchCount,
    searchProvider: "DeepSeek · deepseek-flash",
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
    if (url.pathname === "/api/settings/search-count") {
      counts.push(JSON.parse(r.request().postData()));
      settings.searchCount = counts.at(-1).n;
      return json(settings);
    }
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

async function open(engine, t, lang, saved, counts, searchCount, width) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 900 } })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, saved, counts, searchCount));
  await watchSettingsReads(page);
  await page.goto(`http://magpie.test/?view=settings&tab=models&lang=${lang}`);
  await settingsDrawn(page);
  return { page, errors };
}

async function press(page, locator) {
  await reach(page, locator);
  const b = await locator.boundingBox();
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
}

const choices = (row) => row.locator(".segs button").evaluateAll((bs) => bs.map((b) => b.textContent.trim()));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, name] of Object.entries(words)) {
    for (const width of [1100, 560]) {
      test(`${engine} ${lang} ${width}px: how many results a search is answered with is set on its own`, async (t) => {
        const saved = [], counts = [];
        const { page, errors } = await open(engine, t, lang, saved, counts, undefined, width);
        const row = page.locator("#searchList #searchCountRow");
        await reach(page, row);
        assert.equal(await row.locator(".name").textContent(), name);
        assert.notEqual(await row.locator(".sub").innerText(), "", "the cost is said");
        assert.deepEqual(await choices(row), ["3", "6", "10", "15", "20"]);
        assert.equal(await row.locator(".segs button.on").textContent(), "6", "6 when none is set");
        assert(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth), "the page scrolls sideways");
        const rb = await row.boundingBox();
        for (const b of await row.locator(".segs button").all()) {
          const bb = await b.boundingBox();
          assert(bb.x >= rb.x - 1 && bb.x + bb.width <= rb.x + rb.width + 1, `${await b.textContent()} sticks out of its row`);
        }

        const ten = row.locator(".segs button", { hasText: /^10$/ });
        await reach(page, ten);
        const before = await page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]);
        await press(page, ten);
        for (let i = 0; i < 50 && !counts.length; i++) await page.waitForTimeout(50);
        assert.deepEqual(counts, [{ n: 10 }], "set once, on its own");
        assert.equal(saved.length, 0, "the rest of the settings not written");
        await page.waitForFunction(() => prefsBusy === 0);
        await page.locator("#searchCountRow .segs button.on", { hasText: /^10$/ }).waitFor();
        assert.deepEqual(await page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]), before, "the click moved nothing");
        assert.deepEqual(errors, []);
      });
    }

    test(`${engine} ${lang}: a count set by hand is one of the choices, and on`, async (t) => {
      const { page, errors } = await open(engine, t, lang, [], [], 12, 560);
      const row = page.locator("#searchList #searchCountRow");
      await reach(page, row);
      assert.deepEqual(await choices(row), ["3", "6", "10", "12", "15", "20"]);
      assert.equal(await row.locator(".segs button.on").textContent(), "12");
      assert.deepEqual(errors, []);
    });
  }
}
