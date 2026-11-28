// Run with Node's test runner and Playwright on the module path; see README.md.
// A settings row's short field beside its Off/On (gandli on X): the
// Allowance alert's share, the Low balance alert's amount and the daily
// summary's time of day stand as tall as the switch beside them, within a
// pixel. On a phone's browser every field was raised to 44px for touch while
// the switch stayed 32px, so the box stood a third taller than it. Wide and
// narrow, the desktop app and magpie web, both engines, every language; the
// API is faked here, and nothing reaches the network.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function server(lang, web) {
  const cur = {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", dock: false, dockWindow: false, proxy: "",
    redact: true, redactPersonal: false, redactWords: [], codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "",
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "", version: "0.1.900", dir: "~/.config/magpie",
    gateway: "http://127.0.0.1:3425", proxyNow: "none", proxySource: "none", visionModels: [], imageGenModels: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false }, turnDoneAlert: 0, featuresOff: [],
    usageAlert: 80, balanceAlert: 5, barkKeySet: true, barkDaily: "21:00",
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web},featuresOff:[]};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: cur.fx });
    if (url.pathname === "/api/settings") return json(cur);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const ROWS = ["#usageAlertSegs", "#balanceAlertSegs", "#barkDailySegs"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["zh", "en", "zh-TW", "ja", "de"]) {
    for (const [width, web] of [[420, true], [1180, true], [1180, false], [420, false]]) {
      test(`${engine} ${lang} ${width}px ${web ? "web" : "app"}: an alert's field stands as tall as its Off/On`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-${web ? "web" : "app"}-alert-field.png`) });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, web));
        await watchSettingsReads(page);
        await page.goto("http://magpie.test/?view=settings&tab=usage");
        await settingsDrawn(page);
        for (const id of ROWS) await page.locator(`${id} input.at`).waitFor({ state: "attached" });
        const got = await page.evaluate((ids) => ids.map((id) => {
          const box = document.querySelector(id);
          const i = box.querySelector("input.at"), s = box.querySelector(".segs");
          return { id, input: i.getBoundingClientRect().height, segs: s.getBoundingClientRect().height };
        }), ROWS);
        for (const g of got) {
          assert.ok(g.segs > 0, `${g.id}: no Off/On drawn`);
          assert.ok(Math.abs(g.input - g.segs) <= 1, `${g.id}: the field is ${g.input}px tall beside a ${g.segs}px Off/On`);
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}
