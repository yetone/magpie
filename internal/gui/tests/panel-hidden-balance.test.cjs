// Run with Node's test runner and Playwright on the module path; see README.md.
// A key's balance drawn on its plan's card (keyCards) is hidden from the
// tray panel as the Usage page's Overview hides it (onkashiko on X: hidden
// in the main window, still in the menu bar's panel): a balance hidden
// leaves its plan's card without it, and a plan hidden leaves its balance
// on its own. A hide in the main window reaches the panel when it is shown
// again, with no restart. APIs are mocked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const quotas = [
  { provider: "stepfun", name: "StepFun", icon: "stepfun-color", user: "s@x.y", plan: "Plus", windows: [{ name: "5-hour", used: 12 }] },
  { provider: "stepfun", name: "StepFun", icon: "stepfun-color", user: "s@x.y", windows: [], balance: "¥9.00" },
  { provider: "minimax", name: "MiniMax", icon: "minimax-color", user: "m@x.y", plan: "Max", windows: [{ name: "5-hour", used: 30 }] },
  { provider: "minimax", name: "MiniMax", icon: "minimax-color", user: "m@x.y", windows: [], balance: "¥4.20" },
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
    test(`${engine} ${lang}: the panel hides a plan's balance, or its plan, as the Usage page does`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(async () => { await browser.close(); });
      const errors = [], posts = [];
      const settings = {
        theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", usageOrder: [], panelUsageHidden: [],
        usageCardsHidden: ["stepfun|s@x.y|balance", "minimax|m@x.y|plan"], usageWindowsHidden: [],
      };
      const context = await browser.newContext({ viewport: { width: 440, height: 600 }, reducedMotion: "reduce" });
      await context.addInitScript(() => { try { localStorage.setItem("magpie.panelTab", "usage"); } catch {} });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts, settings));
      await page.goto("http://magpie.test/?mode=panel");
      await page.waitForSelector("#panelQuota .pq-foot .pq-arrange");
      const drawn = () => page.evaluate(() => [...document.querySelectorAll("#panelQuota .pq-card")].map((c) =>
        (c.classList.contains("bal") ? "bal " : "plan ") + c.dataset.card + (c.querySelector(".quota-balance") ? " +balance" : "")));

      // StepFun's balance hidden: its plan's card without it; MiniMax's
      // plan hidden: its balance on its own, among the balances
      assert.deepEqual(await drawn(), ["plan stepfun|s@x.y", "bal minimax|m@x.y"]);
      assert.equal(await page.locator("#panelQuota .pq-bals .pq-amt").textContent(), "¥4.20");
      assert.equal(await page.locator("#panelQuota .pq-foot .pq-hidden").count(), 0, "neither vendor is hidden whole");

      // shown again in the main window, and MiniMax's balance hidden too,
      // while the panel was put away: shown again, the panel follows
      settings.usageCardsHidden = ["minimax|m@x.y|plan", "minimax|m@x.y|balance"];
      await page.evaluate(() => document.dispatchEvent(new Event("visibilitychange")));
      await page.waitForFunction(() => !document.querySelector('#panelQuota .pq-card[data-card="minimax|m@x.y"]'));
      assert.deepEqual(await drawn(), ["plan stepfun|s@x.y +balance"]);
      assert.equal(await page.locator("#panelQuota .pq-foot .pq-hidden").textContent(), await page.evaluate(() => t("{n} hidden", { n: 1 })));
      assert.deepEqual(posts, []);
      assert.deepEqual(errors, []);
    });
  }
}
