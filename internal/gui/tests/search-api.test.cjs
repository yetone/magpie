// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings' Web search section (#419): the search APIs a model's search goes
// to when no provider can search, in their order, and a row to add one. A
// SearXNG picked asks for its address; Add sends the API, its key and its
// address; Remove takes one away; a key magpie refuses is said in the row,
// what was typed kept. In English and Chinese, Chromium and WebKit, with the
// API faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const vendors = [
  { id: "tavily", name: "Tavily", keysURL: "https://app.tavily.com/home" },
  { id: "brave", name: "Brave Search", keysURL: "https://api-dashboard.search.brave.com/app/keys" },
  { id: "exa", name: "Exa", keysURL: "https://dashboard.exa.ai/api-keys" },
  { id: "firecrawl", name: "Firecrawl", keysURL: "https://www.firecrawl.dev/app/api-keys" },
  { id: "searxng", name: "SearXNG", needURL: true },
];
const words = {
  en: { head: "Web search", name: "Search APIs", none: "No provider can search", add: "Add", remove: "Remove", refused: "Brave Search: 401 bad key" },
  zh: { head: "联网搜索", name: "搜索 API", none: "没有能搜索的供应商", add: "添加", remove: "移除", refused: "Brave Search: 401 bad key" },
};

function serve(lang, posted) {
  let apis = [{ vendor: "tavily", name: "Tavily", key: "tvly…abcd", ready: true }];
  const settings = () => ({ lang, theme: "light", searchVendors: vendors, searchAPIs: apis });
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data, status = 200) => r.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings() });
    if (url.pathname === "/api/settings") return json(settings());
    if (url.pathname === "/api/settings/search-api") {
      const b = JSON.parse(r.request().postData());
      posted.push(b);
      if (b.remove) apis = apis.filter((a) => a.vendor !== b.vendor);
      else if (b.vendor === "brave" && b.key === "bad") return json({ error: words.en.refused }, 400);
      else apis = [...apis, { vendor: b.vendor, name: vendors.find((v) => v.id === b.vendor).name, url: b.url, key: b.key ? "••••" : "", ready: true }];
      return json(settings());
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: search APIs are added and removed in Settings`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 1400 } })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posted = [];
      await page.route("**/*", serve(lang, posted));
      await page.goto("http://magpie.test/?view=settings&tab=models");
      const list = page.locator("#searchList");
      const head = list.locator(".row.search-add");
      await head.waitFor();
      await head.scrollIntoViewIfNeeded();
      assert.equal(await list.locator("xpath=preceding-sibling::div[1]").textContent(), w.head);
      assert.equal(await head.locator(".name").innerText(), w.name);
      assert((await head.locator(".sub").innerText()).includes(w.none));
      assert.deepEqual(await list.locator(".row.search-api .name").allInnerTexts(), ["1. Tavily"]);
      assert.equal(await head.locator("input.search-url").isVisible(), false, "Tavily needs no address");

      const where = () => page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]);
      const click = async (loc) => {
        const b = await loc.boundingBox();
        await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
      };
      const before = await where();
      // SearXNG: its address
      await click(head.locator("button.search-vendor"));
      await click(page.locator(".proto-menu .pm-item", { hasText: "SearXNG" }));
      assert.equal(await head.locator("button.search-vendor").innerText(), "SearXNG");
      assert.equal(await head.locator("input.search-url").isVisible(), true, "SearXNG asks for its address");
      await head.locator("input.search-url").fill("https://sx.example.com");
      await click(head.locator("button.text", { hasText: w.add }));
      await page.waitForFunction(() => document.querySelectorAll("#searchList .row.search-api").length === 2);
      assert.deepEqual(posted.at(-1), { vendor: "searxng", key: "", url: "https://sx.example.com" });
      assert.deepEqual(await list.locator(".row.search-api .name").allInnerTexts(), ["1. Tavily", "2. SearXNG"]);
      assert((await list.locator(".row.search-api").nth(1).locator(".sub").innerText()).includes("https://sx.example.com"));

      // Brave refused: said in the row, the vendor picked kept
      await click(head.locator("button.search-vendor"));
      await click(page.locator(".proto-menu .pm-item", { hasText: "Brave Search" }));
      assert.equal(await head.locator("input.search-url").isVisible(), false);
      // an empty key isn't sent at all
      await click(head.locator("button.text", { hasText: w.add }));
      assert.equal(posted.length, 1, "nothing sent without a key");
      await head.locator("input.search-key").fill("bad");
      await click(head.locator("button.text", { hasText: w.add }));
      await page.waitForFunction(() => document.querySelector("#searchList .search-add .sub.err"));
      assert.equal(await head.locator(".sub").innerText(), w.refused);
      assert.equal(await head.locator("button.search-vendor").innerText(), "Brave Search");
      assert.equal(await head.locator("input.search-key").inputValue(), "bad", "what was typed is kept");
      assert.deepEqual(await list.locator(".row.search-api .name").allInnerTexts(), ["1. Tavily", "2. SearXNG"]);

      // Remove
      await click(list.locator(".row.search-api").first().locator("button.text", { hasText: w.remove }));
      await page.waitForFunction(() => document.querySelectorAll("#searchList .row.search-api").length === 1);
      assert.deepEqual(posted.at(-1), { vendor: "tavily", remove: true });
      assert.deepEqual(await list.locator(".row.search-api .name").allInnerTexts(), ["1. SearXNG"]);
      assert.deepEqual(await where(), before, "the clicks moved nothing");
      assert.deepEqual(errors, []);
    });
  }
}
