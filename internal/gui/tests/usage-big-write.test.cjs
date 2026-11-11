// The Usage page's Requests line names the largest cache write, one click
// from its request, and says Claude Code's helper requests by their purpose,
// "(inferred)" where the gateway read it from the body (recih on X, #6199).
// Run as described in README.md; no local accounts or providers are used.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const assets = process.env.ASSET_DIR || path.resolve(__dirname, "../assets");
const now = new Date();
const rows = [
  { route_id: 12, kind: "agent_summary_inferred", cache_write: 108546, cache_read: 11541 },
  { route_id: 11, kind: "", cache_write: 900, cache_read: 50000 },
  { route_id: 10, kind: "auto_mode_check_inferred", cache_write: 40, cache_read: 0 },
].map((r, i) => ({ ...r, t: new Date(now - i * 1000).toISOString(), agent: "claude", agentName: "Claude Code", icon: "claude-color",
  provider: "anthropic", providerName: "Anthropic", model: "example-model", req: "example-model", served: "example-model",
  in: 10, out: 2, ms: 200, status: 200, cost: 0.01, priced: true }));
function ledger(q) {
  const filtered = q.get("route") ? rows.filter((r) => r.route_id === +q.get("route")) : rows;
  const sum = (k) => filtered.reduce((n, r) => n + r[k], 0);
  return { rows: filtered, offset: 0, total: filtered.length, calls: filtered.length, errors: 0,
    input: sum("in"), output: sum("out"), cache_read: sum("cache_read"), cache_write: sum("cache_write"), cost: 0.03, unpriced: 0,
    big_write: { n: 108546, route: 12, at: rows[0].t, model: "example-model" },
    agents: [{ id: "claude", name: "Claude Code", icon: "claude-color" }], purposes: ["kind:agent_summary", "kind:auto_mode_check", "unmarked"] };
}
function server(lang, asked) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs={lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window={};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], models: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/usage/requests") { asked.push(url.searchParams); return json(ledger(url.searchParams)); }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}
const words = {
  en: { big: "109K largest write", summary: "Progress summary (inferred)", classifier: "Auto mode check (inferred)" },
  zh: { big: "最大写入 109K", summary: "进度摘要（推断）", classifier: "自动模式检查（推断）" },
};
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: the largest cache write opens its request, helper requests are named`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 800 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(7000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], asked = [];
      page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
      await page.route("**/*", server(lang, asked));
      await page.goto("http://magpie.test/?view=usage&tab=requests");
      await page.waitForFunction(() => document.querySelectorAll("#ledWrap tbody tr.led-row").length === 3);
      const jump = page.locator("#ledSum button");
      await jump.waitFor({ state: "visible" });
      assert.equal(await jump.count(), 1);
      assert.equal(await jump.textContent(), words[lang].big);
      assert.equal(await page.evaluate(() => window.kindName("agent_summary_inferred")), words[lang].summary);
      assert.equal(await page.evaluate(() => window.kindName("auto_mode_check_inferred")), words[lang].classifier);
      // both spellings are one purpose, the plain kind's
      assert.equal(await page.evaluate(() => purposeOf("agent_summary_inferred")), "kind:agent_summary");
      await jump.click();
      await page.locator("#ledRoute").waitFor({ state: "visible" });
      await page.waitForFunction(() => document.querySelectorAll("#ledWrap tbody tr.led-row").length === 1);
      assert.equal(asked.at(-1).get("route"), "12");
      assert.deepEqual(errors, []);
    });
  }
}
