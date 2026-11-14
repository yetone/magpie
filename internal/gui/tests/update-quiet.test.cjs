// Run with Node's test runner and Playwright on the module path; see README.md.
// "右上角的更新按钮不见了" (visamzh, normalcoder_ on X, 11-14): a download
// waiting (ready) or a newer version found with downloads off (found)
// lights the header's Update pill at once. /api/update says quiet only
// while the pill's × holds (3 days, "红点都快焊死了", certhon520 and others
// on X: it used to hold only until the next release, minutes later): the
// pill stays away, Settings' Update button row says it is hidden for 3 days
// with Show again, and the version row still offers the restart. Every
// language, tray panel, a narrow window and a wide one; no backend, the API
// is faked.
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
    if (url.pathname === "/api/settings/update-skip") return json(settings);
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
  test(engine + ": the Update pill shows a waiting update, and keeps away while its × holds", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const open = async (lang, ctl, query) => {
      const errors = [];
      const width = query.includes("panel") ? 420 : query.includes("narrow") ? 400 : 900;
      const page = await (await browser.newContext({ viewport: { width, height: 560 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
      await page.route("**/*", server(lang, ctl));
      await page.goto("http://magpie.test/" + query);
      return { page, errors };
    };
    const hidden = { en: "Hidden for 3 days", zh: "已隐藏 3 天", "zh-TW": "已隱藏 3 天", ja: "3 日間非表示にしています", de: "Für 3 Tage ausgeblendet" };
    const again = { en: "Show again", zh: "重新显示", "zh-TW": "重新顯示", ja: "再表示", de: "Wieder anzeigen" };
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const [where, mode] of [["panel", "?mode=panel"], ["narrow window", "?narrow"], ["window", ""]]) {
        for (const state of ["ready", "found"]) {
          await t.test(lang + " " + where + ": " + state + ", just out, shows the pill at once", async () => {
            const { page, errors } = await open(lang, { state, latest: "0.1.402" }, mode);
            await page.locator("#update").waitFor({ state: "visible" });
            const box = await page.locator("#update").boundingBox();
            assert(box && box.x >= 0 && box.x + box.width <= (where === "panel" ? 420 : where === "narrow window" ? 400 : 900), "the pill is on screen");
            assert.deepEqual(errors, []);
          });
        }
        await t.test(lang + " " + where + ": quiet (the × holds) keeps the pill away, and Settings says so", async () => {
          const { page, errors } = await open(lang, { quiet: true }, mode);
          await page.locator("#sync").waitFor();
          await page.waitForTimeout(500);
          assert.equal(await page.locator("#update").isHidden(), true, "a dismissed pill came back");
          const v = await open(lang, { quiet: true }, "?view=settings&tab=about" + (mode ? "&narrow" : ""));
          await v.page.locator("#about .row.pref .val button.text").first().waitFor(); // the version row's restart
          const row = v.page.locator("#about .row.pref.update-pill-row");
          await row.locator(".sub", { hasText: hidden[lang] }).waitFor();
          await row.locator("button", { hasText: again[lang] }).waitFor();
          assert.equal(await v.page.locator("#update").isHidden(), true);
          assert.deepEqual([...errors, ...v.errors], []);
        });
      }
    }
  });
}
