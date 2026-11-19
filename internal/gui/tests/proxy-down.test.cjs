// Run with Node's test runner and Playwright on the module path; see README.md.
// @bitssly on X (Windows, proxy Auto): a VPN app turned off left the
// system's proxy at its loopback port, and every model failed with 502
// "proxy not reachable" until Off. Auto now goes direct while nothing
// listens there (netproxy.liveSystem), and Settings says so: the proxy
// found, by its address without its login, and that requests go direct.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
function server(lang) {
  const cur = {
    theme: "light", lang, tray: "panel", currency: "usd", textSize: 100,
    proxy: "", proxyNow: "http://127.0.0.1:7897", proxySource: "down",
    trayUsageEvery: 3, version: "test", dir: "/test", lanURLs: [],
    visionModels: [], imageGenModels: [], workbuddyCheckins: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: true })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: cur.fx });
    if (url.pathname === "/api/settings") return json(cur);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const want = {
  en: "Nothing answers at the proxy found, http://127.0.0.1:7897; requests go direct until it runs again",
  zh: "找到的代理 http://127.0.0.1:7897 没有在运行，请求先直连，等它恢复后再走代理",
  "zh-TW": "找到的代理 http://127.0.0.1:7897 沒有在執行，請求先直連，等它恢復後再走代理",
  ja: "見つかったプロキシ http://127.0.0.1:7897 が応答しないため、再び動くまで直接接続します",
  de: "Der gefundene Proxy http://127.0.0.1:7897 antwortet nicht; Anfragen laufen direkt, bis er wieder läuft",
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(want)) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: Auto with a dead system proxy says it goes direct`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(4000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/?view=settings&tab=network");
        await page.waitForFunction((s) => document.querySelector("#proxySub")?.textContent === s, want[lang]);
        assert.deepEqual(errors, []);
      });
    }
  }
}
