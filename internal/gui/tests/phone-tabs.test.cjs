// Run with Node's test runner and Playwright on the module path; see README.md.
// magpie web on a phone (geraldpppppp on X: 优化移动端 UI，为什么看不到agent
// 以及资源库之类的那些入口？). The pages were a strip that scrolled sideways
// with nothing saying it did, and Agents and the Library lay past its edge.
// Now they are a tab bar at the window's foot, as an iPhone app's: five tabs,
// the fifth More when more pages show, its menu the rest with their icons.
// Every page is a tap or two away, the bar covers no part of a page or its
// status line, and nothing scrolls sideways. In gateway mode (Agents left
// out) and with every page; in every language; at 390px.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const LANGS = { en: "More", zh: "更多", "zh-TW": "更多", ja: "その他", de: "Mehr" };

function serve(lang, gateway) {
  const providers = { providers: [], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light", redact: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true${gateway ? ",gateway:true" : ""}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/settings") return json({ lang, theme: "light", web: true, gatewayOn: gateway, gatewayWhy: gateway ? "no-agents" : "" });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/plugins/market") return json({ listings: [], state: { bun: true, bunVersion: "1.3.0", plugins: [], picker: false } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

async function open(browser, lang, gateway, view) {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, hasTouch: true, isMobile: browser.browserType().name() === "chromium", reducedMotion: "reduce" });
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  page.errors = [];
  page.on("pageerror", (e) => page.errors.push(e.message));
  await page.route("**/*", serve(lang, gateway));
  await page.goto("http://magpie.test/" + (view ? "?view=" + view : ""));
  await page.waitForSelector("#nav", { state: "visible" });
  await page.waitForFunction(() => document.querySelector("#nav .nav-more") || document.querySelectorAll("#nav > button[data-view]").length);
  await page.waitForTimeout(300);
  return page;
}

// the tabs the bar shows, in order, each with its box
const tabs = (page) => page.$$eval("#nav > button", (bs) => bs.filter((b) => {
  const r = b.getBoundingClientRect();
  return getComputedStyle(b).display !== "none" && r.width > 0 && r.height > 0;
}).map((b) => {
  const r = b.getBoundingClientRect();
  return { view: b.dataset.view || "more", on: b.classList.contains("on"), x: r.x, y: r.y, w: r.width, h: r.height, label: b.textContent.trim() };
}));
// every page the reader may open: the nav's buttons that a phone's bar or its More list carries
const pages = (page) => page.$$eval("#nav > button[data-view]", (bs) => bs.filter((b) => !b.hidden && !b.dataset.replaced && b.dataset.view !== "groups" && getComputedStyle(b).display !== "none" || b.classList.contains("tb-over")).map((b) => b.dataset.view));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, moreWord] of Object.entries(LANGS)) {
    for (const gateway of [false, true]) {
      test(`${engine} ${lang}${gateway ? " gateway mode" : ""}: every page is reachable from the tab bar at 390px`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await open(browser, lang, gateway, gateway ? "providers" : "agents");
        const all = await pages(page);
        assert.ok(all.includes("library"), "the Library is a page here");
        assert.equal(all.includes("agents"), !gateway, "Agents shows unless gateway mode leaves it out");
        const bar = await tabs(page);
        assert.equal(bar.length, 5, "five tabs: " + JSON.stringify(bar.map((b) => b.view)));
        assert.equal(bar[4].view, "more", "the fifth is More");
        assert.equal(bar[4].label, moreWord, "More in this language");
        const vw = await page.evaluate(() => innerWidth), vh = await page.evaluate(() => innerHeight);
        for (const b of bar) {
          assert.ok(b.x >= 0 && b.x + b.w <= vw + 0.5, b.view + " is inside the window: " + JSON.stringify(b));
          assert.ok(b.y + b.h <= vh + 0.5 && b.y > vh - 100, b.view + " sits at the window's foot: " + JSON.stringify(b));
          assert.ok(b.h >= 44, b.view + " is a touch target");
        }
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "nothing scrolls sideways");
        const onBar = bar.slice(0, 4).map((b) => b.view);
        // the bar's own pages open with a tap
        for (const v of onBar) {
          await page.locator(`#nav > button[data-view="${v}"]`).tap();
          await page.waitForSelector("#view-" + v, { state: "visible" });
        }
        // the rest are in More's list, each with its icon, and open from it
        const rest = all.filter((v) => !onBar.includes(v));
        assert.ok(rest.length >= 1);
        for (const v of rest) {
          await page.locator("#nav .nav-more").tap();
          const menu = page.locator(".nav-more-menu");
          await menu.waitFor({ state: "visible" });
          const items = await menu.locator(".pm-item").count();
          assert.equal(items, rest.length, "More lists every page the bar hasn't");
          assert.equal(await menu.locator(".pm-item .nav-ic").count(), rest.length, "each with its tab's icon");
          const mb = await menu.boundingBox(), more = await page.locator("#nav .nav-more").boundingBox();
          assert.ok(mb.y + mb.height <= more.y + 1, "the menu opens above the bar");
          assert.ok(mb.x >= 0 && mb.x + mb.width <= vw + 0.5, "inside the window");
          await menu.locator(".pm-item").nth(rest.indexOf(v)).tap();
          await page.waitForSelector("#view-" + v, { state: "visible" });
          assert.equal(await page.locator(".nav-more-menu").count(), 0, "the menu closes");
          const now = await tabs(page);
          assert.ok(now.find((b) => b.view === "more").on, "More is marked while " + v + " is open");
          assert.ok(!now.slice(0, 4).some((b) => b.on), "no other tab is marked");
        }
        // back on a tab of the bar, More lets go of its mark
        await page.locator(`#nav > button[data-view="${onBar[0]}"]`).tap();
        await page.waitForSelector("#view-" + onBar[0], { state: "visible" });
        assert.equal((await tabs(page)).find((b) => b.view === "more").on, false);
        assert.deepEqual(page.errors, []);
      });
    }
  }

  test(`${engine}: the bar covers no part of a page or its status line`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await open(browser, "en", false, "settings");
    const bar = await page.locator("#nav").boundingBox();
    // the page's foot scrolls clear of the bar
    const last = await page.evaluate(async () => {
      const v = document.querySelector(".view:not([hidden])");
      v.scrollTop = v.scrollHeight;
      await new Promise((r) => setTimeout(r, 200));
      return Math.min(v.getBoundingClientRect().bottom, Math.max(...[...v.children].filter((e) => e.getBoundingClientRect().height > 0).map((e) => e.getBoundingClientRect().bottom)));
    });
    assert.ok(last <= bar.y + 1, `the page's last line (${last}) ends above the bar (${bar.y})`);
    // a status message sits above it
    await page.evaluate(() => { const s = document.querySelector(".foot .status") || document.querySelector(".status"); s.textContent = "Saved"; });
    await page.waitForTimeout(100);
    const st = await page.evaluate(() => { const s = document.querySelector(".foot .status") || document.querySelector(".status"); const r = s.getBoundingClientRect(); return { y: r.y, h: r.height, shown: getComputedStyle(s).display !== "none" }; });
    if (st.shown && st.h > 0) assert.ok(st.y + st.h <= bar.y + 1, "the status line is above the bar: " + JSON.stringify(st));
    assert.deepEqual(page.errors, []);
  });

  test(`${engine}: a desktop browser keeps the header's tabs, with no More`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const context = await browser.newContext({ viewport: { width: 1280, height: 800 }, reducedMotion: "reduce" });
    const page = await context.newPage();
    await page.route("**/*", serve("en", false));
    await page.goto("http://magpie.test/?view=providers");
    await page.waitForSelector("#view-providers", { state: "visible" });
    await page.waitForTimeout(300);
    assert.equal(await page.locator("#nav .nav-more").count(), 0);
    assert.equal(await page.locator("#nav .tb-over").count(), 0);
    const nav = await page.locator("#nav").boundingBox();
    assert.ok(nav.y < 200, "the tabs are at the top: " + JSON.stringify(nav));
  });
}
