// Run with Node's test runner and Playwright on the module path; see README.md.
// A group's speed in the Usage page's ranking and its Output speed card is
// the median of its replies' own, not their summed tokens over their summed
// time (FallMonkey on X: fable-5-2's ranking row read ≥31 tok/s over 1,034
// replies whose own rows mostly read ≥80-105; a few replies held for many
// minutes before their first token, by a rate limit or a closed lid, filled
// the summed time). The server sends each group's replies counted in 2%
// bins (usage.Rates, `rates.s` and `rates.f`); the page takes their middle
// as usage's median does, and Other's row is the median of all its groups'
// replies, not a sum of theirs. The sums here say 31 and 10, what the old
// page showed. Every language; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();

// usage.histBin: 2% bins, bin 400 holds 1
const bin = (v) => Math.floor(Math.log(v) / Math.log(1.02)) + 400;
const mid = (b) => 1.02 ** (b - 399.5);
const FAST = bin(100), STALLED = bin(1.1), SLOW = bin(40), QUICK = bin(300);
const G = (id, input, more) => ({ id, calls: 10, input, output: 1000, cache_read: 0, cache_write: 0, cost: 0, ...more });
const GROUPS = [
  // the reporter's: 1,000 floors at ~100 tok/s, 34 held for half an hour;
  // their tokens over their time, 2,068,000 over 66,709 s, read ≥31
  G("fable-5-2", 9e6, { calls: 1034, floor_out: 2068000, floor_ms: 66709677, floor_n: 1034, rates: { f: { [FAST]: 1000, [STALLED]: 34 } } }),
  // through the gateway: 500 replies at 100 tok/s after their first token,
  // 20 stalled mid-stream, 520,000 over 52 s summed: 10
  G("gpt-6.1-sol", 8e6, { calls: 520, timed: 520, ttft_ms: 520000, decode_out: 520000, decode_ms: 52000000, decode_n: 520, rates: { s: { [FAST]: 500, [STALLED]: 20 } } }),
  ...["a", "b", "c", "d", "e"].map((x, i) => G("filler-" + x, 7e6 - i * 1e5)),
  // Other: three replies at ~300 and two at ~40; their sums, 50 each
  G("small-1", 1000, { timed: 3, ttft_ms: 3000, decode_out: 1500, decode_ms: 30000, decode_n: 3, rates: { s: { [QUICK]: 3 } } }),
  G("small-2", 900, { timed: 2, ttft_ms: 2000, decode_out: 1000, decode_ms: 20000, decode_n: 2, rates: { s: { [SLOW]: 2 } } }),
];
const SUM = ["calls", "input", "output", "timed", "ttft_ms", "decode_ms", "decode_out", "decode_n", "floor_out", "floor_ms", "floor_n"];
function plusRates(groups) {
  const out = { s: {}, f: {} };
  for (const g of groups) for (const k of ["s", "f"]) for (const [b, n] of Object.entries(g.rates?.[k] || {})) out[k][b] = (out[k][b] || 0) + n;
  return out;
}
// a page of the period's rows, so the Requests view has a table to show
const ROW = { in: 9000, cache_read: 0, cache_write: 0, status: 0, cost: 0, priced: false, source: "log", provider: "session-unknown", providerName: "Local session", agent: "claude", agentName: "Claude Code", icon: "claudecode-color", model: "fable-5-2", req: "fable-5-2", served: "fable-5-2" };
const ROWS = [0, 1, 2].map((i) => ({ ...ROW, t: new Date(now - (i + 1) * 60e3).toISOString(), out: 2000, ms: 20000 }));
function ledger(period) {
  const tot = {};
  for (const k of SUM) tot[k] = GROUPS.reduce((a, x) => a + (x[k] || 0), 0);
  return {
    period, rows: ROWS, offset: 0, total: ROWS.length, errors: 0, cache_read: 0, cache_write: 0, cost: 0, unpriced: 0, ...tot, rates: plusRates(GROUPS),
    by: { model: GROUPS }, bucket: "hour", series: [],
    agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }],
  };
}

const STATE = (lang) => ({
  agents: [{ id: "claude", name: "Claude Code", path: "/t/c.json", icon: "claudecode-color", wired: true, fields: [] }],
  profiles: [], settings: { lang, theme: "light" }, fx: { rate: 7.2, at: new Date(now).toISOString() },
});

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(STATE(lang));
    if (url.pathname === "/api/usage/requests") return json(ledger(url.searchParams.get("period")));
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 0, errors: 0, input: 0, output: 0, reasoning: 0, unpriced: 0, cost: 0, bucket: "day", series: [], path: "", agents: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], models: [], gateway: { running: false } });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// how each language writes a speed, and the median's tooltips
const L = {
  en: { tok: (n) => `${n} tok/s`, median: "The median of 520 replies", floorTip: "At least: the median of 1,034 replies" },
  zh: { tok: (n) => `${n} token/秒`, median: "520 次实测回复的中位数", floorTip: "下限：1,034 次读自会话文件的回复的中位数" },
  "zh-TW": { tok: (n) => `${n} token/秒`, median: "520 次實測回覆的中位數", floorTip: "下限：1,034 次讀自會話檔案的回覆的中位數" },
  ja: { tok: (n) => `${n} トークン/秒`, median: "520 件の応答の中央値", floorTip: "下限：セッションファイルから読んだ 1,034 件の応答の中央値" },
  de: { tok: (n) => `${n} Token/s`, median: "Median aus 520 Antworten", floorTip: "Mindestens: Median aus 1.034 Antworten" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a group's speed is the median of its replies', not their sums", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) {
      for (const width of [1400, 560]) {
        const w = L[lang];
        await t.test(lang + " " + width, async () => {
          const errors = [];
          const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
          const p = await ctx.newPage();
          p.setDefaultTimeout(5000);
          p.setDefaultNavigationTimeout(60000);
          p.on("pageerror", (e) => errors.push(e.message));
          await p.route("**/*", server(lang));
          await p.goto("http://magpie.test/");
          await p.locator('[data-view="usage"]').first().click();
          await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
          await p.locator("#ledWrap .led tbody tr").first().waitFor();
          await p.locator("#ledRank .rk").first().waitFor();
          await p.waitForTimeout(200);

          const rk = (id) => p.locator("#ledRank .rk").filter({ has: p.locator(".rk-nm", { hasText: id }) });
          const speed = rk("fable-5-2").locator(".rk-speed");
          const fable = (await speed.textContent()).trim();
          assert(fable.startsWith("≥" + w.tok(100)), "the reporter's row, the middle of its floors, not ≥31: " + fable);
          assert((await speed.getAttribute("title")).startsWith(w.floorTip), "it says it is a median: " + (await speed.getAttribute("title")));
          const sol = (await rk("gpt-6.1-sol").locator(".rk-speed").textContent()).trim();
          assert(sol.startsWith(w.tok(100)), "stalled replies don't pull 500 down to 10: " + sol);
          assert((await rk("gpt-6.1-sol").locator(".rk-speed").getAttribute("title")).startsWith(w.median));

          // Other: small-1's three at ~300 and small-2's two at ~40, the
          // middle of all five; not the sums' 50
          const other = p.locator("#ledRank .rk").last();
          const said = (await other.locator(".rk-speed").textContent()).trim();
          assert(said.startsWith(w.tok(Math.round(mid(QUICK)))), "Other's speed is the middle of its replies: " + said);

          // the period's card: the floors' middle, the measured one's under it
          const kpi = p.locator("#ledKpi .blk").nth(4);
          assert((await kpi.locator(".v").textContent()).trim().startsWith("≥" + w.tok(100)), "card: " + (await kpi.locator(".v").textContent()));
          assert.deepEqual(errors, []);
          await ctx.close();
        });
      }
    }
  });
}
