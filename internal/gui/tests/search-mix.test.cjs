// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Models › Web search › Advanced (#2845's mix, heyiwei5 on X):
// "Ask the search APIs" in order, or all at once with their results merged.
// The fold shows only with two or more APIs on, closed while none is mixed;
// All at once names every API on (POST /api/settings/search-api {mix}), In
// order names none, and the fold is open while some are mixed. A mix of
// some of them (magpie search mix) has neither choice on, and its line
// names them. In every language, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, reach } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: ["Ask the search APIs", "In order", "All at once", "Advanced"],
  zh: ["搜索 API 的调用方式", "按顺序", "同时调用", "高级"],
  "zh-TW": ["搜尋 API 的呼叫方式", "按順序", "同時呼叫", "進階"],
  ja: ["検索 API の使い方", "順番", "同時に", "詳細設定"],
  de: ["Such-APIs fragen", "Der Reihe nach", "Alle zugleich", null],
};

const API = {
  tavily: { vendor: "tavily", name: "Tavily", key: "tvly…abcd", ready: true },
  brave: { vendor: "brave", name: "Brave Search", key: "BSA…abcd", ready: true },
  exa: { vendor: "exa", name: "Exa", key: "exa…abcd", ready: true },
};

function serve(lang, apis, posts) {
  const settings = { lang, theme: "light", redact: true, searcher: "off",
    searchVendors: Object.values(API).map((a) => ({ id: a.vendor, name: a.name })),
    searchAPIs: apis };
  const state = { agents: [], profiles: [], settings };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/settings/search-api") {
      const b = JSON.parse(r.request().postData());
      posts.push(b);
      settings.searchAPIs = settings.searchAPIs.map((a) => ({ ...a, mix: b.mix.includes(a.vendor) || undefined }));
      return json(settings);
    }
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404 }); }
  };
}

async function open(engine, t, lang, apis, posts, width) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 900 } })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, apis, posts));
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

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, [name, inOrder, all, adv]] of Object.entries(words)) {
    for (const width of [1100, 560]) {
      test(`${engine} ${lang} ${width}px: the search APIs are asked all at once from Advanced, and in order again`, async (t) => {
        const posts = [];
        const { page, errors } = await open(engine, t, lang, [API.tavily, API.brave], posts, width);
        const more = page.locator("#searchMore");
        await reach(page, more.locator("summary"));
        assert.equal(await more.evaluate((d) => d.open), false, "closed while none is mixed");
        if (adv) assert.equal((await more.locator("summary").textContent()).trim(), adv);
        await press(page, more.locator("summary"));
        const row = page.locator("#searchMixRow");
        await reach(page, row);
        assert.equal(await row.locator(".name").textContent(), name);
        assert.notEqual(await row.locator(".sub").innerText(), "");
        assert.deepEqual(await row.locator(".segs button").evaluateAll((bs) => bs.map((b) => b.textContent.trim())), [inOrder, all]);
        assert.equal(await row.locator(".segs button.on").textContent(), inOrder);
        assert(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth), "the page scrolls sideways");
        const rb = await row.boundingBox();
        for (const b of await row.locator(".segs button").all()) {
          const bb = await b.boundingBox();
          assert(bb.x >= rb.x - 1 && bb.x + bb.width <= rb.x + rb.width + 1, `${await b.textContent()} sticks out of its row`);
        }

        await press(page, row.locator(".segs button", { hasText: all }));
        for (let i = 0; i < 50 && !posts.length; i++) await page.waitForTimeout(50);
        assert.deepEqual(posts, [{ mix: ["tavily", "brave"] }]);
        await page.locator("#searchMixRow .segs button.on", { hasText: all }).waitFor();
        assert.equal(await more.evaluate((d) => d.open), true, "open while some are mixed");

        await press(page, page.locator("#searchMixRow .segs button", { hasText: inOrder }));
        for (let i = 0; i < 50 && posts.length < 2; i++) await page.waitForTimeout(50);
        assert.deepEqual(posts[1], { mix: [] });
        await page.locator("#searchMixRow .segs button.on", { hasText: inOrder }).waitFor();
        assert.deepEqual(errors, []);
      });
    }

    test(`${engine} ${lang}: one search API has no Advanced; a mix of some names them`, async (t) => {
      {
        const { page, errors } = await open(engine, t, lang, [API.tavily, { ...API.brave, off: true }], [], 560);
        await reach(page, page.locator("#searchMcpRow"));
        assert.equal(await page.locator("#searchMore").isVisible(), false, "nothing to mix with one on");
        assert.deepEqual(errors, []);
      }
      const { page, errors } = await open(engine, t, lang, [{ ...API.tavily, mix: true }, API.exa, { ...API.brave, mix: true }], [], 560);
      const row = page.locator("#searchMixRow");
      assert.equal(await page.locator("#searchMore").evaluate((d) => d.open), true);
      await reach(page, row);
      assert.equal(await row.locator(".segs button.on").count(), 0, "neither choice is what is set");
      const sub = await row.locator(".sub").innerText();
      assert(sub.includes("Tavily") && sub.includes("Brave Search") && !sub.includes("Exa"), sub);
      assert.deepEqual(errors, []);
    });
  }
}
