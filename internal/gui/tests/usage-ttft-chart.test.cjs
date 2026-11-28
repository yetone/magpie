// Run with Node's test runner and Playwright on the module path; see README.md.
// A trend of the first token's wait on the Usage page's Requests tab
// (GoForceX on X: "在用量统计页加上查看 ttft 的图表"): a First token pill
// beside Speed. Waits are means, not sums, so it is drawn as speeds are:
// each hour's own wait a track, each model's a mark on it (a line each in
// the line chart, the hour's own dashed); the axis, the tooltip and the
// ranking say milliseconds and seconds, the ranking gives no share, and a
// period with no timed reply says so. Every language, wide and narrow,
// Chromium and WebKit; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const H = 3600e3;
const hour = (i) => new Date(Math.floor(now / H) * H - i * H).toISOString();

const row = (i, model, ttft) => ({
  route_id: 900 + i, t: new Date(now - (i + 1) * 60e3).toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color",
  provider: "zai", providerName: "Z.ai Coding Plan", host: "me@example.com", req: model, model, served: model,
  in: 1000, out: 100, ms: ttft + 1000, ttft_ms: ttft, status: 200, rid: "req_" + i, ep: "/v1/messages", cost: 0.01, priced: true,
});
const ROWS = [row(0, "glm-fast", 600), row(1, "kimi-slow", 2400), row(2, "glm-fast", 600)];

// glm-fast: 600 ms on average; kimi-slow: 2.4 s; the period's: 7200 ms over
// 6 timed replies, 1.2 s
const FAST = { id: "glm-fast", calls: 4, input: 4000, output: 400, cache_read: 0, cache_write: 0, cost: 0.04, timed: 4, ttft_ms: 2400, decode_ms: 4000, decode_out: 400 };
const SLOW = { id: "kimi-slow", calls: 2, input: 2000, output: 200, cache_read: 0, cache_write: 0, cost: 0.02, timed: 2, ttft_ms: 4800, decode_ms: 2000, decode_out: 200 };
const part = (x) => ({ calls: x.calls, tokens: x.output, cost: x.cost, timed: x.timed, ttft_ms: x.ttft_ms, decode_ms: x.decode_ms, decode_out: x.decode_out });
const half = (x) => ({ ...x, calls: x.calls / 2, output: x.output / 2, cost: x.cost / 2, timed: x.timed / 2, ttft_ms: x.ttft_ms / 2, decode_ms: x.decode_ms / 2, decode_out: x.decode_out / 2 });
const point = (time, parts) => {
  const s = (k) => parts.reduce((a, x) => a + x[k], 0);
  return {
    label: "", time, calls: s("calls"), input: s("input"), output: s("output"), cost: s("cost"),
    timed: s("timed"), ttft_ms: s("ttft_ms"), decode_ms: s("decode_ms"), decode_out: s("decode_out"),
    by: { model: Object.fromEntries(parts.map((x) => [x.id, part(x)])) },
  };
};

function ledger(period, none) {
  const by = none ? [{ ...FAST, timed: 0, ttft_ms: 0 }] : [FAST, SLOW];
  const series = none
    ? [point(hour(1), [{ ...FAST, timed: 0, ttft_ms: 0 }])]
    // hour(1): glm-fast alone; hour(0): both, its own 1.5 s (6000 ms over 4)
    : [point(hour(1), [half(FAST)]), point(hour(0), [half(FAST), SLOW])];
  return {
    period, rows: ROWS, offset: 0, total: ROWS.length, calls: 6, errors: 0, input: 6000, output: 600, cache_read: 0, cache_write: 0, cost: 0.06, unpriced: 0,
    timed: none ? 0 : 6, ttft_ms: none ? 0 : 7200, decode_ms: 6000, decode_out: 600,
    by: { model: by }, bucket: "hour", series, agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }],
  };
}

function server(lang, none) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: { rate: 7.2, at: new Date().toISOString() } });
    if (url.pathname === "/api/usage/requests") return json(ledger(url.searchParams.get("period"), none));
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 0, errors: 0, input: 0, output: 0, reasoning: 0, unpriced: 0, cost: 0, bucket: "day", series: [], agents: [], models: [], path: "" });
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

const L = {
  en: { pill: "First token", fast: "600 ms", slow: "2.4 s", hour: "1.5 s", none: "No streamed replies timed in this period" },
  zh: { pill: "首字耗时", fast: "600 毫秒", slow: "2.4 秒", hour: "1.5 秒", none: "这段时间没有可计时的流式回复" },
  "zh-TW": { pill: "首字耗時", fast: "600 毫秒", slow: "2.4 秒", hour: "1.5 秒", none: "這段時間沒有可計時的流式回覆" },
  ja: { pill: "最初のトークン", fast: "600 ミリ秒", slow: "2.4 秒", hour: "1.5 秒", none: "この期間に計測できたストリーミング応答はありません" },
  de: { pill: "Erstes Token", fast: "600 ms", slow: "2.4 s", hour: "1.5 s", none: "In diesem Zeitraum wurde keine gestreamte Antwort gemessen" },
};

async function open(browser, lang, width, none) {
  const ctx = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
  const p = await ctx.newPage();
  p.setDefaultTimeout(5000);
  p.setDefaultNavigationTimeout(60000);
  const errors = [];
  p.on("pageerror", (e) => errors.push(e.message));
  await p.addInitScript(() => { try { localStorage.removeItem("magpie.ledMetric"); localStorage.removeItem("magpie.ledStyle"); } catch {} });
  await p.route("**/*", server(lang, none));
  await p.goto("http://magpie.test/");
  await p.locator('[data-view="usage"]').first().click();
  await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
  await p.locator("#ledWrap .led tbody tr").first().waitFor();
  await p.waitForTimeout(300);
  await reader.click(p, p.locator("#ledMetric .opt").filter({ hasText: L[lang].pill }));
  await p.waitForTimeout(150);
  return { ctx, p, errors };
}

// the chart in the window, clear of its bottom, for a hover
async function inView(p, loc) {
  const view = p.viewportSize();
  const clear = async () => { const b = await loc.boundingBox(); return b.y + b.height + 60 < view.height; };
  if (await clear()) return;
  await p.mouse.move(view.width / 2, view.height / 2);
  await p.mouse.wheel(0, 300);
  for (const end = Date.now() + 5000; !(await clear()); await p.waitForTimeout(50)) {
    if (Date.now() > end) throw new Error("the chart never scrolled into the window");
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a trend of the first token's wait", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) for (const width of [1180, 420]) {
      const w = L[lang];
      await t.test(`${lang} ${width}px`, async () => {
        const { ctx, p, errors } = await open(browser, lang, width);
        const pill = p.locator("#ledMetric .opt").filter({ hasText: w.pill });
        assert.equal(await pill.count(), 1, "a First token pill");
        assert.equal(await pill.evaluate((b) => b.classList.contains("on")), true, "picked");
        assert.equal(await p.locator("#ledMetric select, select").count(), 0, "the app's own switch, no native select");
        // the switch with one pill more fits, wrapping if it must, never scrolling sideways
        const sideways = await p.evaluate(() => [document.documentElement.scrollWidth - innerWidth, ...[...document.querySelectorAll("#ledMetric, .led-trend")].map((b) => b.scrollWidth - b.clientWidth)]);
        assert(sideways.every((n) => n <= 1), `scrolls sideways: ${sideways}`);

        // columns: each hour's own wait a track, each model's a mark on it
        assert.equal(await p.locator("#ledChart rect.col.all").count(), 2, "the hour's own");
        assert.equal(await p.locator("#ledChart rect.col.mark").count(), 3, "a mark for each model at each hour it answered");
        // the slow model's mark is the highest one, above the hour's own
        const ys = await p.locator("#ledChart rect.col.mark").evaluateAll((rs) => rs.map((r) => r.y.baseVal.value));
        const tops = await p.locator("#ledChart rect.col.all").evaluateAll((rs) => rs.map((r) => r.y.baseVal.value));
        assert(Math.min(...ys) < Math.min(...tops), "a model slower than its hour sits above the column: " + JSON.stringify({ ys, tops }));
        // the axis says a time
        const axis = await p.locator("#ledChart text").allTextContents();
        const units = [w.fast, w.slow].map((x) => x.replace(/^[\d.]+/, ""));
        assert(axis.some((x) => /^\d/.test(x) && units.some((u) => x.endsWith(u))), "the axis in ms or s: " + axis.join(" | "));

        // the ranking: each one's own wait, no share of a whole
        const rank = async () => p.locator("#ledRank .rk").evaluateAll((rs) => rs.map((r) => ({ nm: r.querySelector(".rk-nm")?.textContent.trim(), val: r.querySelector(".rk-val")?.textContent.trim(), b: r.querySelector(".rk-b")?.textContent || "" })));
        const r = await rank();
        assert.equal(r.find((x) => x.nm === "glm-fast")?.val, w.fast, JSON.stringify(r));
        assert.equal(r.find((x) => x.nm === "kimi-slow")?.val, w.slow, JSON.stringify(r));
        // (a share is a whole "NN%" of its own; the hit rate keeps its decimals)
        assert(r.every((x) => !x.b.split(" · ").some((y) => /^\d+%$/.test(y.trim())) && !/%/.test(x.val)), "no share: " + JSON.stringify(r));

        // the tooltip over the hour both answered in
        const col = p.locator("#ledChart rect.col.all").nth(1);
        await inView(p, col);
        const box = await col.boundingBox();
        await p.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
        const tip = p.locator("#ledChart .tip");
        await tip.waitFor();
        const text = await tip.textContent();
        for (const v of [w.fast, w.slow, w.hour]) assert(text.includes(v), `the tooltip says ${v}: ${text}`);
        assert(await tip.evaluate((e) => { const a = e.getBoundingClientRect(), b = e.parentElement.getBoundingClientRect(); return a.left >= b.left - 1 && a.right <= b.right + 1 && a.right <= innerWidth + 1; }), "the tooltip inside the chart");
        await p.mouse.move(0, 0);

        // lines: one a model, each at its own, the hour's own dashed, none stacked
        await p.locator("#ledStyle .opt").nth(1).click();
        await p.locator("#ledChart g.ln").first().waitFor();
        const lines = await p.evaluate(() => ({
          keys: [...document.querySelectorAll("#ledChart g.ln")].map((g) => g.dataset.k),
          dashed: document.querySelectorAll("#ledChart g.ln .line.all").length,
          bands: document.querySelectorAll("#ledChart g.ln .band").length,
        }));
        assert.equal(lines.dashed, 1, "the hour's own, dashed: " + JSON.stringify(lines));
        assert.equal(lines.bands, 0, "waits aren't stacked");
        assert(lines.keys.includes("glm-fast") && lines.keys.includes("kimi-slow"), JSON.stringify(lines));
        await p.locator("#ledStyle .opt").nth(0).click();

        // kept for the next visit, as the other metrics are
        assert.equal(await p.evaluate(() => localStorage.getItem("magpie.ledMetric")), "ttft");
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }

    await t.test("no timed reply in the period", async () => {
      const { ctx, p, errors } = await open(browser, "zh", 1180, true);
      assert.equal((await p.locator("#ledChart .none").textContent()).trim(), L.zh.none);
      assert.equal(await p.locator("#ledChart rect.col").count(), 0);
      assert.deepEqual(errors, []);
      await ctx.close();
    });
  });
}
