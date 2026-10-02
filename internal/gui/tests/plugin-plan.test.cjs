// Run with Node's test runner and Playwright on the module path; see README.md.
// A plugin's account beside a built-in subscription (cursor-plugin, the
// account's builtin "cursor") reads its plan as the built-in's does:
// "Cursor Pro", "SuperGrok" with no plan told, not "Pro" or "signed in".
// A plugin of its own keeps its plan as told. English and Chinese; no
// backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function serve(lang) {
  const settings = { theme: "light", lang, tray: "panel" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a plugin's plan is named as its built-in's`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 700 } })).newPage();
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/");
      await page.waitForFunction(() => typeof accountPlan === "function");
      const plans = await page.evaluate(() => [
        accountPlan({ agent: "cursor-plugin", builtin: "cursor", plan: "pro" }),
        accountPlan({ agent: "cursor", plan: "pro" }),
        accountPlan({ agent: "grok-plugin", builtin: "grok" }),
        accountPlan({ agent: "grok" }),
        accountPlan({ agent: "fakeco", builtin: "fakeco", plan: "Max" }),
        accountPlan({ agent: "copilot", plan: "Education" }),
        accountPlan({ agent: "copilot", plan: "Pro+" }),
        accountPlan({ agent: "copilot" }),
      ]);
      assert.deepEqual(plans.slice(0, 4), ["Cursor Pro", "Cursor Pro", "SuperGrok", "SuperGrok"]);
      assert.equal(plans[4], "Max");
      assert.deepEqual(plans.slice(5), ["GitHub Education", "GitHub Pro+", "GitHub"]);
      assert.equal(await page.evaluate(() => quotaText({ unlimited: true, display: "Unlimited", used: 0 })), lang === "zh" ? "无限" : "Unlimited");
      const metrics = await page.evaluate(() => {
        const windows = [{ name: "Premium requests", used: 90, display: "270 / 300" }, { name: "Chat requests", unlimited: true, used: 0 }, { name: "Completions", unlimited: true, used: 0 }];
        const q = { windows, name: "Copilot", user: "test" };
        const row = accountQuota({ test: q }, "test"), panel = panelQuotaCard(q), usage = quotaWindows(q);
        return { row: row.textContent, meters: row.querySelectorAll(".aq-track").length, rings: panel.querySelectorAll(".pq-ring").length, full: panel.querySelectorAll(".pq-ring.full").length, tracks: usage.querySelectorAll(".quota-track").length };
      });
      assert.match(metrics.row, /270 \/ 300/);
      assert.equal(metrics.meters, 1);
      assert.equal(metrics.rings, 1);
      assert.equal(metrics.full, 1);
      assert.equal(metrics.tracks, 1);
      // windows past the row's two still read on one tooltip line, " · "
      // between them, when no family joins them (#510 split them by line)
      const tip = await page.evaluate(() => {
        const windows = ["A", "B", "C", "D"].map((name, i) => ({ name, used: 10 * i, display: String(i) }));
        const line = accountQuota({ test: { windows, name: "Copilot", user: "test" } }, "test");
        return line.title;
      });
      assert.equal(tip.split("\n").length, 1);
      assert.match(tip, /^C .* · D /);
      assert.deepEqual(errors, []);
    });
  }
}
