// Run with Node's test runner and Playwright on the module path; see README.md.
// A page's tabs listed in the sidebar fold the way a Mac sidebar's do
// (hkwok06 on X): Settings opened, its tabs unrolled over it, and nothing
// said they would fold again; only another page, the gateway, put them
// away. The row whose list shows has a disclosure chevron, and the row
// clicked again folds the list and unfolds it, the page staying open.
// The same holds for every page with a list (Usage here), and a list
// folded stays folded when its page is opened again.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets"));

function serve(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:false,sideGateway:{url:"http://127.0.0.1:3425",running:true,boot:true}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { url: "http://127.0.0.1:3425", running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const langs = ["en", "zh", "zh-TW", "ja", "de"];
const open = (page) => page.locator("#side .nav-sub:not(.leaving) .nav-s");
const folded = (page) => page.waitForFunction(() => !document.querySelector("#side .nav-sub"));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a page's tabs in the sidebar fold from its own row`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of langs) await t.test(lang, async () => {
      const context = await browser.newContext({ viewport: { width: 1200, height: 760 } });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/");
      await page.waitForSelector("#nav button.on");
      assert.ok(await page.evaluate(() => document.body.classList.contains("side-nav")), "the sidebar shows");
      const prefs = page.locator("#side .side-foot #prefs");
      const chev = prefs.locator(".nav-fold");
      // no list, no chevron
      assert.equal(await chev.isVisible().catch(() => false), false, "a row with no list open has no chevron");

      await prefs.click();
      await open(page).first().waitFor();
      assert.equal(await open(page).count(), 8, "Settings' tabs are listed");
      // the list is drawn first and again with its chevron once navicons.js
      // is in (app.js navSubs), so the chevron is waited for, not read at once
      await chev.waitFor({ timeout: 10000 }).catch(() => {});
      assert.ok(await chev.isVisible(), "Settings' row shows a disclosure chevron");
      assert.equal(await prefs.getAttribute("aria-expanded"), "true");
      // a tab picked, so the page has something to keep
      await page.locator(".nav-s[data-of=setTab-network]").click();
      await page.waitForFunction(() => document.querySelector("#setTab-network").classList.contains("on"));

      // Settings' row clicked again folds its list; Settings stays open on its tab
      await prefs.click();
      await folded(page);
      assert.equal(await prefs.getAttribute("aria-expanded"), "false");
      assert.ok(await chev.isVisible(), "the chevron stays, to unfold it again");
      assert.ok(await prefs.evaluate((b) => b.classList.contains("on")), "Settings is still the page open");
      assert.ok(await page.locator("#view-settings").isVisible());
      assert.ok(await page.evaluate(() => document.querySelector("#setTab-network").classList.contains("on")), "on the tab it was on");
      const [a, b] = await page.evaluate(() => {
        const r = document.querySelector("#prefs .nav-fold").getBoundingClientRect(), p = document.querySelector("#prefs").getBoundingClientRect();
        return [r.right <= p.right + 0.5 && r.left >= p.left - 0.5, getComputedStyle(document.querySelector("#prefs .nav-fold")).color];
      });
      assert.ok(a, "the chevron sits inside the row");
      assert.ok(b, "the chevron has a colour");

      // and again unfolds it
      await prefs.click();
      await open(page).first().waitFor();
      assert.equal(await open(page).count(), 8);
      assert.equal(await prefs.getAttribute("aria-expanded"), "true");

      // folded, away and back: it stays folded, its chevron there to open it
      await prefs.click();
      await folded(page);
      await page.locator('#nav button[data-view="agents"]').click();
      await page.waitForFunction(() => !document.querySelector("#prefs").classList.contains("on"));
      assert.equal(await chev.isVisible().catch(() => false), false, "the chevron goes with the page");
      await prefs.click();
      await page.waitForFunction(() => document.querySelector("#prefs").classList.contains("on"));
      await page.waitForTimeout(350);
      assert.equal(await open(page).count(), 0, "a folded list stays folded");
      assert.ok(await chev.isVisible());
      await chev.click();
      await open(page).first().waitFor();

      // the same for a page in the list above: Usage's row folds its tabs
      const usage = page.locator('#nav button[data-view="usage"]');
      await usage.click();
      await page.waitForFunction(() => document.querySelector('#side .nav-sub:not(.leaving)')?.dataset.view === "usage");
      assert.ok(await usage.locator(".nav-fold").isVisible(), "Usage's row shows the chevron");
      assert.equal(await prefs.locator(".nav-fold").isVisible().catch(() => false), false, "Settings' went with its list");
      await usage.click();
      await folded(page);
      assert.ok(await usage.evaluate((b) => b.classList.contains("on")), "Usage is still open");
      assert.equal(await usage.getAttribute("aria-expanded"), "false");
      // from the keyboard too
      await usage.focus();
      await page.keyboard.press("Enter");
      await page.waitForFunction(() => document.querySelector('#side .nav-sub:not(.leaving)')?.dataset.view === "usage");
      assert.equal(await usage.getAttribute("aria-expanded"), "true");

      assert.deepEqual(errors, []);
      await context.close();
    });
  });
}
