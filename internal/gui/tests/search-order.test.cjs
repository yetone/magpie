// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings' search APIs in the order they are asked, and switched off without
// being removed (#2286, blackrion): one is dragged by its row, anywhere off
// its buttons, to another place, the rows numbered anew at once and the order
// sent; Alt+↑/↓ on a focused row moves it a place; an order that can't be
// saved is put back. Each row has a switch: one off is dimmed, says Off and
// is sent as {vendor, on}; with none on, Search first with is gone. In
// English, Chinese, Japanese and German, Chromium and WebKit, 1100px and
// 420px wide, with the API faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const vendors = [
  { id: "tavily", name: "Tavily", base: "https://api.tavily.com" },
  { id: "exa", name: "Exa", base: "https://api.exa.ai" },
  { id: "brave", name: "Brave Search", base: "https://api.search.brave.com" },
];
const off = { en: "Off", zh: "关闭", ja: "オフ", de: "Aus" };

function serve(lang, posted, opts) {
  let apis = [
    { vendor: "tavily", name: "Tavily", key: "tvly…abcd", ready: true, keyList: [{ ref: "t1", masked: "tvly…abcd" }] },
    { vendor: "exa", name: "Exa", key: "exa…1234", ready: true, off: true, keyList: [{ ref: "e1", masked: "exa…1234" }] },
    { vendor: "brave", name: "Brave Search", key: "bsa…9876", ready: true, keys: 2, keyList: [{ ref: "b1", masked: "bsa…9876" }, { ref: "b2", masked: "bsa…5432" }] },
  ];
  const settings = () => ({ lang, theme: "light", searchVendors: vendors, searchAPIs: apis, searchProvider: "OpenAI" });
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
      if (b.arrange) {
        if (opts.refuse) return json({ error: "no google search API is set up" }, 400);
        apis = b.arrange.map((v) => apis.find((a) => a.vendor === v));
      } else if ("on" in b) {
        apis = apis.map((a) => (a.vendor === b.vendor ? { ...a, off: !b.on || undefined } : a));
      }
      return json(settings());
    }
    if (url.pathname === "/api/settings/search-quota") return json({ keys: [] });
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
  for (const lang of ["en", "zh", "ja", "de"]) for (const width of [1100, 420]) {
    test(`${engine} ${lang} ${width}px: search APIs are dragged into order and switched off`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 2400 } })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posted = [], opts = {};
      await page.route("**/*", serve(lang, posted, opts));
      await watchSettingsReads(page);
      await page.goto("http://magpie.test/?view=settings&tab=models");
      await settingsDrawn(page);
      const list = page.locator("#searchList");
      const units = list.locator(".search-unit");
      await units.first().waitFor();
      for (let i = 0; ; i++) {
        try { await units.last().locator(".row.search-keys").scrollIntoViewIfNeeded({ timeout: 1000 }); break; } catch (e) { if (i >= 4) throw e; }
      }
      const names = () => list.locator(".row.search-api .name").allInnerTexts();
      assert.deepEqual(await names(), ["1. Tavily", "2. Exa", "3. Brave Search"]);
      // Brave's keys' row is in its unit, dragged with it
      assert.equal(await units.nth(2).locator(".row.search-keys").count(), 1);
      // Exa is off: dimmed, says so, its switch unchecked; the others on
      const exa = units.nth(1);
      assert.equal(await exa.evaluate((u) => u.classList.contains("off")), true);
      assert((await exa.locator(".row.search-api .sub").innerText()).startsWith(off[lang]), await exa.locator(".row.search-api .sub").innerText());
      assert.deepEqual(await list.locator("button.search-on").evaluateAll((bs) => bs.map((b) => b.getAttribute("aria-checked"))), ["true", "false", "true"]);
      assert(Number(await exa.locator(".row.search-api .who").evaluate((e) => getComputedStyle(e).opacity)) < 1, "an API off is dimmed");
      assert.equal(await exa.locator("button.search-on").evaluate((e) => getComputedStyle(e).opacity), "1", "its switch isn't");
      assert.equal(await page.locator("#searchFirstRow").count(), 1);
      assert(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth), "nothing wider than the window");
      const where = () => page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]);
      const before = await where();
      const click = async (loc) => {
        const b = await loc.boundingBox();
        await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
      };
      // what was sent saved and the page drawn again from it, Saved said
      const saving = () => page.evaluate(() => { const s = document.querySelector("#status"); s.className = "status"; s.textContent = ""; });
      const saved = () => page.waitForFunction(() => document.querySelector("#status")?.classList.contains("ok"));

      // drag Brave, by its name, above Tavily
      const from = await units.nth(2).locator(".row.search-api .name").boundingBox();
      const to = await units.nth(0).boundingBox();
      await saving();
      await page.mouse.move(from.x + 10, from.y + from.height / 2);
      await page.mouse.down();
      for (let i = 1; i <= 12; i++) await page.mouse.move(from.x + 10, from.y + from.height / 2 + (to.y + 4 - from.y - from.height / 2) * i / 12);
      await page.mouse.up();
      await page.waitForFunction(() => document.querySelector("#searchList .row.search-api .name")?.textContent === "1. Brave Search");
      assert.deepEqual(posted.at(-1), { arrange: ["brave", "tavily", "exa"] });
      assert.deepEqual(await names(), ["1. Brave Search", "2. Tavily", "3. Exa"]);
      assert.equal(posted.length, 1, "letting go sent the order once, and clicked nothing");
      await saved();

      // Alt+↓ on the first row, Brave, moves it a place
      await saving();
      await units.nth(0).focus();
      await page.keyboard.press("Alt+ArrowDown");
      await page.waitForFunction(() => document.querySelector("#searchList .row.search-api .name")?.textContent === "1. Tavily");
      assert.deepEqual(posted.at(-1), { arrange: ["tavily", "brave", "exa"] });
      assert.deepEqual(await names(), ["1. Tavily", "2. Brave Search", "3. Exa"]);
      await saved();
      // the keyboard stays on the row moved
      await page.waitForFunction(() => document.activeElement?.matches?.(".search-unit[data-vendor=brave]") && document.activeElement.isConnected);

      // an order that can't be saved is put back
      opts.refuse = true;
      await units.nth(2).focus();
      await page.keyboard.press("Alt+ArrowUp");
      await page.waitForFunction(() => document.querySelector("#status")?.textContent.includes("no google"));
      await page.waitForFunction(() => [...document.querySelectorAll("#searchList .row.search-api .name")].map((n) => n.textContent).join("|") === "1. Tavily|2. Brave Search|3. Exa");
      opts.refuse = false;

      // Exa switched on, then Tavily and Brave off: with none on, nothing to put first
      await click(list.locator(".search-unit[data-vendor=exa] button.search-on"));
      await page.waitForFunction(() => document.querySelector("#searchList .search-unit[data-vendor=exa] button.search-on")?.getAttribute("aria-checked") === "true");
      assert.deepEqual(posted.at(-1), { vendor: "exa", on: true });
      for (const v of ["tavily", "brave", "exa"]) {
        await click(list.locator(`.search-unit[data-vendor=${v}] button.search-on`));
        await page.waitForFunction((v) => document.querySelector(`#searchList .search-unit[data-vendor=${v}]`)?.classList.contains("off"), v);
        assert.deepEqual(posted.at(-1), { vendor: v, on: false });
      }
      assert.equal(await page.locator("#searchFirstRow").count(), 0, "no API is asked, so nothing to put first");
      assert.deepEqual(await names(), ["1. Tavily", "2. Brave Search", "3. Exa"], "switching moves nothing");
      assert.deepEqual(await where(), before, "the clicks moved nothing");
      assert(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth), "nothing wider than the window");
      assert.deepEqual(errors, []);
    });
  }
}
