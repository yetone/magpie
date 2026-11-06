// Run with Node's test runner and Playwright on the module path; see README.md.
// teenux on Discord: the Routing page's requests say each one's cache hit
// rate, the Usage page's requests didn't. Each Usage request now has a
// Cache hit rate column, worked out as the Routing list does (what was
// read from the cache over the prompt, writes included) and said as it
// does ("90%"): "—" for a vendor that counts no cache and for a request
// with no prompt counted, 0% for one that missed. It stays while a
// narrower window leaves the raw cache columns out, goes only with the
// first token's column, and the row's details say it then. In Chromium and WebKit, in every language; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const usage = { calls: 4, errors: 1, input: 0, output: 0, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0, unpriced: 0, path: "/test/usage.jsonl", bucket: "day", agents: [], models: [], series: [] };

const req = (i, x) => ({
  t: new Date(now - (i + 1) * 60e3).toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color",
  providerAccount: "a@example.com", req: "claude-sonnet-4.5", model: "claude-sonnet-4.5", served: "claude-sonnet-4.5",
  ms: 3000, status: 200, rid: "r-" + i, ep: "/v1/messages", cost: 0.01, priced: true, out: 19, ...x,
});
// 9000 of a 10000 prompt read (90%, where read over input alone would be
// 1800%), a miss (0%), a vendor that counts no cache, and a failed request
// with no tokens
const ROWS = [
  req(0, { provider: "anthropic", providerName: "Anthropic", in: 500, cache_read: 9000, cache_write: 500 }),
  req(1, { provider: "anthropic", providerName: "Anthropic", in: 1000, cache_read: 0, cache_write: 0 }),
  req(2, { provider: "kiro", providerName: "Kiro", in: 148000, cache_read: 0, cache_write: 0, cache_unknown: true }),
  req(3, { provider: "anthropic", providerName: "Anthropic", in: 0, out: 0, cache_read: 0, cache_write: 0, status: 529, err_type: "overloaded_error", err: "Overloaded", priced: false, cost: 0 }),
];
const WANT = ["90%", "0%", "—", "—"];
const page = (q) => ({ period: q.get("period"), rows: ROWS, offset: 0, total: ROWS.length, calls: 4, errors: 1, input: 149500, output: 57, cache_read: 9000, cache_write: 500, cache_unknown: 148000, cache_unknown_calls: 1, reasoning: 0, cost: 0.03, unpriced: 0,
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
const NOT_COUNTED = "The vendor doesn't count the cache";
const SHARE = "The share of the prompt read from the cache";

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: each Usage request says its cache hit rate, as the Routing page does`, async (t) => {
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
          const say = (s, v) => p.evaluate(([s, v]) => t(s, v), [s, v]);
          const label = await say("Cache hit rate"), share = await say(SHARE), notCounted = await say(NOT_COUNTED);
          if (lang !== "en") assert.notEqual(share, SHARE, "translated");
          const rows = p.locator(".led tbody tr.led-row");
          await rows.nth(ROWS.length - 1).waitFor({ timeout: 15000 });
          assert.equal(await p.locator(".led").evaluate((tb) => tb.classList.contains("tight2")), false, "wide enough for it");
          const col = await p.locator(".led").evaluate((tb, label) => {
            const heads = [...tb.tHead.rows[0].cells];
            const i = heads.findIndex((c) => c.textContent === label);
            if (i < 0) return null;
            return {
              title: heads[i].title, after: heads[i - 1].textContent, shown: getComputedStyle(heads[i]).display !== "none",
              cells: [...tb.tBodies[0].rows].filter((x) => x.classList.contains("led-row")).map((x) => ({ text: x.cells[i].textContent, title: x.cells[i].title, faint: x.cells[i].classList.contains("faint") })),
            };
          }, label);
          assert(col, "a Cache hit rate column");
          assert(col.shown);
          assert.equal(col.after, await say("Cache read"), "beside the cache read");
          assert.equal(col.title, share);
          assert.deepEqual(col.cells.map((c) => c.text), WANT);
          assert.equal(col.cells[0].title, label + ": " + share);
          assert.equal(col.cells[0].faint, false);
          assert.equal(col.cells[2].title, notCounted, "a vendor that counts no cache says so, not 0%");
          assert(col.cells[2].faint && col.cells[3].faint);

          // while it is there the details don't say it again
          await rows.nth(0).click();
          const dt = p.locator(".led-detail").first().locator("dt", { hasText: label });
          assert.equal(await dt.count(), 1);
          assert.equal(await dt.isVisible(), false, "said once, in its column");
          // the cache columns go before it does
          // (these model names are too long for every column in 1200px)
          assert(await p.locator(".led").evaluate((tb) => tb.classList.contains("tight")), "the fixture is too wide for every column");
          assert.equal(await p.locator(".led thead th").filter({ hasText: await say("Cache read") }).isVisible(), false);
          assert(await p.locator(".led thead th").filter({ hasText: label }).isVisible());

          // narrow: the column goes with the first token's, and the row's
          // details say the rate
          await p.setViewportSize({ width: 760, height: 900 });
          await p.waitForFunction(() => document.querySelector(".led")?.classList.contains("tight2"));
          assert.equal(await p.locator(".led thead th").filter({ hasText: label }).isVisible(), false, "left out narrow");
          await dt.waitFor();
          assert.equal(await dt.evaluate((d) => d.nextElementSibling.textContent), "90%");
          // a request without one says nothing of it
          await rows.nth(2).click();
          assert.equal(await p.locator(".led-detail").nth(1).locator("dt", { hasText: label }).count(), 0);
          assert(await p.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "the page scrolls sideways");
          assert.deepEqual(errors, []);
        } finally {
          await context.close();
        }
      });
    }
  });
}
