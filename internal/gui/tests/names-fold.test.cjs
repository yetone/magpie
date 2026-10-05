// Run with Node's test runner and Playwright on the module path; see README.md.
// Hu9956, #868: once Names & levels was open (to set Provider in model
// names), it couldn't be closed — the only way was the button under the
// list, out of sight — and it stayed open in every editor after, until
// magpie was restarted. Now the open list has its own quiet title and a
// Fold at its top, held there while the rows scroll; a click there folds it
// and the button under the list reads as off again; and an editor opened
// afresh has it folded. No click moves the page, nothing native, no
// coloured left border. English, Chinese, Japanese and German.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: { names: "Names & levels", fold: "Fold", own: "Not on names I set", cancel: "Cancel" },
  zh: { names: "名称与推理档位", fold: "收起", own: "自定义名称不带供应商", cancel: "取消" },
  ja: { names: "名前と推論レベル", fold: "折りたたむ" },
  de: { names: "Namen & Stufen", fold: "Einklappen" },
};

function serve(lang, posts) {
  let cur = { lang, theme: "light", plainNames: false, plainOwnNames: false };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const models = Array.from({ length: 8 }, (_, i) => ({ id: `sol-${i}-preview-long`, name: `Sol ${i}`, on: true, efforts: [], images: false }));
  const provider = { id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example/v1", responses: "", anthropic: "", models, agents: [], key: { set: true, masked: "sk-…1234" }, ready: true };
  return async (r) => {
    const req = r.request(), url = new URL(req.url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/settings/plain-names") {
      const body = req.postDataJSON();
      posts.push(body);
      cur = { ...cur, plainNames: body.mode === "off", plainOwnNames: body.mode === "own" };
      return json(cur);
    }
    if (url.pathname === "/api/settings") return json(cur);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ models: [], pools: [], groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [provider], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

const scrolls = (page) => page.evaluate(() => [scrollY, ...[...document.querySelectorAll(".view, .ebody")].map((v) => v.scrollTop)]);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    const w = words[lang];
    test(`${engine} ${lang}: Names & levels folds where it opened, and opens folded`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 900 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=providers");
      const relay = page.locator('.row.provider[data-id="relay"]');
      await relay.click();
      const rename = page.locator(".mfoot button", { hasText: w.names });
      await rename.waitFor();
      const open = page.locator(".mnames:not([hidden])");
      assert.equal(await open.count(), 0, "it opens folded");

      await rename.click();
      const head = open.locator(".mnhead");
      await head.waitFor();
      assert.equal((await head.locator("b").textContent()).trim(), w.names);
      const fold = head.locator("button");
      assert.equal((await fold.textContent()).trim(), w.fold);
      // the Fold stays at the top while the rows scroll
      await open.evaluate((n) => { n.scrollTop = n.scrollHeight; });
      const [nTop, hTop] = await Promise.all([open.evaluate((n) => n.getBoundingClientRect().top), head.evaluate((h) => h.getBoundingClientRect().top)]);
      assert.ok(Math.abs(hTop - nTop) <= 2, `the head scrolled away: ${hTop} vs ${nTop}`);
      if (w.own) {
        // picking Provider in model names leaves it open, and foldable
        await open.locator(".msuffix .opt", { hasText: w.own }).click();
        await page.locator(".mnames:not([hidden]) .msuffix .opt.on", { hasText: w.own }).waitFor();
        assert.deepEqual(posts, [{ mode: "own" }]);
      }
      const before = await scrolls(page);
      await page.locator(".mnames:not([hidden]) .mnhead button").click();
      await open.waitFor({ state: "detached" }).catch(() => {});
      assert.equal(await open.count(), 0, "Fold folds it");
      assert.equal(await page.locator(".mfoot button.on", { hasText: w.names }).count(), 0, "the button under the list reads as off");
      assert.deepEqual(await scrolls(page), before, "the click scrolled the page");

      // open again, then close the editor: the next one opens folded
      await page.locator(".mfoot button", { hasText: w.names }).click();
      await page.locator(".mnames:not([hidden])").waitFor();
      assert.equal(await page.locator(".mnames:not([hidden])").evaluate((e) => getComputedStyle(e).borderLeftStyle), "none");
      await page.keyboard.press("Escape");
      await page.locator("#modal").waitFor({ state: "hidden" });
      await relay.click();
      await page.locator(".mfoot button", { hasText: w.names }).waitFor();
      assert.equal(await page.locator(".mnames:not([hidden])").count(), 0, "a fresh editor opens it folded");
      assert.equal(await page.locator("#modal select").count(), 0);
      assert.deepEqual(errors, []);
    });
  }
}
