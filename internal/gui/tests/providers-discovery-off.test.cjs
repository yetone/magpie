// Run with Node's test runner and Playwright on the module path; see README.md.
// Subscription accounts are turned off in Settings › Privacy, and the
// Providers page, where every subscription then stops working, said nothing
// of it (the scattered settings audit after netube99 on X). A line over the
// list now says they are off, with a Settings › Privacy that opens the
// switch, lit; while they are on, no line. MAGPIE_NO_ACCOUNTS says so
// instead. Every language, Chromium and WebKit, at 1100 and 420 wide,
// nothing past the window's edge.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function serve(lang, mode) {
  const settings = { lang, theme: "light", noAccountDiscovery: mode === "off", lanURLs: [], fx: { rate: 7.2, at: new Date().toISOString() } };
  const full = { ...settings, noAccountsEnv: mode === "env" };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false,textSize:100};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(full);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [{ id: "relay", name: "Relay", icon: "generic", host: "relay.example", chat: "https://relay.example/v1", models: [], agents: [], key: { set: true, masked: "sk-…1234" } }], presets: [], excluded: [] });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const fits = (page) => page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: Providers says subscription accounts are off and leads to the switch`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const open = async (mode) => {
          const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(6000);
          page.setDefaultNavigationTimeout(60000);
          page.errors = [];
          page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; page.errors.push(e.message); });
          await page.route("**/*", serve(lang, mode));
          await page.goto("http://magpie.test/?view=providers");
          await page.locator("#view-providers .row", { hasText: "Relay" }).first().waitFor();
          return page;
        };
        const tr = (page, s) => page.evaluate((s) => t(s), s);

        // on: no line
        let page = await open("on");
        assert.equal(await page.locator("#movable .discovery-off").count(), 0, "no line while on");
        assert.deepEqual(page.errors, []);

        // off: the line, translated, and the way to the switch
        page = await open("off");
        const line = page.locator("#movable .discovery-off");
        await line.waitFor();
        assert(await page.locator("#movable").isVisible(), "shown");
        assert.equal((await line.locator(".dep-head").textContent()).trim(), await tr(page, "Subscription accounts are off"));
        assert.notEqual(await tr(page, "Subscription accounts are off"), lang === "en" ? "" : "Subscription accounts are off", "translated");
        assert(await fits(page), "fits");
        const go = line.locator("button.action");
        assert.equal((await go.textContent()).trim(), await tr(page, "Settings › Privacy"));
        const gb = await go.boundingBox(), lb = await line.boundingBox();
        assert(gb.x >= lb.x - 1 && gb.x + gb.width <= lb.x + lb.width + 1, "inside the line");
        await go.click();
        await page.locator("#view-settings #discoveryList .row.flash").waitFor();
        assert(await page.locator("#navBack").isVisible(), "Back to Providers");
        assert.deepEqual(page.errors, []);

        // MAGPIE_NO_ACCOUNTS: why it is off, once Settings has been read
        page = await open("env");
        await page.evaluate(() => api("settings").then((s) => { prefs = s; renderMovable(); }));
        await page.locator("#movable .discovery-off").waitFor();
        assert.equal((await page.locator("#movable .discovery-off .dep-why").textContent()).trim(), await tr(page, "Off for this run: MAGPIE_NO_ACCOUNTS is set"));
        assert.deepEqual(page.errors, []);
      });
    }
  }
}
