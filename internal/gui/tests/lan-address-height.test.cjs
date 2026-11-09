// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Network: Share on local network's Address row is as tall as its
// controls, with the same room above them as below (the owner, 11-08: a
// blank band under the address made the card's spacing uneven). With a
// pointer that hovers, Settings' rows are content-box, and the row's fixed
// height had the padding added under it. One address and several, wide and
// narrow, Chromium and WebKit, English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function server(lang, lanURLs) {
  const s = {
    theme: "light", lang, tray: "panel", currency: "usd", proxy: "", redactWords: [],
    version: "0.1.400", dir: "/config/magpie", gateway: "http://0.0.0.0:3425", proxyNow: "none", proxySource: "none",
    visionModels: [], imageGenModels: [], workbuddyCheckins: [],
    lan: true, lanKey: "sk-magpie-0123456789abcdef", lanURLs, lanPublic: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
  };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: s.fx });
    if (url.pathname === "/api/settings") return json(s);
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
  test(engine + ": the LAN address row is as tall as its controls", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      for (const urls of [["http://10.0.0.175:3425"], ["http://10.0.0.175:3425", "http://192.168.1.5:3425"]]) {
        for (const width of [1300, 560]) {
          const what = `${lang} ${urls.length} address(es) at ${width}px`;
          await t.test(what, async () => {
            const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
            const page = await ctx.newPage();
            page.setDefaultTimeout(5000);
            page.setDefaultNavigationTimeout(60000);
            const errors = [];
            page.on("pageerror", (e) => errors.push(e.message));
            await page.route("**/*", server(lang, urls));
            await watchSettingsReads(page);
            await page.goto("http://localhost:3430/?view=settings&tab=network");
            await settingsDrawn(page);
            await page.locator("#lanList .lan-address-row .lan-copy button").waitFor();
            const m = await page.evaluate(() => {
              const rows = [...document.querySelectorAll("#lanList > .row")];
              const r = document.querySelector("#lanList .lan-address-row");
              const rb = r.getBoundingClientRect();
              const parts = [...r.querySelectorAll(".lan-address-controls > *, .who")].map((e) => e.getBoundingClientRect()).filter((b) => b.height);
              return {
                hover: matchMedia("(hover: hover)").matches,
                share: rows[0].getBoundingClientRect().height, row: rb.height,
                above: Math.min(...parts.map((b) => b.top)) - rb.top,
                below: rb.bottom - Math.max(...parts.map((b) => b.bottom)),
              };
            });
            const at = what + ": " + JSON.stringify(m);
            assert.ok(m.hover, "the page has a pointer that hovers, where the rows are content-box: " + at);
            assert.ok(Math.abs(m.above - m.below) <= 2, "as much room below the controls as above: " + at);
            if (width > 600) assert.ok(Math.abs(m.row - m.share) <= 1, "as tall as the row above it: " + at);
            assert.deepEqual(errors, []);
            await ctx.close();
          });
        }
      }
    }
  });
}
