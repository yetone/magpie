// Run with Node's test runner and Playwright on the module path; see README.md.
// #3464 (zeewell on X): Kiro caches but tells only credits, so its calls
// carry no cache tokens, and every hit rate read their 0 as a miss: "缓存
// 0%" on each Kiro call, and a mixed total pulled toward 0. The ledger now
// keeps such a call's cache as not counted (cache_unknown), and every hit
// rate leaves it out: the Usage page's agent rows and cache tile, the
// requests' Cache hit rate card (with how many were left out), each Kiro
// request's cache cells and details, and the tray panel's card. In
// Chromium and WebKit, in every language; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const reader = require("./reader.cjs");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();

// a counted call, 9000 of its 10000 prompt from the cache, and a Kiro one
// whose 148000 prompt no one counted: 90%, where reading Kiro's 0 as a miss
// said 9000 / 158000, 6%
const counted = { input: 500, cache_read: 9000, cache_write: 500 };
const kiro = { input: 148000, cache_read: 0, cache_write: 0, cache_unknown: 148000, cache_unknown_calls: 1 };
const plus = (...xs) => xs.reduce((a, x) => { for (const [k, v] of Object.entries(x)) a[k] = (a[k] || 0) + v; return a; }, {});
const agentRow = (name, x) => ({ name, icon: "", calls: 10, errors: 0, output: 1000, reasoning: 0, cost: 0, unpriced: 0, cache_unknown: 0, ...x });
const agents = [agentRow("Claude Code", plus(counted, kiro)), agentRow("Kiro only", kiro)];
const usage = { calls: 20, errors: 0, output: 2000, reasoning: 0, cost: 0, unpriced: 0, path: "/test/usage.jsonl", bucket: "day", agents, models: [], series: [], ...plus(counted, kiro, kiro) };

const req = (i, x) => ({
  t: new Date(now - (i + 1) * 60e3).toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color",
  providerAccount: "a@example.com", req: "claude-sonnet-4.5", model: "claude-sonnet-4.5", served: "claude-sonnet-4.5",
  ms: 3000, status: 200, rid: "r-" + i, ep: "/v1/messages", cost: 0.01, priced: true, out: 19, ...x,
});
const ROWS = [
  req(0, { provider: "anthropic", providerName: "Anthropic", in: 500, cache_read: 9000, cache_write: 500 }),
  req(1, { provider: "kiro", providerName: "Kiro", in: 148000, cache_read: 0, cache_write: 0, cache_unknown: true }),
];
const page = (q) => ({ period: q.get("period"), rows: ROWS, offset: 0, total: ROWS.length, calls: 2, errors: 0, output: 38, reasoning: 0, cost: 0.02, unpriced: 0,
  ...plus(counted, kiro), bucket: "day", series: [], by: { provider: [], agent: [], model: [] }, agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }] });

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
const LEFT_OUT = "{n} request left out: its vendor doesn't count the cache";
const NOT_COUNTED = "The vendor doesn't count the cache";

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a hit rate leaves out the calls whose vendor counts no cache`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of LANGS) {
      await t.test(lang, async () => {
        const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, reducedMotion: "reduce" });
        const errors = [];
        const open = async (url) => {
          const p = await context.newPage();
          p.setDefaultTimeout(5000);
          p.setDefaultNavigationTimeout(60000);
          p.on("pageerror", (e) => errors.push(e.message));
          await p.route("**/*", serve(lang));
          await p.goto(url);
          return p;
        };
        try {
          const p = await open("http://magpie.test/?view=usage");
          const say = (s, v) => p.evaluate(([s, v]) => t(s, v), [s, v]);
          const n1 = await p.evaluate(() => ledNum(1));
          const leftOut = await say(LEFT_OUT, { n: n1 }), notCounted = await say(NOT_COUNTED);
          if (lang !== "en") assert.notEqual(notCounted, NOT_COUNTED, "translated");
          const rate = (p90) => say("hit rate {p}", { p: p90 });

          // the Usage page: the mixed agent's row and the cache read tile
          // say 90%, the Kiro-only row none
          await p.locator("#usageAgents .row.stat").nth(1).waitFor();
          const notes = await p.locator("#usageAgents .row.stat .num small").allTextContents();
          assert.equal(notes.length, 2, notes.join(" | "));
          assert(notes[0].includes(await rate("90%")), notes[0]);
          const hitWord = (await rate("")).trim();
          assert(!notes[1].includes(hitWord), notes[1]);
          const r90 = await rate("90%");
          const tiles = await p.locator(".kpi").allTextContents();
          assert(tiles.some((x) => x.includes(r90)), tiles.join(" | "));

          // the requests: 90.0% with one left out, and the Kiro call's cache
          // cells say nothing counted rather than 0
          await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
          const rows = p.locator(".led tbody tr.led-row");
          await rows.nth(ROWS.length - 1).waitFor({ timeout: 15000 });
          const card = await p.locator("#ledKpi .blk").evaluateAll((bs, label) => {
            const b = bs.find((x) => x.querySelector(".k")?.textContent === label);
            return b && { v: b.querySelector(".v").textContent, sub: b.querySelector(".sub")?.textContent || "" };
          }, await say("Cache hit rate"));
          assert(card, "a Cache hit rate card");
          assert.equal(card.v, "90.0%");
          assert.equal(card.sub, leftOut);
          const cells = await p.locator(".led").evaluate((tb, [w, r]) => {
            const heads = [...tb.tHead.rows[0].cells].map((c) => c.textContent);
            const at = [heads.indexOf(w), heads.indexOf(r)];
            return [...tb.tBodies[0].rows].filter((x) => x.classList.contains("led-row")).map((x) => at.map((i) => ({ text: x.cells[i].textContent, title: x.cells[i].title })));
          }, [await say("Cache write"), await say("Cache read")]);
          // said short, the exact count on hover
          assert.deepEqual(cells[0].map((c) => c.text), [await p.evaluate(() => fmtN(500)), await p.evaluate(() => fmtN(9000))]);
          assert.equal(cells[0][1].title, await p.evaluate(() => ledNum(9000)));
          assert.deepEqual(cells[1], [{ text: "—", title: notCounted }, { text: "—", title: notCounted }]);
          // its details say so beside its tokens
          const tokens = await (await reader.openRequest(p, rows.nth(1))).evaluate((b) => b.querySelector(".led-tokens")?.textContent || b.textContent);
          assert(tokens.includes(notCounted), tokens);
          assert(await p.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "the page scrolls sideways");

          // the tray panel's card
          const panel = await open("http://magpie.test/?mode=panel");
          await panel.locator('[data-ptab="stats"]').click();
          await panel.locator("#panelUsage .pu-tot").waitFor();
          const blk = await panel.locator("#panelUsage .pu-tot .blk").evaluateAll((bs) => bs.map((b) => b.textContent));
          assert(blk.some((x) => x.includes("90%") && x.includes(leftOut)), blk.join(" | "));
          assert.deepEqual(errors, []);
        } finally {
          await context.close();
        }
      });
    }
  });
}
