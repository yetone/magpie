// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings' Web search section, a search API's keys as a list (辰林鸭~ on
// Discord: "only one Tavily key can be entered"): each key on a row of its
// own, masked, with its tick, Show, Copy and Remove; a key switched off
// stays, dimmed, and says so; a key refused lately says why and until when
// it is asked last. Several are added at once, pasted split by commas or
// lines, and the list is put in order with Alt+↑/↓. Every change is sent
// by the key's ref, never the key. In five languages, Chromium and WebKit,
// 1100px and 420px wide, with the API faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, reach } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const I18N = (() => {
  const src = require("node:fs").readFileSync(path.join(assets, "i18n.js"), "utf8");
  return new Function(src.slice(0, src.indexOf("\n};") + 3) + "; return I18N;")();
})();
const tr = (lang, s, vars = {}) => {
  const got = lang === "en" ? s : I18N[lang][s];
  assert(got, `"${s}" has no ${lang} translation`);
  return got.replace(/\{(\w+)\}/g, (_, k) => String(vars[k]));
};

const vendors = [{ id: "tavily", name: "Tavily", keysURL: "https://app.tavily.com/home", base: "https://api.tavily.com" }];
const restUntil = new Date(Date.now() + 40 * 60 * 1000).toISOString();
const mask = (k) => "tvly-…" + k.slice(-4);
const refOf = (k) => "ref-" + k.slice(-4);

function serve(lang, posted) {
  // as the real list: keys in order, each on or off; the rest is the
  // gateway's, for a key on
  let list = [
    { key: "tvly-dev-first-1111" },
    { key: "tvly-dev-limit-2222", rest: "rate limited" },
    { key: "tvly-dev-spare-3333", off: true },
  ];
  const settings = () => ({
    lang, theme: "light", searchVendors: vendors,
    searchAPIs: [{
      vendor: "tavily", name: "Tavily", ready: true, keys: list.filter((k) => !k.off).length,
      key: list.filter((k) => !k.off).map((k) => mask(k.key)).join(", "),
      keyList: list.map((k) => ({ ref: refOf(k.key), masked: mask(k.key), off: k.off || undefined, ...(k.rest && !k.off ? { rest: k.rest, restUntil } : {}) })),
    }],
  });
  const at = (ref) => list.findIndex((k) => refOf(k.key) === ref);
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data, status = 200) => r.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings() });
    if (url.pathname === "/api/settings") return json(settings());
    if (url.pathname === "/api/settings/search-keys") {
      const b = JSON.parse(r.request().postData());
      posted.push(b);
      let added = 0, had = 0;
      if (b.arrange) list = b.arrange.map((ref) => list[at(ref)]);
      else if (b.add) {
        for (const k of b.add.split(/[\s,;]+/).filter(Boolean)) {
          if (list.some((x) => x.key === k)) had++;
          else { list.push({ key: k }); added++; }
        }
      } else if (b.remove) list.splice(at(b.ref), 1);
      else if ("on" in b) list[at(b.ref)] = { ...list[at(b.ref)], off: !b.on };
      return json({ settings: settings(), added, had });
    }
    if (url.pathname === "/api/settings/search-key") {
      const b = JSON.parse(r.request().postData());
      posted.push({ asked: b });
      return json({ key: list[at(b.ref)].key });
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
  for (const lang of LANGS) for (const width of [1100, 420]) {
    test(`${engine} ${lang} ${width}px: a search API's keys are a list, each switched, shown, removed and moved`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 1400 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posted = [];
      await page.route("**/*", serve(lang, posted));
      await watchSettingsReads(page);
      await page.goto("http://magpie.test/?view=settings&tab=models");
      await settingsDrawn(page);
      const keys = page.locator('#searchList .row.search-keys[data-vendor="tavily"]');
      await keys.waitFor();
      const rows = keys.locator(".search-key-list .acc[data-ref]");
      const codes = () => rows.locator("code").allInnerTexts();
      assert.deepEqual(await codes(), ["tvly-…1111", "tvly-…2222", "tvly-…3333"]);
      // nothing in the page holds a whole key
      assert.equal(await page.evaluate(() => document.body.innerHTML.includes("tvly-dev-")), false);
      assert.deepEqual(await rows.evaluateAll((rs) => rs.map((r) => r.className.includes("off") ? "off" : "on")), ["on", "on", "off"]);
      assert.deepEqual(await rows.locator("button.tick").evaluateAll((bs) => bs.map((b) => b.getAttribute("aria-pressed"))), ["true", "true", "false"]);
      assert.equal(await rows.nth(0).locator("button.tick").getAttribute("aria-label"), tr(lang, "Stop using this key"));
      assert.equal(await rows.nth(2).locator("button.tick").getAttribute("aria-label"), tr(lang, "Use this key too"));
      // why a key is asked last, and until when; one off says Off
      const until = await page.evaluate((u) => hhmm(u), restUntil);
      assert.equal(await rows.nth(1).locator(".key-rest").innerText(), tr(lang, "asked last until {time}: {why}", { time: until, why: tr(lang, "rate limited") }));
      assert.equal(await rows.locator(".key-rest").count(), 1);
      assert.equal(await rows.nth(2).locator(".key-state").innerText(), tr(lang, "Off"));
      assert(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth), "nothing wider than the window");
      const past = await keys.evaluate((r) => [...r.querySelectorAll("button, code, .key-rest")]
        .filter((e) => e.getBoundingClientRect().right > innerWidth + 0.5).map((e) => e.className + " " + e.textContent));
      assert.deepEqual(past, [], "nothing past the window's edge");

      const where = () => page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop, document.querySelector("#view-settings")?.scrollTop]);
      // reached as a reader does (the wheel), then clicked where it is:
      // the click itself never moves the page. Settings is drawn again
      // after each change, so the box is taken from the row that stays.
      const stays = [];
      const click = async (loc) => {
        await reach(page, loc);
        const b = await loc.boundingBox();
        assert(b, "the button is in the page");
        const at = await where();
        await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
        stays.push(async () => assert.deepEqual(await where(), at, "a click doesn't scroll"));
      };
      const drawn = (want) => page.waitForFunction((w) => [...document.querySelectorAll('#searchList .search-keys[data-vendor="tavily"] .acc[data-ref] code')].map((c) => c.textContent).join() === w, want.join());
      const settle = async () => { for (const f of stays.splice(0)) await f(); };

      // Show reads that one key, by its ref; Hide masks it again
      await click(rows.nth(2).locator("button.search-eye"));
      await page.waitForFunction(() => document.querySelector("#searchList .search-key-list code.revealed"));
      assert.equal(await rows.nth(2).locator("code").innerText(), "tvly-dev-spare-3333");
      assert.equal(await rows.nth(2).locator("button.search-eye").innerText(), tr(lang, "Hide"));
      await click(rows.nth(2).locator("button.search-eye"));
      assert.equal(await rows.nth(2).locator("code").innerText(), "tvly-…3333");
      await settle();
      await click(rows.nth(0).locator("button.search-copy"));
      for (let i = 0; i < 100 && !posted.some((p) => p.copied); i++) await page.waitForTimeout(50);
      assert.deepEqual(posted.splice(0), [{ asked: { vendor: "tavily", ref: "ref-3333" } }, { asked: { vendor: "tavily", ref: "ref-1111" } }, { copied: "tvly-dev-first-1111" }]);

      // switched on, then the first off
      await click(rows.nth(2).locator("button.tick"));
      await page.waitForFunction(() => !document.querySelector('#searchList .search-keys[data-vendor="tavily"] .acc.off'));
      await click(rows.nth(0).locator("button.tick"));
      await page.waitForFunction(() => document.querySelector('#searchList .search-keys[data-vendor="tavily"] .acc[data-ref="ref-1111"]')?.classList.contains("off"));
      assert.deepEqual(posted.splice(0), [{ vendor: "tavily", ref: "ref-3333", on: true }, { vendor: "tavily", ref: "ref-1111", on: false }]);
      await settle();

      // several pasted at once, one already there: said how many
      await click(keys.locator(".search-key-list .acc.add"));
      const field = keys.locator("input.search-key-new");
      await field.waitFor();
      await settle();
      assert.equal(await field.getAttribute("type"), "password", "a key typed is masked");
      assert.equal(await field.getAttribute("placeholder"), tr(lang, "Paste a key, or several split by commas or lines"));
      await field.fill("tvly-dev-new-4444, tvly-dev-first-1111, tvly-dev-more-5555");
      await field.press("Enter");
      await drawn(["tvly-…1111", "tvly-…2222", "tvly-…3333", "tvly-…4444", "tvly-…5555"]);
      assert.deepEqual(posted.splice(0), [{ vendor: "tavily", add: "tvly-dev-new-4444, tvly-dev-first-1111, tvly-dev-more-5555" }]);
      await page.waitForFunction((w) => document.querySelector("#status")?.textContent === w, tr(lang, "{n} keys added, {m} already there", { n: 2, m: 1 }));

      // Alt+↓ on the second puts it third: the order is sent as refs
      await rows.nth(1).focus();
      await page.keyboard.press("Alt+ArrowDown");
      await drawn(["tvly-…1111", "tvly-…3333", "tvly-…2222", "tvly-…4444", "tvly-…5555"]);
      assert.deepEqual(posted.splice(0), [{ vendor: "tavily", arrange: ["ref-1111", "ref-3333", "ref-2222", "ref-4444", "ref-5555"] }]);
      await page.waitForFunction(() => document.activeElement?.dataset?.ref === "ref-2222");

      // Remove
      await click(rows.nth(3).locator("button.search-key-remove"));
      await drawn(["tvly-…1111", "tvly-…3333", "tvly-…2222", "tvly-…5555"]);
      assert.deepEqual(posted.splice(0), [{ vendor: "tavily", ref: "ref-4444", remove: true }]);
      assert.equal(await rows.nth(0).locator("button.search-key-remove").innerText(), tr(lang, "Remove"));

      await settle();
      assert(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth), "nothing wider than the window");
      assert.equal(await page.evaluate(() => document.body.innerHTML.includes("tvly-dev-")), false, "no key left in the page");
      assert.deepEqual(errors, []);
    });
  }
}
