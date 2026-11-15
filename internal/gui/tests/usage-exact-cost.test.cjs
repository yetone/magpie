// Run with Node's test runner and Playwright on the module path; see README.md.
// shangyuan0818 on X: the Usage page's request costs all read "≈" with three
// decimals at most and none above $100, and the cache hit rate one decimal.
// A cost worked out from known prices is said as it is: no "≈", at least
// two decimals at any size ($123.46, not $123), a sub-cent one to its first
// two digits ($0.00042, not $0.000). The Requests head's cache hit rate has
// two decimals (87.65%). A sum missing some prices still ends in "+". In
// Chromium and WebKit, in every language; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const reader = require("./reader.cjs");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const usage = { calls: 3, errors: 0, input: 1235, output: 57, cache_read: 8765, cache_write: 0, reasoning: 0, cost: 123.47, unpriced: 0, path: "/test/usage.jsonl", bucket: "day", agents: [], models: [], series: [] };

const req = (i, x) => ({
  t: new Date(now - (i + 1) * 60e3).toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color",
  provider: "anthropic", providerName: "Anthropic", providerAccount: "a@example.com", req: "sonnet", model: "sonnet", served: "sonnet",
  ms: 3000, status: 200, rid: "r-" + i, ep: "/v1/messages", priced: true, out: 19, in: 400, cache_read: 3000, cache_write: 0, ...x,
});
const ROWS = [req(0, { cost: 123.456 }), req(1, { cost: 0.00042 }), req(2, { cost: 0.0123 })];
const WANT = ["$123.46", "$0.00042", "$0.012"];
// 8765 of a 10000 prompt read: 87.65%
const page = (q) => ({ period: q.get("period"), rows: ROWS, offset: 0, total: ROWS.length, calls: 3, errors: 0, input: 1235, output: 57, cache_read: 8765, cache_write: 0, reasoning: 0, cost: 123.46842, unpriced: 0,
  bucket: "day", series: [], by: { provider: [], agent: [], model: [] }, agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }] });

function serve(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: new Date(now).toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, calls: [], groups: [] } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/requests") return json(page(url.searchParams));
    if (url.pathname === "/api/usage/requests/content") return json({ found: false, why: "read" });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json(usage);
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname.startsWith("/api/window/")) return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404 }); }
  };
}

const LANGS = ["en", "zh", "zh-TW", "ja", "de"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a priced request's cost is said as it is, two decimals at least`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of LANGS) {
      await t.test(lang, async () => {
        const context = await browser.newContext({ viewport: { width: 1600, height: 900 }, reducedMotion: "reduce" });
        const errors = [];
        try {
          const p = await context.newPage();
          p.setDefaultTimeout(5000);
          p.setDefaultNavigationTimeout(60000);
          p.on("pageerror", (e) => errors.push(e.message));
          await p.route("**/*", serve(lang));
          await p.goto("http://magpie.test/?view=usage&tab=requests");
          const say = (s) => p.evaluate((s) => t(s), s);
          const rows = p.locator(".led tbody tr.led-row");
          await rows.nth(ROWS.length - 1).waitFor({ timeout: 15000 });
          assert.deepEqual(await p.locator(".led tbody tr.led-row td.cost").allTextContents(), WANT);
          // the head: the sum, and the hit rate to two decimals
          const blk = (label) => p.locator("#ledKpi .blk").filter({ has: p.locator(".k", { hasText: label }) }).locator(".v");
          assert.equal(await blk(await say("Cost")).textContent(), "$123.47");
          assert.equal(await blk(await say("Cache hit rate")).textContent(), "87.65%");
          // and the request's own page
          const dt = (await reader.openRequest(p, rows.nth(0))).locator("dt", { hasText: await say("Cost") });
          assert.equal(await dt.evaluate((d) => d.nextElementSibling.textContent), "$123.46");
          await reader.closeRequest(p);
          // the formatter at the edges: none is rounded away or to a bare dollar
          assert.deepEqual(await p.evaluate(() => [0, 0.0004, 0.000012345, 0.5, 1, 99.999, 100, 2500.5].map((cost) => fmtCost({ cost }))),
            ["$0.000", "$0.00040", "$0.000012", "$0.500", "$1.00", "$100.00", "$100.00", "$2500.50"]);
          assert.equal(await p.evaluate(() => wholeCost(1234.56)), "$1235", "an estimate stays whole");
          assert.deepEqual(errors, []);
        } finally {
          await context.close();
        }
      });
    }
  });
}
