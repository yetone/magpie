// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings' rows where their feature is (the scattered settings audit after
// netube99 on X): magpie web draws no empty Menu bar cost row, as it has no
// menu bar; the alerts (allowance, balance, reset, task done) sit under
// their own Alerts heading, not among the allowances' display; what routing
// does when an account refuses or every one rests, and a busy model's place
// (from Network), sit under Models › Routing, which the Routing page opens.
// Every
// language, Chromium and WebKit, at 1100 and 420 wide, nothing past the
// window's edge. No backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function server(lang, web) {
  const cur = {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", textSize: 100,
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false, trayUsage: "", trayUsageEvery: 3,
    visionModels: [], imageGenModels: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web},textSize:100};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", textSize: 100 }, fx: cur.fx });
    if (url.pathname === "/api/settings") return json(cur);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json({ providers: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const fits = (page) => page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: Settings' rows sit with their feature`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const open = async (web, view) => {
          const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(6000);
          page.setDefaultNavigationTimeout(60000);
          page.errors = [];
          page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; page.errors.push(e.message); });
          await page.route("**/*", server(lang, web));
          await page.goto("http://magpie.test/?view=" + view);
          return page;
        };
        const tr = (page, s) => page.evaluate((s) => t(s), s);

        // the app: the alerts under their own heading, after the display's rows
        let page = await open(false, "settings&tab=usage");
        await page.locator("#currencySegs .opt").first().waitFor();
        const head = page.locator("#alertHead");
        assert(await head.isVisible(), "Alerts' heading shows");
        assert.equal((await head.textContent()).trim(), await tr(page, "Alerts"));
        for (const id of ["usageAlertRow", "balanceAlertRow", "resetReminderRow", "turnDoneRow"]) {
          assert.equal(await page.locator(`#alertList > #${id}`).count(), 1, id + " under Alerts");
        }
        assert(await page.evaluate(() => document.querySelector("#currencySegs").compareDocumentPosition(document.querySelector("#alertHead")) & Node.DOCUMENT_POSITION_FOLLOWING), "after the display's rows");
        assert(await page.locator("#trayCostRow").isVisible(), "the app has a menu bar");
        assert(await fits(page), "fits");
        assert.deepEqual(page.errors, []);

        // magpie web: no menu bar, so no empty Menu bar cost row
        page = await open(true, "settings&tab=usage");
        await page.locator("#currencySegs .opt").first().waitFor();
        assert.equal(await page.locator("#trayCostRow").isVisible(), false, "no Menu bar cost row in magpie web");
        assert(await fits(page), "fits in magpie web");
        assert.deepEqual(page.errors, []);

        // Models › Routing: the rows about where a request goes, after the
        // replies' own rows, and the busy model's place out of Network
        page = await open(false, "settings&tab=models");
        const rhead = page.locator("#routeList").locator("xpath=preceding-sibling::*[1]");
        await rhead.waitFor();
        assert.equal((await rhead.textContent()).trim(), await tr(page, "Routing"));
        for (const id of ["refusalRetriesRow", "retryAfterCapRow", "classifyTimeoutRow", "busyLoadRow"]) {
          assert.equal(await page.locator(`#routeList > #${id}`).count(), 1, id + " under Routing");
        }
        assert(await page.evaluate(() => document.querySelector("#replyList").compareDocumentPosition(document.querySelector("#routeList")) & Node.DOCUMENT_POSITION_FOLLOWING), "after the replies' rows");
        assert.equal(await page.locator("#busyLoadRow").count(), 1, "one busy model row, not one in Network too");
        assert(await page.locator("#busyLoadRow").isVisible(), "shown on the Models tab");
        assert(await fits(page), "fits");
        assert.deepEqual(page.errors, []);

        // the Routing page leads there
        page = await open(false, "routing");
        const go = page.locator("#rtToSet");
        await go.waitFor();
        assert.equal((await go.textContent()).trim(), (await tr(page, "Settings")) + " ›");
        assert(await fits(page), "the Routing page fits");
        await go.click();
        await page.locator("#view-settings #routeList .row.flash").waitFor();
        assert.deepEqual(page.errors, []);
      });
    }
  }
}
