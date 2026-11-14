// apple_test_src on X: Kimi Code's booster (加油包) showed only on the
// Usage page. The tray panel's Allowances card drew its 5-hour and weekly
// rings and left out a wallet window beside them; it now says what is left
// on it under the rings, in the reader's language, inside a narrow panel.
// Real page, isolated responses: no vendor or account is used.
// MAGPIE_QUOTA_ASSETS points at older assets to see it fail.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const assets = process.env.MAGPIE_QUOTA_ASSETS || path.resolve(__dirname, "../assets");
const iso = n => new Date(n).toISOString();
function fixtures() {
  const now = Date.now(), readAt = iso(now - 60000);
  return [{
    provider: "kimi-code", name: "Kimi Code", icon: "kimi", user: "k@example.com", plan: "Allegretto", readAt, windows: [
      { name: "5 hours", used: 30, resetsAt: iso(now + 2 * 3600000), credits: true },
      { name: "Week", used: 50, resetsAt: iso(now + 3 * 86400000), credits: true },
      { name: "Extra usage", balance: 98.5, unit: "CNY" },
    ],
  }];
}
function serve(lang, data) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd" };
  return async route => {
    const url = new URL(route.request().url()), json = body => route.fulfill({ json: body });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: true })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state" && url.searchParams.get("last")) return json({ agents: [], profiles: [], settings, last: true });
    if (url.pathname === "/api/state") { return json({ agents: [], profiles: [], settings }); }
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") { return json(data.quotas); }
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 0, cost: 0, days: [], agents: [], models: [], keys: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); }
    catch { await route.fulfill({ status: 404, body: "" }); }
  };
}
const card = '#panelQuota .pq-card[data-card="kimi-code|k@example.com"]';
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [440, 360]) {
      test(`${engine} ${lang} ${width}px: an allowance card says its booster's balance under its rings`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await browser.newPage({ viewport: { width, height: 640 }, reducedMotion: "reduce" });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", e => errors.push(e.message));
        await page.addInitScript(() => { try { localStorage.setItem("magpie.panelTab", "usage"); } catch {} });
        await page.route("**/*", serve(lang, { quotas: fixtures() }));
        await page.goto("http://magpie.test/?mode=panel");
        await page.locator(`${card} .pq-dial b`, { hasText: "30%" }).waitFor();
        const wallet = page.locator(`${card} .pq-rings + .pq-sub`);
        await wallet.waitFor();
        const text = (await wallet.textContent()).trim();
        const want = await page.evaluate(() => t("Extra usage") + " " + t("{n} left", { n: "98.5 " + t("CNY") }));
        assert.equal(text, want.trim());
        if (lang !== "en") assert.ok(!text.includes("Extra usage"), `said in ${lang}: ${text}`);
        assert.ok(await page.locator(`${card} .pq-ring`).count() === 2, "both rings stay");
        const fits = await page.locator(card).evaluate(c => c.scrollWidth <= c.clientWidth + 1 && c.getBoundingClientRect().right <= innerWidth);
        assert.ok(fits, "nothing runs out of the card");
        assert.deepEqual(errors, []);
      });
    }
  }
}
