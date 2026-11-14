// Run with Node's test runner and Playwright on the module path; see README.md.
// What the Usage page hides is left out of the tray panel's Allowances tab
// too (onkashiko on X: a provider hidden in the main window still showed in
// the menu bar's panel): a hidden card, a hidden row, and a subscription
// hidden whole, which Arrange calls hidden and whose Show brings it back on
// the page too. A hide in the main window reaches the panel when it is
// shown again, with no restart. APIs are mocked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const quotas = [
  { provider: "claude", name: "Claude Code", icon: "claude-color", user: "a@b.c", windows: [{ name: "Weekly", used: 17.6 }, { name: "5-hour", used: 42.2 }] },
  { provider: "codex", name: "Codex", icon: "codex-color", user: "x@y.z", windows: [{ name: "5-hour", used: 8 }] },
  { provider: "codex", name: "Codex", icon: "codex-color", user: "other@例子.test", windows: [{ name: "Weekly", used: 25 }] },
  { provider: "deepseek", name: "DeepSeek", icon: "deepseek-color", windows: [], balance: "¥12.30" },
  { provider: "kimi", name: "Kimi Code", icon: "kimi-color", windows: [{ name: "Weekly", used: 30 }] },
];

function serve(lang, posts, settings) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/usage/arrange") {
      const body = route.request().postDataJSON();
      posts.push(body);
      if (body.panelHidden) settings.panelUsageHidden = body.panelHidden;
      if (body.cardsHidden) settings.usageCardsHidden = body.cardsHidden;
      return json(settings);
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/usage") return json({ days: [] });
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
    test(`${engine} ${lang}: the panel leaves out what the Usage page hides`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(async () => { await browser.close(); });
      const errors = [], posts = [];
      const settings = {
        theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", usageOrder: [], panelUsageHidden: [],
        usageCardsHidden: ["deepseek|balance", "codex|x@y.z|plan"], usageWindowsHidden: ["claude|5-hour"],
      };
      const context = await browser.newContext({ viewport: { width: 440, height: 600 }, reducedMotion: "reduce" });
      await context.addInitScript(() => { try { localStorage.setItem("magpie.panelTab", "usage"); } catch {} });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts, settings));
      await page.goto("http://magpie.test/?mode=panel");
      await page.waitForFunction(() => document.querySelectorAll("#panelQuota .pq-card").length >= 2);
      const tr = (s, v) => page.evaluate(([s, v]) => t(s, v), [s, v]);
      const cards = () => page.evaluate(() => [...document.querySelectorAll("#panelQuota .pq-card")].map((c) => c.dataset.card));
      const rings = (card) => page.locator(`#panelQuota .pq-card[data-card="${card}"] .pq-ring`).count();

      // the hidden card, the account and the row are left out
      assert.deepEqual(await cards(), ["claude|a@b.c", "codex|other@例子.test", "kimi"]);
      assert.equal(await rings("claude|a@b.c"), 1, "the row the page hides is left out");
      assert.equal(await page.evaluate(() => document.querySelector("#panelQuota .pq-bals")), null, "no balance for the hidden DeepSeek card");
      assert.equal(await page.locator("#panelQuota .pq-foot .pq-hidden").textContent(), await tr("{n} hidden", { n: 1 }));

      // hidden in the main window while the panel was put away: shown again,
      // the panel has it gone, and one shown there comes back
      settings.usageCardsHidden = ["deepseek|balance", "kimi|plan"];
      settings.usageWindowsHidden = [];
      await page.evaluate(() => document.dispatchEvent(new Event("visibilitychange")));
      await page.waitForFunction(() => !document.querySelector('#panelQuota .pq-card[data-card="kimi"]'));
      assert(!(await cards()).includes("kimi") && (await cards()).includes("claude|a@b.c"));
      assert.equal(await rings("claude|a@b.c"), 2, "the row shown in the main window again is back");
      assert.equal(await page.locator("#panelQuota .pq-foot .pq-hidden").textContent(), await tr("{n} hidden", { n: 2 }));

      // Arrange calls them hidden; Show brings DeepSeek back on the page too
      await page.locator("#panelQuota .pq-arrange").click();
      await page.waitForSelector("#panelQuota .pq-arow");
      for (const key of ["deepseek", "kimi"]) {
        assert(await page.locator(`#panelQuota .pq-arow[data-key="${key}"]`).evaluate((r) => r.classList.contains("put-away")), key + " shows as hidden");
        assert.equal(await page.locator(`#panelQuota .pq-arow[data-key="${key}"] .pq-show`).textContent(), await tr("Show"));
      }
      assert.equal(await page.locator('#panelQuota .pq-arow[data-key="claude"] .pq-show').textContent(), await tr("Hide"));
      await page.locator('#panelQuota .pq-arow[data-key="deepseek"] .pq-show').click();
      await page.waitForFunction(() => !document.querySelector('#panelQuota .pq-arow[data-key="deepseek"]').classList.contains("put-away"));
      assert.deepEqual(posts.splice(0), [{ panelHidden: [], cardsHidden: ["kimi|plan"] }]);
      // hiding from the panel alone leaves the page's list as it is
      await page.locator('#panelQuota .pq-arow[data-key="claude"] .pq-show').click();
      await page.waitForFunction(() => document.querySelector('#panelQuota .pq-arow[data-key="claude"]').classList.contains("put-away"));
      assert.deepEqual(posts.splice(0), [{ panelHidden: ["claude"] }]);
      assert.deepEqual(settings.usageCardsHidden, ["kimi|plan"]);
      assert.deepEqual(errors, []);
    });
  }
}
