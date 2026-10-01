// Run with Node's test runner and Playwright on the module path; see README.md.
// Providers switched off are kept apart (01huadalang on Discord: 已关闭的
// 供应商还占着位置): the list holds the ones on, and the ones off sit below
// it under a "Turned off (N)" fold, folded at first. The fold opens and
// closes on a click that leaves the page where it is, and is remembered
// across a reload; a provider switched off moves under it. In English and
// Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const provider = (i, off) => ({
  id: "p" + i, name: "Provider " + i, icon: "generic", chat: "https://p" + i + ".example.com/v1", responses: "", anthropic: "", catalog: "",
  host: "p" + i + ".example.com", models: [{ id: "model-" + i, name: "Model " + i, on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…" + i }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", off,
});
const OFF = new Set([1, 5, 9]);

function serve(lang) {
  const list = Array.from({ length: 12 }, (_, i) => provider(i, OFF.has(i)));
  const providers = () => ({ providers: list, presets: [], excluded: [], gateway: { running: true, window: true } });
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers());
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/provider/off" || url.pathname === "/api/provider/on") {
      const { id } = route.request().postDataJSON();
      for (const p of list) if (p.id === id) p.off = url.pathname.endsWith("/off");
      return json(providers());
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = { en: "Turned off", zh: "已关闭的供应商" };
const top = (page, sel) => page.locator(sel).evaluate((e) => e.getBoundingClientRect().top);
const ids = (page, sel) => page.locator(sel + " > .row.provider").evaluateAll((rs) => rs.map((r) => r.dataset.id));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: providers switched off fold away below the ones on`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 560 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-providers-off-fold.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));

      await page.goto("http://magpie.test/?view=providers");
      await page.locator("#providers .row.provider").first().waitFor();
      // the list holds the ones on; the ones off are under the fold, folded
      assert.deepEqual(await ids(page, "#providers"), ["p0", "p2", "p3", "p4", "p6", "p7", "p8", "p10", "p11"]);
      const fold = page.locator("#foldOff");
      assert(await fold.isVisible(), "the fold shows");
      assert.equal((await fold.locator(".label").textContent()).trim(), words[lang]);
      assert.equal((await page.locator("#offCount").textContent()).trim(), "3");
      assert.equal(await fold.getAttribute("aria-expanded"), "false", "folded at first");
      assert(await page.locator("#offProviders").evaluate((l) => l.hidden), "folded, the ones off are hidden");
      const listBottom = await page.locator("#providers").evaluate((e) => e.getBoundingClientRect().bottom);
      assert(await top(page, "#offHead") >= listBottom, "the fold is below the list");

      // opened with the page scrolled to it, the head stays where it is
      const view = page.locator("#view-providers");
      await page.mouse.move(450, 200);
      await page.mouse.wheel(0, 400);
      await page.waitForTimeout(400);
      const scrolled = await view.evaluate((v) => v.scrollTop);
      assert(scrolled > 20, "the page scrolled to the fold");
      const was = await top(page, "#foldOff");
      await fold.click();
      await page.waitForTimeout(400);
      assert.equal(await fold.getAttribute("aria-expanded"), "true");
      assert.deepEqual(await ids(page, "#offProviders"), ["p1", "p5", "p9"]);
      assert(await page.locator("#offProviders").isVisible());
      assert(Math.abs(await top(page, "#foldOff") - was) <= 1, "opening moved the head");
      assert(Math.abs(await view.evaluate((v) => v.scrollTop) - scrolled) <= 1, "opening scrolled the page");

      // remembered across a reload
      await page.reload();
      await page.locator("#providers .row.provider").first().waitFor();
      assert.equal(await page.locator("#foldOff").getAttribute("aria-expanded"), "true", "the fold is remembered");
      assert.deepEqual(await ids(page, "#offProviders"), ["p1", "p5", "p9"]);

      // a provider switched off moves under it
      await page.locator('#providers .row.provider[data-id="p3"] .pswitch').click();
      await page.waitForFunction(() => document.querySelector('#offProviders .row.provider[data-id="p3"]'));
      assert.equal((await page.locator("#offCount").textContent()).trim(), "4");
      assert.equal(await page.locator('#providers .row.provider[data-id="p3"]').count(), 0);

      // and folds again
      await page.mouse.move(450, 200);
      await page.mouse.wheel(0, 200);
      await page.waitForTimeout(400);
      await page.locator("#foldOff").click();
      await page.waitForTimeout(300);
      assert.equal(await page.locator("#foldOff").getAttribute("aria-expanded"), "false");
      assert(await page.locator("#offProviders").evaluate((l) => l.hidden));
      assert.deepEqual(errors, []);
    });
  }
}
