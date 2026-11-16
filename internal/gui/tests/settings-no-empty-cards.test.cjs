// Run with Node's test runner and Playwright on the module path; see README.md.
// No empty card in Settings (fishmagic80 on X): with Share on local network
// off, Allowed addresses, Client certificates and HTTPS draw nothing into
// their cards, and each empty card showed as a thin line under the Local
// network card, three of them before Web pages. A card with no rows is not
// drawn at all, on every Settings page, wide and at 390px. English and
// Chinese, Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));

function server(lang) {
  const s = {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "/config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [],
    lan: false, lanURLs: [], allowIPs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
  };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data, status = 200) => route.fulfill({ json: data, status });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light"};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: s.fx });
    if (url.pathname === "/api/settings") return json(s);
    if (url.pathname === "/api/settings/https") return json({ on: false });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// cards on the shown Settings page that take room with no row in them
const emptyCards = (page) => page.evaluate(() => [...document.querySelectorAll("#view-settings .set-page .list")]
  .filter((l) => l.getClientRects().length && !l.children.length)
  .map((l) => (l.id || l.className) + " " + l.getBoundingClientRect().height + "px"));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Settings draws no empty card (fishmagic80)", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      for (const [where, width, height] of [["window", 980, 760], ["narrow", 390, 560]]) {
        await t.test(`${lang}, ${where}`, async () => {
          const page = await (await browser.newContext({ viewport: { width, height }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang));
          await watchSettingsReads(page);
          await page.goto(`http://127.0.0.1:4999/?view=settings&tab=network`);
          await settingsDrawn(page);
          await page.locator("#lanList .row").first().waitFor();
          // the reporter's case: Share on local network off, Off picked
          assert.equal(await page.locator("#lanList .row").count(), 1);
          assert.equal(await page.locator("#lanList .opt.on").first().textContent().then((s) => s.trim()),
            await page.evaluate(() => t("Off")));
          await page.waitForTimeout(100);
          assert.deepEqual(await emptyCards(page), [], "network page");
          // the Local network card is followed by Web pages' header, nothing between
          const gap = await page.evaluate(() => {
            const lan = document.querySelector("#lanList").getBoundingClientRect();
            const head = document.querySelector("#corsList").previousElementSibling.getBoundingClientRect();
            return head.top - lan.bottom;
          });
          assert(gap < 40, `Web pages sits ${gap}px under the Local network card`);
          let tabs = 0;
          for (const tab of await page.locator("#view-settings [role=tab]").all()) {
            if (!(await tab.isVisible())) continue;
            tabs++;
            await tab.click();
            await page.waitForTimeout(150);
            assert.deepEqual(await emptyCards(page), [], "tab " + (await tab.getAttribute("id")));
          }
          assert(tabs >= 3, `only ${tabs} Settings tabs`);
          assert.deepEqual(errors, []);
          await page.context().close();
        });
      }
    }
  });
}
