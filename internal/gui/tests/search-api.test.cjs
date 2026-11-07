// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings' Web search section (#419): the search APIs a model's search goes
// to when no provider can search, in their order, and a row to add one. A
// SearXNG picked asks for its address; Add sends the API, its key and its
// address; Remove takes one away; a key magpie refuses is said in the row,
// what was typed kept. A saved key is shown in its row of the API's keys
// and hidden again, or copied, as a provider's is (OnurBen on Discord); a
// key typed for an API added before joins its keys (辰林鸭~ on Discord). Every API takes a base URL
// of the user's own (#1580), on a line under the key: empty is the vendor's,
// and one set shows in the field when its API is picked again, so a new key
// alone keeps it. You.com is added with no key, for its free search
// (#1626). In English and Chinese, Chromium and WebKit, 980px and
// 420px wide, with the API faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, reach } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const vendors = [
  { id: "tavily", name: "Tavily", keysURL: "https://app.tavily.com/home", base: "https://api.tavily.com" },
  { id: "brave", name: "Brave Search", keysURL: "https://api-dashboard.search.brave.com/app/keys", base: "https://api.search.brave.com" },
  { id: "exa", name: "Exa", keysURL: "https://dashboard.exa.ai/api-keys", base: "https://api.exa.ai" },
  { id: "firecrawl", name: "Firecrawl", keysURL: "https://www.firecrawl.dev/app/api-keys", base: "https://api.firecrawl.dev" },
  { id: "you", name: "You.com", keysURL: "https://you.com/platform", base: "https://ydc-index.io", keyOptional: true },
  { id: "searxng", name: "SearXNG", needURL: true },
];
const words = {
  en: { head: "Web search", name: "Search APIs", none: "No provider can search", add: "Add", remove: "Remove", refused: "Brave Search: 401 bad key", show: "Show", hide: "Hide", copy: "Copy",
    base: "Base URL, empty for https://api.tavily.com", exaBase: "Base URL, empty for https://api.exa.ai", keep: "Another key for Tavily, or empty to keep its keys", youKey: "API key, or empty for its free search" },
  zh: { head: "联网搜索", name: "搜索 API", none: "没有能搜索的供应商", add: "添加", remove: "移除", refused: "Brave Search: 401 bad key", show: "显示", hide: "隐藏", copy: "复制",
    base: "API 地址，留空则用 https://api.tavily.com", exaBase: "API 地址，留空则用 https://api.exa.ai", keep: "Tavily 的另一个密钥，留空则保留它的密钥", youKey: "API 密钥，留空则用其免费搜索" },
};

function serve(lang, posted) {
  let apis = [{ vendor: "tavily", name: "Tavily", key: "tvly…abcd", ready: true, keyList: [{ ref: "r-abcd", masked: "tvly…abcd" }] }];
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
      else {
        // as SetSearchAPI: one of a vendor added before is changed where it
        // is, keeping its key when none is sent
        // its keys when none is sent, and a key sent with append after them
        const was = apis.find((a) => a.vendor === b.vendor);
        const typed = b.key ? [{ ref: "r-" + b.key, masked: "••••" + b.key.slice(-1) }] : [];
        const keyList = b.append ? [...(was?.keyList || []), ...typed] : b.key ? typed : was?.keyList || [];
        const now = { vendor: b.vendor, name: vendors.find((v) => v.id === b.vendor).name, url: b.url || undefined, key: keyList.map((k) => k.masked).join(", "), keyList, ready: true };
        apis = was ? apis.map((a) => (a === was ? now : a)) : [...apis, now];
      }
      return json(settings());
    }
    if (url.pathname === "/api/settings/search-key") {
      const b = JSON.parse(r.request().postData());
      posted.push({ asked: b.vendor, ref: b.ref });
      return json({ key: "tvly-dev-0123456789abcd" });
    }
    if (url.pathname === "/api/copy") { posted.push({ copied: JSON.parse(r.request().postData()).text }); return json({}); }
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
  for (const lang of ["en", "zh"]) for (const width of [980, 420]) {
    const w = words[lang];
    test(`${engine} ${lang} ${width}px: search APIs are added and removed in Settings`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 1400 } })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posted = [];
      await page.route("**/*", serve(lang, posted));
      await watchSettingsReads(page);
      await page.goto("http://magpie.test/?view=settings&tab=models");
      await settingsDrawn(page); // rows found are the ones that stay (#1659)
      const list = page.locator("#searchList");
      const head = list.locator(".row.search-add");
      await head.waitFor();
      // Settings is drawn from /api/state and again from /api/settings, so
      // the row found first can be replaced while it is scrolled to
      for (let i = 0; ; i++) {
        try { await head.scrollIntoViewIfNeeded({ timeout: 1000 }); break; } catch (e) { if (i >= 4) throw e; }
      }
      // the clicks below are at the row's buttons' places, so the whole row
      // is brought on screen, by the wheel: magpie puts back a scroll the
      // reader didn't make, and a row only partly shown is left where it is
      // by scrollIntoViewIfNeeded; Settings' own landing can still put the
      // view back, so the row is on screen twice, a second apart
      for (let i = 0, seen = 0; ; i++) {
        const below = await head.evaluate((e) => Math.ceil(e.getBoundingClientRect().bottom - innerHeight + 24));
        if (below <= 0 && ++seen === 2) break;
        assert(i < 12, `the search row comes on screen: ${below}px below`);
        if (below > 0) {
          seen = 0;
          await page.mouse.move(width / 2, 700);
          await page.mouse.wheel(0, below);
        }
        await page.waitForTimeout(below > 0 ? 200 : 1000);
      }
      assert.equal(await list.locator("xpath=preceding-sibling::div[1]").textContent(), w.head);
      assert.equal(await head.locator(".name").innerText(), w.name);
      assert((await head.locator(".sub").innerText()).includes(w.none));
      assert.deepEqual(await list.locator(".row.search-api .name").allInnerTexts(), ["1. Tavily"]);
      // every API's base URL (#1580): under the key, empty for the vendor's own
      const url = head.locator("input.search-url");
      assert.equal(await url.isVisible(), true, "Tavily takes a base URL");
      assert.equal(await url.inputValue(), "");
      assert.equal(await url.getAttribute("placeholder"), w.base);
      assert.equal(await head.locator("input.search-key").getAttribute("placeholder"), w.keep);
      const kb = await head.locator("input.search-key").boundingBox(), ub = await url.boundingBox();
      assert(ub.y >= kb.y + kb.height, `the base URL is under the key: ${JSON.stringify([kb, ub])}`);
      assert(ub.width >= kb.width, "the base URL has the row's width");
      assert(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth), "nothing wider than the window");

      const where = () => page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]);
      const click = async (loc) => {
        const b = await loc.boundingBox();
        await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
      };
      let before = await where();
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
      assert.equal(await head.locator("input.search-url").inputValue(), "", "SearXNG's address isn't Brave's");
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

      // Exa at a base URL of the user's own (#1580): sent with its key
      await click(head.locator("button.search-vendor"));
      await click(page.locator(".proto-menu .pm-item", { hasText: "Exa" }));
      assert.equal(await url.getAttribute("placeholder"), w.exaBase);
      await head.locator("input.search-key").fill("exa-k");
      await url.fill("https://search.example.com/exa");
      await click(head.locator("button.text", { hasText: w.add }));
      await page.waitForFunction(() => document.querySelectorAll("#searchList .row.search-api").length === 3);
      assert.deepEqual(posted.at(-1), { vendor: "exa", key: "exa-k", url: "https://search.example.com/exa" });
      assert((await list.locator(".row.search-api").nth(2).locator(".sub").innerText()).includes("https://search.example.com/exa"));
      // picked again, its base URL is in the field: a new key alone keeps it
      await click(head.locator("button.search-vendor"));
      await click(page.locator(".proto-menu .pm-item", { hasText: "Tavily" }));
      assert.equal(await url.inputValue(), "");
      await click(head.locator("button.search-vendor"));
      await click(page.locator(".proto-menu .pm-item", { hasText: "Exa" }));
      assert.equal(await url.inputValue(), "https://search.example.com/exa");
      await head.locator("input.search-key").fill("exa-2");
      await click(head.locator("button.text", { hasText: w.add }));
      await page.waitForFunction((n) => document.querySelector("#searchList .search-add input.search-key")?.value === "", null);
      assert.deepEqual(posted.at(-1), { vendor: "exa", key: "exa-2", url: "https://search.example.com/exa", append: true });
      // and with no key typed, the address alone changes, the key kept
      await url.fill("https://search2.example.com/exa");
      await click(head.locator("button.text", { hasText: w.add }));
      await page.waitForFunction(() => [...document.querySelectorAll("#searchList .row.search-api .sub")].at(-1)?.innerText.includes("search2.example.com"));
      assert.deepEqual(posted.at(-1), { vendor: "exa", key: "", url: "https://search2.example.com/exa" });
      assert.deepEqual(await list.locator(".row.search-api .name").allInnerTexts(), ["1. Tavily", "2. SearXNG", "3. Exa"]);
      assert.deepEqual(await list.locator('.row.search-keys[data-vendor="exa"] .acc[data-ref] code').allInnerTexts(), ["••••k", "••••2"], "Exa's keys kept, the second after the first");

      // the saved key: shown, hidden, copied; SearXNG's row has none to show
      const tavily = list.locator('.row.search-keys[data-vendor="tavily"] .acc[data-ref="r-abcd"]');
      const code = tavily.locator("code");
      assert.equal(await list.locator(".search-unit").nth(1).locator("button.search-eye").count(), 0);
      assert.equal(await list.locator(".row.search-api").first().locator(".sub").innerText(), "", "the key is in its own row, not the API's");
      assert.equal(await code.innerText(), "tvly…abcd");
      const sent = posted.length;
      // the clicks so far moved nothing; Image generation timeout, drawn
      // above, puts the saved keys past the window's foot at 420, so the
      // reader wheels down to them, and the clicks from there move nothing
      assert.deepEqual(await where(), before, "the clicks moved nothing");
      await reach(page, tavily.locator("button.search-eye"));
      before = await where();
      await click(tavily.locator("button.search-eye"));
      await page.waitForFunction(() => document.querySelector("#searchList .search-key-list code.revealed"));
      assert.equal(await code.innerText(), "tvly-dev-0123456789abcd");
      assert.equal(await tavily.locator("button.search-eye").innerText(), w.hide);
      assert.equal(await code.evaluate((e) => getComputedStyle(e).webkitUserSelect || getComputedStyle(e).userSelect), "text", "the key shown can be selected");
      await click(tavily.locator("button.search-eye"));
      assert.equal(await code.innerText(), "tvly…abcd");
      assert.equal(await tavily.locator("button.search-eye").innerText(), w.show);
      assert.equal(await tavily.locator("button.search-copy").getAttribute("aria-label"), w.copy);
      await click(tavily.locator("button.search-copy"));
      // #status may still hold Brave's refusal, so wait for the copy itself
      for (let i = 0; i < 100 && !posted.some((p) => p.copied); i++) await page.waitForTimeout(50);
      assert.deepEqual(posted.slice(sent), [{ asked: "tavily", ref: "r-abcd" }, { asked: "tavily", ref: "r-abcd" }, { copied: "tvly-dev-0123456789abcd" }]);
      assert.equal(await code.innerText(), "tvly…abcd", "copying shows nothing");
      posted.splice(sent);

      // Remove
      await click(list.locator(".row.search-api").first().locator("button.text", { hasText: w.remove }));
      await page.waitForFunction(() => document.querySelectorAll("#searchList .row.search-api").length === 2);
      assert.deepEqual(posted.at(-1), { vendor: "tavily", remove: true });
      assert.deepEqual(await list.locator(".row.search-api .name").allInnerTexts(), ["1. SearXNG", "2. Exa"]);

      // You.com (#1626): its key is optional, so Add sends it with none
      await click(head.locator("button.search-vendor"));
      await click(page.locator(".proto-menu .pm-item", { hasText: "You.com" }));
      assert.equal(await head.locator("input.search-key").getAttribute("placeholder"), w.youKey);
      await click(head.locator("button.text", { hasText: w.add }));
      await page.waitForFunction(() => document.querySelectorAll("#searchList .row.search-api").length === 3);
      assert.deepEqual(posted.at(-1), { vendor: "you", key: "", url: "" });
      assert.deepEqual(await list.locator(".row.search-api .name").allInnerTexts(), ["1. SearXNG", "2. Exa", "3. You.com"]);
      assert.deepEqual(await where(), before, "the clicks moved nothing");
      assert.deepEqual(errors, []);
    });
  }
}
