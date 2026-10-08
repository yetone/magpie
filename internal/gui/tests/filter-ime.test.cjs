// Run with Node's test runner and Playwright on the module path; see README.md.
// #1055: a digit typed into the Routing page's group filter went in twice
// (1, 2 → 1122). The filter's own input event drew its row again with
// replaceChildren, which took the focused field out of the page and put it
// back; an IME committing its text while the field was out wrote it twice.
// The provider editor's keys filter and the plugins search were made the
// same way. Each is typed into here, through an IME in Chromium (CDP's
// imeSetComposition, then insertText) and by key in both engines, and the
// field must keep one character a key, its focus and its caret, and never
// leave the page. The row around it still follows the filter and the
// language. In English, Chinese, Japanese and German, wide and at 440px.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = process.env.MAGPIE_FILTER_ASSETS || path.resolve(__dirname, "../assets");

const models = [
  { id: "a/one", name: "one", providerName: "A", icon: "generic" },
  { id: "a/two", name: "two", providerName: "A", icon: "generic" },
  { id: "a/m12", name: "m12", providerName: "A", icon: "generic" },
];
const groups = [
  { id: "g", name: "G", members: ["a/one"], off: [], routing: "order", ready: true, memberInfo: [{ id: "a/one", ready: true }] },
  { id: "o", name: "O", members: ["a/two"], off: [], routing: "order", ready: true, memberInfo: [{ id: "a/two", ready: true }] },
  { id: "v12", name: "v12", members: ["a/m12"], off: [], routing: "order", ready: true, memberInfo: [{ id: "a/m12", ready: true }] },
];
const N = 300;
const idOf = (i) => i.toString(16).padStart(10, "0");
const relay = () => ({
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "big", name: "", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…0000" }, balanceToken: { takes: false, set: false }, proxy: "",
  keyList: Array.from({ length: N }, (_, i) => ({ id: idOf(i), name: "", masked: "sk-…" + String(i).padStart(4, "0"), on: true, active: i === 0 })),
});
const listings = [
  { package: "@magpie-community/opencode-zed-auth", name: "Zed", icon: "zed", providers: ["zed"], community: true,
    summary: { en: "Your Zed plan.", zh: "Zed 订阅。" }, npm: { version: "0.1.0", weekly: 120 } },
  { package: "opencode-gemini-auth", name: "Gemini", icon: "gemini-color", providers: ["google"],
    summary: { en: "Gemini with a Google account.", zh: "用 Google 账号使用 Gemini。" }, npm: { version: "1.4.0", weekly: 9100 } },
];

// serve answers the page from fixtures; held, when given, keeps the plugin
// listings back until it is resolved, as a slow npm mirror would
function serve(lang, held) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname.startsWith("/api/groups")) return json({ models, pools: [], groups });
    if (url.pathname === "/api/providers") return json({ providers: [relay()], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/plugins") return json({ bun: true, bunVersion: "1.3.0", plugins: [{ spec: "opencode-gemini-auth@1.4.0", providers: ["Gemini"], version: "1.4.0" }] });
    if (url.pathname === "/api/plugins/listings") {
      if (held) await held;
      return json({ listings });
    }
    if (url.pathname === "/api/plugins/search") return json({ hits: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// watch counts each time the field, or a box it sits in, is taken out of
// the page from now on
const watch = (page, sel) => page.evaluate((sel) => {
  const q = document.querySelector(sel);
  window.__field = q;
  window.__out = 0;
  new MutationObserver((recs) => {
    for (const r of recs) for (const n of r.removedNodes) if (n === q || n.contains?.(q)) window.__out++;
  }).observe(document.body, { childList: true, subtree: true });
}, sel);
const field = (page) => page.evaluate(() => {
  const q = window.__field, now = document.activeElement;
  return { out: window.__out, same: now === q, value: q.value, caret: [q.selectionStart, q.selectionEnd], connected: q.isConnected };
});

// ime types each character the way an input method commits it: a
// composition, then its text (Chromium only, through CDP)
async function ime(page, text) {
  const c = await page.context().newCDPSession(page);
  for (const ch of text) {
    await c.send("Input.imeSetComposition", { text: ch, selectionStart: 1, selectionEnd: 1 });
    await c.send("Input.insertText", { text: ch });
    await page.waitForTimeout(30);
  }
  await c.detach();
}

// other: the language the routing header is switched to, and its words
const words = {
  en: { label: "Routing groups", installed: "Installed", of: "1 of 300 keys", other: ["zh", "路由组", "筛选路由组和模型"] },
  zh: { label: "路由组", installed: "已安装", of: "300 个密钥中的 1 个", other: ["en", "Routing groups", "Filter groups and models"] },
  ja: { label: "ルーティンググループ", installed: "インストール済み", of: "300 個中 1 個のキー", other: ["en", "Routing groups", "Filter groups and models"] },
  de: { label: "Weiterleitungsgruppen", installed: "Installiert", of: "1 von 300 Schlüsseln", other: ["en", "Routing groups", "Filter groups and models"] },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    for (const width of [1100, 440]) {
      const w = words[lang];
      const open = async (t, view, held) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, held));
        await page.goto("http://magpie.test/?view=" + view);
        return { page, errors };
      };
      // typeIn: through an IME where there is one to drive, by key elsewhere
      const typeIn = (page, text) => engine === "chromium" ? ime(page, text) : page.keyboard.type(text);

      test(`${engine} ${lang} ${width}px: the routing group filter takes one character a key`, async (t) => {
        const { page, errors } = await open(t, "routing");
        const q = page.locator("input.rt-gfilter");
        await q.waitFor();
        await watch(page, "input.rt-gfilter");
        await q.focus();
        await typeIn(page, "12");
        assert.deepEqual(await field(page), { out: 0, same: true, value: "12", caret: [2, 2], connected: true }, "the filter after 1, 2");
        // the groups follow it: only v12 has a 12 in it
        await page.waitForFunction(() => [...document.querySelectorAll(".rt-groups .rt-group")].map((r) => r.dataset.id).join() === "v12");
        assert.equal(await page.locator(".rt-gsec .row-head").first().locator(".label").textContent(), w.label);
        // a key in the middle goes where the caret is
        await page.keyboard.press("ArrowLeft");
        await typeIn(page, "5");
        assert.deepEqual(await field(page), { out: 0, same: true, value: "152", caret: [2, 2], connected: true }, "the filter after ←, 5");
        await page.keyboard.press("Backspace");
        await page.keyboard.press("ArrowRight");
        await page.keyboard.press("Backspace");
        assert.deepEqual(await field(page), { out: 0, same: true, value: "1", caret: [1, 1], connected: true }, "the filter after two Backspaces");
        // the row around it is still drawn again: in the other language
        // its words change, and the filter stays where it was
        await page.evaluate((l) => setLocale(l), w.other[0]);
        await page.waitForFunction((s) => document.querySelector(".rt-gsec .row-head .label")?.textContent === s, w.other[1]);
        assert.equal(await q.getAttribute("placeholder"), w.other[2]);
        const f = await field(page);
        assert.equal(f.out, 0, "switching the language took the filter out");
        assert.equal(f.value, "1");
        // Select takes the buttons beside it away and leaves the filter
        await q.fill("");
        await page.locator(".rt-gselect").click();
        await page.locator(".rt-gsel").waitFor();
        assert.equal(await page.locator(".rt-gsec .row-head .rt-gselect, .rt-gsec .row-head .rt-gnew").count(), 0);
        assert.equal(await page.locator(".rt-gsec .row-head input.rt-gfilter").count(), 1);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: the provider keys filter takes one character a key`, async (t) => {
        const { page, errors } = await open(t, "providers");
        await page.locator(".row.provider", { hasText: "Relay" }).first().click();
        await page.locator(".editor .keys-more").click();
        const q = page.locator(".keys-tools .keys-filter");
        await q.waitFor();
        await watch(page, ".keys-tools .keys-filter");
        await q.focus();
        await typeIn(page, "12");
        assert.deepEqual(await field(page), { out: 0, same: true, value: "12", caret: [2, 2], connected: true }, "the filter after 1, 2");
        await typeIn(page, "9");
        assert.deepEqual(await field(page), { out: 0, same: true, value: "129", caret: [3, 3], connected: true }, "the filter after 9");
        // the keys, and the count beside the filter, follow it: only sk-…0129
        await page.waitForFunction(() => document.querySelectorAll(".editor .accts .acc[data-account-id]").length === 1);
        assert.equal((await page.locator(".keys-count").textContent()).trim(), w.of);
        assert.equal(await page.locator(".keys-tools").count(), 1);
        await page.keyboard.press("Escape");
        assert.deepEqual(await field(page), { out: 0, same: true, value: "", caret: [0, 0], connected: true }, "Escape empties the filter");
        await page.waitForFunction(() => document.querySelectorAll(".editor .accts .acc[data-account-id]").length === 50);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: the plugins search takes one character a key`, async (t) => {
        let release;
        const held = new Promise((r) => { release = r; });
        const { page, errors } = await open(t, "plugins", held);
        const q = page.locator(".pm-find input");
        await q.waitFor();
        // from Installed, the first key goes to Discover
        await page.locator(".lib-tabs .opt", { hasText: w.installed }).click();
        await watch(page, ".pm-find input");
        await q.focus();
        await typeIn(page, "1");
        assert.deepEqual(await field(page), { out: 0, same: true, value: "1", caret: [1, 1], connected: true }, "the search after 1 from Installed");
        // the listings come while the reader types
        release();
        await page.locator(".pm-sec").first().waitFor();
        await typeIn(page, "2");
        assert.deepEqual(await field(page), { out: 0, same: true, value: "12", caret: [2, 2], connected: true }, "the search after the listings came, and 2");
        await page.keyboard.press("Escape");
        assert.equal(await q.inputValue(), "");
        assert.equal((await field(page)).out, 0);
        assert.deepEqual(errors, []);
      });
    }
  }
}
