// Run with Node's test runner and Playwright on the module path; see README.md.
// #694: a credits count on a card too narrow for it beside the window's
// name stands under the name and, still too long, breaks at its · — in
// WebKit too, which left the space between the count's two parts out when
// it checked the line fit and cut "100 / 100 积分 · 剩余 100%" to
// "… 剩余 100…". Whatever the width, every part of every count is whole.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const wb = [
  { provider: "workbuddy", name: "WorkBuddy", icon: "workbuddy-color", plan: "Free", user: "a", windows: [{ name: "Credits", used: 29.6, display: "710.99 / 2400", amount: 710.99, limit: 2400, unit: "credits" }] },
  { provider: "workbuddy-ai", name: "WorkBuddy AI", icon: "workbuddy-color", plan: "Free", user: "b", windows: [{ name: "Credits", used: 0, display: "0 / 100", amount: 0, limit: 100, unit: "credits" }] },
];
const trae = { after: { provider: "trae-cn-plugin", name: "Trae CN", icon: "trae", plan: "Credits", user: "c", windows: [{ name: "Credits", used: 39.28, amount: 1492.58, limit: 3800, unit: "credits" }] } };
function serve(lang, quotas) {
  const settings = { lang, theme: "light", quotaLeft: true, currency: "usd" };
  const state = { agents: [], profiles: [], settings };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/gateway/trace") return url.searchParams.get("wait") ? new Promise(() => {}) : json({ mine: true, now: new Date().toISOString(), seq: 0, totals: {}, routes: [] });
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

for (const [name, engine] of [["webkit", webkit], ["chromium", chromium]]) {
  test(`${name}: a stacked count breaks at its ·, never cut`, async () => {
    const browser = await engine.launch();
    try {
      for (const width of [600, 980, 990, 1000, 1010, 1100]) {
        const page = await (await browser.newContext({ viewport: { width, height: 820 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        await page.route("**/*", serve("zh", [...wb, trae.after]));
        await page.goto("http://magpie.test/?view=usage");
        await page.locator(".subscription-card .quota-n").nth(2).waitFor();
        await page.waitForTimeout(300);
        const counts = await page.evaluate(() => [...document.querySelectorAll(".quota-n")].map((n) => ({
          text: n.textContent,
          cut: n.scrollWidth > n.clientWidth || [...n.children].some((s) => s.scrollWidth > s.clientWidth),
        })));
        assert.equal(counts.length, 3);
        for (const c of counts) assert.equal(c.cut, false, `${width}px: "${c.text}" is cut`);
        await page.close();
      }
    } finally { await browser.close(); }
  });
}
