// Run with Node's test runner and Playwright on the module path; see README.md.
// A link into a setting (the Usage page's and the Routing page's Settings ›)
// leaves a Back in the title bar, as a browser's, to the page it came from;
// going to another page by hand drops it (the owner, 11-10). The row it lit
// keeps its ring's corners: the list's rounded clip cut them off. Every
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
    if (url.pathname === "/api/usage/quotas") return json([{ provider: "codex", name: "Codex", icon: "openai", plan: "Plus", user: "dev.one@example.com", windows: [{ name: "5 hours", used: 20 }] }]);
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
      test(`${engine} ${lang} ${width}: Back goes to where a link into a setting came from`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(6000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; errors.push(e.message); });
        await page.route("**/*", server(lang, false));
        await page.goto("http://magpie.test/?view=usage");
        const back = page.locator("#navBack");
        const go = page.locator("#quotaHead #usageToSet");
        await go.waitFor();
        assert.equal(await back.isVisible(), false, "no Back before a jump");
        await go.click();
        const lit = page.locator("#view-settings #setPage-usage #quotaLeftRow.flash");
        await lit.waitFor();
        // the ring is rounded as the list is inside its border
        const r = await lit.evaluate((row) => {
          const l = getComputedStyle(row.closest(".list"));
          return { row: parseFloat(getComputedStyle(row).borderTopLeftRadius), list: parseFloat(l.borderTopLeftRadius) - parseFloat(l.borderTopWidth) };
        });
        assert.ok(r.row >= r.list - 0.5, "the lit row's corners follow the list's: " + JSON.stringify(r));
        await back.waitFor();
        const say = await page.evaluate(() => t("Back to {page}", { page: t(magpie.page("usage").title) }));
        assert.equal(await back.getAttribute("aria-label"), say);
        assert.ok(await fits(page), "fits with Back");
        // Back sits clear of the tabs and the title bar's icons
        const clear = await page.evaluate(() => {
          const b = document.querySelector("#navBack").getBoundingClientRect();
          return [...document.querySelectorAll(".top #nav, .top .actions, .top .brand, .top #crumb")].filter((e) => e.offsetParent)
            .every((e) => { const o = e.getBoundingClientRect(); return o.right <= b.left + 0.5 || o.left >= b.right - 0.5; });
        });
        assert.ok(clear, "Back overlaps nothing in the title bar");
        await back.click();
        await page.locator("#view-usage").waitFor({ state: "visible" });
        assert.equal(await page.locator("#view-settings").isVisible(), false);
        assert.equal(await back.isVisible(), false, "gone once back");

        // Routing's Settings ›, then a page picked by hand: no Back
        await page.goto("http://magpie.test/?view=routing");
        await page.locator("#rtToSet").click();
        await page.locator("#view-settings #routeList .row.flash").waitFor();
        await back.waitFor();
        await back.click();
        await page.locator("#rtToSet").waitFor();
        await page.locator("#rtToSet").click();
        await back.waitFor();
        await page.evaluate(() => show("plugins"));
        await page.locator("#view-plugins").waitFor({ state: "visible" });
        assert.equal(await back.isVisible(), false, "another page picked drops Back");
        assert.deepEqual(errors, []);
      });
    }
  }
}
