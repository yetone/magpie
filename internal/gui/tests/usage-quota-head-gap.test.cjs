// Run with Node's test runner and Playwright on the module path; see README.md.
// The Usage page's Allowances head: Hide accounts, the trend's range and
// Used/Left stand apart, never touching (the owner: 这两个之间竟然没有空隙，
// 贴在一起了 — Hide accounts against Used/Left). Both engines, every
// language, wide and narrow; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const later = new Date(Date.now() + 5 * 3600e3).toISOString();
const quotas = [{ provider: "codex", name: "Codex", kind: "subscription", icon: "openai", plan: "Plus", user: "one@example.com", windows: [{ name: "5 hours", used: 20, resetsAt: later }] }];

function serve(lang) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: the Allowances head's controls stand apart`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=usage");
        await page.waitForSelector("#quotaMode:not([hidden]) .opt");
        // with the trend's range shown too, as when a curve is on
        await page.evaluate(() => { document.querySelector("#quotaTrend").hidden = false; });
        const boxes = await page.evaluate(() => ["#quotaTrend", "#usageMask", "#quotaMode"].map((s) => {
          const r = document.querySelector(s).getBoundingClientRect();
          return { s, left: r.left, right: r.right, top: r.top, bottom: r.bottom, w: r.width };
        }));
        for (const b of boxes) assert.ok(b.w > 0, `${b.s} is shown`);
        for (let i = 1; i < boxes.length; i++) {
          const [a, b] = [boxes[i - 1], boxes[i]];
          if (b.top >= a.bottom) continue; // wrapped onto its own line
          assert.ok(b.left - a.right >= 6, `${a.s} and ${b.s} are ${(b.left - a.right).toFixed(1)}px apart`);
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}
