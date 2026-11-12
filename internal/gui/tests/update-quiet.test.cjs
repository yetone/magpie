// Run with Node's test runner and Playwright on the module path; see README.md.
// "红点都快焊死了" (certhon520, jkd6688, Yuyuaneth, milianxinqi on X): a release
// comes every few minutes, so a downloaded update was always waiting and the
// header's Update pill was always lit. /api/update says quiet for a version
// not behind for long and with no urgent release out: the pill stays away,
// while Settings' version row still offers the restart. Not quiet (behind
// for days, or urgent), it shows. Every language, tray panel and
// window; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function server(lang, ctl) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    const settings = {
      theme: "light", lang, tray: "panel", currency: "usd", redactWords: [], trayUsageEvery: 3,
      version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
      proxyNow: "none", proxySource: "none", visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
      fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    };
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings, fx: settings.fx });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/update") return json({ state: "ready", current: "0.1.400", latest: "0.1.401", ...ctl });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Update pill waits until the update has been out a while", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const open = async (lang, ctl, query) => {
      const errors = [];
      const page = await (await browser.newContext({ viewport: { width: query.includes("panel") ? 420 : 900, height: 560 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
      await page.route("**/*", server(lang, ctl));
      await page.goto("http://magpie.test/" + query);
      return { page, errors };
    };
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const mode of ["?mode=panel", ""]) {
        await t.test(lang + (mode ? " panel" : " window") + ": quiet keeps the pill away, and its version row still restarts", async () => {
          const { page, errors } = await open(lang, { quiet: true }, mode);
          await page.locator("#sync").waitFor();
          await page.waitForTimeout(500);
          assert.equal(await page.locator("#update").isHidden(), true, "a fresh update lit the pill");
          const v = await open(lang, { quiet: true }, "?view=settings&tab=about");
          await v.page.locator("#about .row.pref .val button.text").first().waitFor(); // the version row's restart
          assert.equal(await v.page.locator("#update").isHidden(), true);
          assert.deepEqual([...errors, ...v.errors], []);
        });
        await t.test(lang + (mode ? " panel" : " window") + ": not quiet (days behind, or urgent) shows the pill", async () => {
          const { page, errors } = await open(lang, {}, mode);
          await page.locator("#update").waitFor({ state: "visible" });
          assert.deepEqual(errors, []);
        });
      }
    }
  });
}
