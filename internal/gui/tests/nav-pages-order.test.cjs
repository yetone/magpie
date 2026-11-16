// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › General › Sidebar pages puts the pages in the reader's order
// (chet030714 on X: Routing, Providers and Agents first in a phone's tab
// bar, the rest under More). A row of its menu is dragged to its place, or
// moved with Alt+↑/↓; the sidebar follows the order and leaves out its
// groups' names, a phone's tab bar shows the first four with the rest under
// More, the order is kept for this browser (localStorage magpie.navOrder), a
// page the kept order doesn't name goes after the page it follows at home,
// and Default order puts it all back. Chromium and WebKit, every language. No
// backend: the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const BASE = "http://magpie.test/";
const HOME = ["agents", "sessions", "library", "providers", "routing", "groups", "gateway", "usage", "masking", "plugins"];
const RESET = { en: "Default order", zh: "恢复默认顺序", "zh-TW": "恢復預設順序", ja: "既定の順に戻す", de: "Standardreihenfolge" };

function server(lang, web) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (d) => route.fulfill({ json: d });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/settings") return json({ theme: "light", lang, version: "0.1.400", gateway: "http://127.0.0.1:3425", web, visionModels: [], imageGenModels: [], lanURLs: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

async function open(browser, { lang = "en", web = false, phone = false } = {}) {
  const context = await browser.newContext(phone
    ? { viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, hasTouch: true, isMobile: browser.browserType().name() === "chromium", reducedMotion: "reduce" }
    : { viewport: { width: 1100, height: 760 }, reducedMotion: "reduce" });
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  page.errors = [];
  page.on("pageerror", (e) => page.errors.push(e.message));
  await page.route("**/*", server(lang, web));
  return page;
}

// the built-in pages in the sidebar's order, and the groups' names it shows
const order = (page) => page.$$eval("#nav > button[data-view]:not([data-plugin-page])", (bs) => bs.map((b) => b.dataset.view));
const groups = (page) => page.$$eval("#nav > .nav-gl", (gs) => gs.filter((g) => !g.hidden).map((g) => g.textContent));
const menuOrder = (page) => page.$$eval(".nav-pages-menu .nav-pages-list .pm-item", (rs) => rs.map((r) => r.dataset.v));
// the sidebar in that order: a kept order is put in place by navorder.js,
// imported as the page starts
const arranged = (page, want) => page.waitForFunction((w) => [...document.querySelectorAll("#nav > button[data-view]:not([data-plugin-page])")].map((b) => b.dataset.view).join() === w.join(), want);
const kept = (page) => page.evaluate(() => JSON.parse(localStorage.getItem("magpie.navOrder") || "null"));
const move = (list, v, to) => { const o = list.filter((x) => x !== v); o.splice(to, 0, v); return o; };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(RESET)) {
    test(`${engine} ${lang}: the pages go in the reader's order, in the sidebar and a phone's tab bar`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await open(browser, { lang });
      await page.goto(BASE + "?view=settings");
      await page.waitForSelector("#navPagesRow:not([hidden]) .nav-pages-pick");
      assert.deepEqual(await order(page), HOME, "home order at first");
      const homeGroups = await groups(page);
      assert.equal(homeGroups.length, 4, "the sidebar's groups are named");
      await page.locator(".nav-pages-pick").click();
      await page.waitForSelector(".nav-pages-menu");
      assert.deepEqual(await menuOrder(page), HOME, "the menu lists every page, Providers too, in their order");
      assert.equal(await page.locator(".nav-pages-reset").isVisible(), false, "no Default order while it is the default");
      // Providers is always shown: a click leaves it ticked
      await page.locator('.nav-pages-menu [data-v="providers"]').click({ force: true });
      assert.equal(await page.locator('.nav-pages-menu [data-v="providers"]').getAttribute("aria-checked"), "true");
      // Routing to the top from the keyboard
      await page.locator('.nav-pages-menu [data-v="routing"]').focus();
      for (let i = 0; i < 4; i++) await page.keyboard.press("Alt+ArrowUp");
      let want = move(HOME, "routing", 0);
      assert.deepEqual(await menuOrder(page), want);
      assert.deepEqual(await order(page), want, "the sidebar follows at once");
      assert.deepEqual(await groups(page), [], "its groups' names go with the home order");
      assert.equal(await page.evaluate(() => document.activeElement?.dataset.v), "routing", "the moved row keeps the focus");
      // Providers dragged up to second by its row
      const from = await page.locator('.nav-pages-menu [data-v="providers"]').boundingBox();
      const onto = await page.locator('.nav-pages-menu [data-v="agents"]').boundingBox();
      await page.mouse.move(from.x + from.width - 10, from.y + from.height / 2);
      await page.mouse.down();
      for (let i = 1; i <= 12; i++) await page.mouse.move(from.x + from.width - 10, from.y + from.height / 2 + (onto.y - 4 - from.y) * i / 12);
      await page.waitForTimeout(80);
      await page.mouse.up();
      want = move(want, "providers", 1);
      assert.deepEqual(await menuOrder(page), want, "dragged into place");
      assert.equal(await page.locator('.nav-pages-menu [data-v="providers"]').getAttribute("aria-checked"), "true", "a drag ticks nothing off");
      assert.deepEqual(await kept(page), want, "kept");
      // hiding a page still works beside the order
      await page.locator('.nav-pages-menu [data-v="sessions"]').click();
      assert.equal(await page.locator('#nav button[data-view="sessions"]').isHidden(), true);
      await page.keyboard.press("Escape");
      assert.equal(await page.locator(".nav-pages-menu").count(), 0);

      // kept for this browser: a reload draws it so from the start
      await page.goto(BASE + "?view=providers");
      await page.waitForSelector("#nav button.on");
      await arranged(page, want);
      assert.deepEqual(await groups(page), []);

      // a phone's tab bar: the first four shown, the rest under More
      const phone = await open(browser, { lang, web: true, phone: true });
      await phone.goto(BASE);
      await phone.evaluate((o) => { localStorage.setItem("magpie.navOrder", JSON.stringify(o)); localStorage.setItem("magpie.navHidden", '["sessions"]'); }, want);
      await phone.goto(BASE + "?view=providers");
      await phone.waitForSelector("#nav .nav-more");
      await arranged(phone, want);
      const bar = await phone.$$eval("#nav > button", (bs) => bs.filter((b) => getComputedStyle(b).display !== "none" && b.getBoundingClientRect().width > 0).map((b) => b.dataset.view || "more"));
      const shown = want.filter((v) => v !== "sessions" && v !== "groups");
      assert.deepEqual(bar, [...shown.slice(0, 4), "more"], "the bar: " + JSON.stringify(bar));
      await phone.locator("#nav .nav-more").tap();
      const rest = await phone.$$eval(".nav-more-menu .pm-item", (rs) => rs.length);
      assert.ok(rest >= shown.length - 4, "More has the rest");
      // the order is set on the phone too, where its pages are hidden
      await phone.keyboard.press("Escape");
      await phone.goto(BASE + "?view=settings");
      await phone.waitForSelector("#navPagesRow:not([hidden]) .nav-pages-pick");
      await phone.locator(".nav-pages-pick").tap();
      await phone.waitForSelector(".nav-pages-menu .grip");
      assert.equal(await phone.locator('.nav-pages-menu [data-v="routing"] .grip').isVisible(), true, "a phone shows the grip");
      assert.deepEqual(phone.errors, []);

      // a page the kept order doesn't name (new in an update) goes after
      // the page it follows at home
      await page.evaluate(() => localStorage.setItem("magpie.navOrder", JSON.stringify(["routing", "providers", "agents", "sessions", "library", "gateway", "usage", "masking", "plugins"])));
      await page.goto(BASE + "?view=settings");
      await page.waitForSelector(".nav-pages-pick");
      await arranged(page, ["routing", "groups", "providers", "agents", "sessions", "library", "gateway", "usage", "masking", "plugins"]);

      // Default order puts the pages and their groups back
      await page.locator(".nav-pages-pick").click();
      await page.locator(".nav-pages-reset").click();
      assert.equal(await page.locator(".nav-pages-reset").isVisible(), false);
      assert.equal(await page.locator(".nav-pages-reset").innerText().catch(() => RESET[lang]), RESET[lang]);
      assert.deepEqual(await order(page), HOME);
      assert.deepEqual(await groups(page), homeGroups);
      assert.equal(await kept(page), null);
      assert.deepEqual(page.errors, []);
    });
  }
}
