// Run with Node's test runner and Playwright on the module path; see README.md.
// soilzhu80 on X: a relay's Model settings, hundreds of models, was a small
// box to scroll with no way to find one. A list of more than five now has a
// search field in its head, matching a row's ids and names, and an Expand
// that lets the list be as tall as its rows while the editor scrolls, its
// head (search, Collapse, Fold) held in sight. Escape clears the search
// before it closes anything; a list of five has no search. Narrow window,
// both engines, English, Chinese, Japanese and German.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: { names: "Model settings", expand: "Expand the list", collapse: "Collapse the list", none: "No model matches “zzz”" },
  zh: { names: "模型设置", expand: "展开列表", collapse: "收起列表", none: "没有匹配“zzz”的模型" },
  ja: { names: "モデル設定", expand: "リストを広げる", collapse: "リストを畳む", none: "「zzz」に一致するモデルはありません" },
  de: { names: "Modelleinstellungen", expand: "Liste aufklappen", collapse: "Liste einklappen", none: "Kein Modell passt zu „zzz“" },
};

function serve(lang, posts) {
  let cur = { lang, theme: "light", plainNames: false, plainOwnNames: false };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const models = Array.from({ length: 300 }, (_, i) => ({ id: `sol-${i}-preview`, name: `Sol ${i}`, on: true, efforts: [], images: false }));
  const small = { id: "small", name: "Small", icon: "generic", chat: "https://small.example/v1", responses: "", anthropic: "", models: models.slice(0, 5), agents: [], key: { set: true, masked: "sk-…5678" }, ready: true };
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
    if (url.pathname === "/api/providers") return json({ providers: [provider, small], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

const shown = (page) => page.locator(".mnames:not([hidden]) > .mname:not([hidden])");

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a long Model settings list is searched and expanded`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 420, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(8000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator('.row.provider[data-id="relay"]').click();
      await page.locator(".mfoot button", { hasText: w.names }).click();
      const list = page.locator(".mnames:not([hidden])");
      const find = list.locator(".mnhead input.mfind");
      await find.waitFor();
      assert.equal(await shown(page).count(), 300);
      assert.equal(await find.getAttribute("type"), "search");
      // the head fits the narrow window
      const hb = await list.locator(".mnhead").evaluate((h) => { const r = h.getBoundingClientRect(); return [r.right, h.scrollWidth - h.clientWidth]; });
      assert.ok(hb[0] <= 420 && hb[1] <= 1, `the head overflows: ${hb}`);

      // by id
      await find.fill("sol-123-");
      assert.equal(await shown(page).count(), 1);
      assert.equal(await shown(page).locator(".mwho code").textContent(), "sol-123-preview");
      assert.ok(await find.evaluate((i) => document.activeElement === i), "typing keeps the field focused");
      // by its own name, every word, each anywhere in it
      await find.fill("SOL 27");
      assert.deepEqual(await shown(page).locator(".mwho code").allTextContents(), ["sol-27-preview", "sol-127-preview", "sol-227-preview", "sol-270-preview", "sol-271-preview", "sol-272-preview", "sol-273-preview", "sol-274-preview", "sol-275-preview", "sol-276-preview", "sol-277-preview", "sol-278-preview", "sol-279-preview"]);
      // nothing matches: it says so
      await find.fill("zzz");
      assert.equal(await shown(page).count(), 0);
      assert.equal((await list.locator(".mnone").textContent()).trim(), w.none);
      assert.ok(await list.locator(".mnone").isVisible());
      // Escape clears it, and the editor stays open
      await find.press("Escape");
      assert.equal(await find.inputValue(), "");
      assert.equal(await shown(page).count(), 300);
      assert.ok(await page.locator("#modal").isVisible(), "Escape closed the editor while the search had text");
      assert.ok(!(await list.locator(".mnone").isVisible()));

      // Expand: as tall as its rows, the editor scrolls, the head stays in sight
      const big = list.locator(".mnhead button.mbig");
      assert.equal((await big.textContent()).trim(), w.expand);
      const small = await list.evaluate((n) => n.clientHeight);
      assert.ok(small <= 242, `folded it is a small box: ${small}`);
      const page0 = await page.evaluate(() => scrollY);
      await big.click();
      assert.equal((await big.textContent()).trim(), w.collapse);
      const [tall, own] = await list.evaluate((n) => [n.clientHeight, n.scrollHeight - n.clientHeight]);
      assert.ok(tall > 3000 && own <= 1, `expanded it is as tall as its rows: ${tall}, ${own}`);
      assert.equal(await page.evaluate(() => scrollY), page0, "the click scrolled the page");
      const body = await list.evaluate((n) => { let p = n.parentElement; while (p && !(p.scrollHeight > p.clientHeight + 1 && /auto|scroll/.test(getComputedStyle(p).overflowY))) p = p.parentElement; return !!p; });
      assert.ok(body, "nothing scrolls the expanded list");
      await list.evaluate((n) => { let p = n.parentElement; while (!(p.scrollHeight > p.clientHeight + 1 && /auto|scroll/.test(getComputedStyle(p).overflowY))) p = p.parentElement; p.scrollTop += 2000; });
      const inView = await list.locator(".mnhead").evaluate((h) => { const r = h.getBoundingClientRect(); return r.top >= 0 && r.bottom <= innerHeight; });
      assert.ok(inView, "the head scrolled out of sight");
      await find.fill("sol-299");
      assert.equal(await shown(page).count(), 1);
      await big.click();
      assert.equal((await big.textContent()).trim(), w.expand);
      assert.ok((await list.evaluate((n) => n.clientHeight)) <= 242);
      await find.fill("");

      // a list of five has no search
      await page.keyboard.press("Escape");
      await page.locator("#modal").waitFor({ state: "hidden" });
      await page.locator('.row.provider[data-id="small"]').click();
      await page.locator(".mfoot button", { hasText: w.names }).click();
      await page.locator(".mnames:not([hidden]) .mname").first().waitFor();
      assert.equal(await page.locator(".mnames:not([hidden]) .mfind").count(), 0);
      assert.equal(await page.locator("#modal select").count(), 0);
      assert.deepEqual(errors, []);
    });
  }
}
