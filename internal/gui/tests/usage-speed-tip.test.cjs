// Run with Node's test runner and Playwright on the module path; see README.md.
// The speed trend's tooltip on the Usage page's Requests tab shows each
// model's name whole, with its speed and first-token time in columns that
// line up from row to row, and the period's line on one line (#2343, tsuixl:
// "claude-sonnet-5..." cut short, each model's numbers crowded on its line,
// the sum wrapped). Both engines, wide and narrow, English and Chinese; no
// backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = process.env.MAGPIE_SPEED_TIP_ASSETS || path.resolve(__dirname, "../assets");
const now = Date.now();
const H = 3600e3;
const hour = (i) => new Date(Math.floor(now / H) * H - i * H).toISOString();

const MODELS = [
  { id: "claude-sonnet-5-20260929-thinking", calls: 41, output: 120000, ttft_ms: 41 * 1830, decode_ms: 41 * 2400 },
  { id: "gpt-6.1-sol-codex-max-high", calls: 17, output: 30000, ttft_ms: 17 * 640, decode_ms: 17 * 3000 },
  { id: "deepseek-v4.1-flash", calls: 9, output: 40000, ttft_ms: 9 * 420, decode_ms: 9 * 1500 },
].map((m) => ({ ...m, input: 1, cache_read: 0, cache_write: 0, cost: 0.1, timed: m.calls, decode_out: m.output }));
const total = (k) => MODELS.reduce((a, m) => a + m[k], 0);
const timing = { timed: total("timed"), ttft_ms: total("ttft_ms"), decode_ms: total("decode_ms"), decode_out: total("decode_out") };
const part = (x) => ({ calls: x.calls, tokens: x.output, cost: x.cost, timed: x.timed, ttft_ms: x.ttft_ms, decode_ms: x.decode_ms, decode_out: x.decode_out });
const row = (i, model) => ({
  route_id: 900 + i, t: new Date(now - (i + 1) * 60e3).toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color",
  provider: "zai", providerName: "Z.ai Coding Plan", req: model, model, served: model, in: 1000, out: 258, cache_read: 0, cache_write: 0,
  ms: 1762, ttft_ms: 762, status: 200, rid: "req_" + i, ep: "/v1/messages", cost: 0.01, priced: true,
});

function ledger(period) {
  return {
    period, rows: MODELS.map((m, i) => row(i, m.id)), offset: 0, total: MODELS.length, calls: total("calls"), errors: 0,
    input: total("input"), output: total("output"), cache_read: 0, cache_write: 0, cost: total("cost"), unpriced: 0, ...timing,
    by: { model: MODELS }, bucket: "hour",
    series: [{ label: "", time: hour(0), calls: total("calls"), input: 3, output: total("output"), ...timing, by: { model: Object.fromEntries(MODELS.map((m) => [m.id, part(m)])) } }],
    agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }],
  };
}

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/usage/requests") return json(ledger(url.searchParams.get("period")));
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

const SPEED = { en: "Speed", zh: "速度" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the speed tooltip shows each model whole, its numbers in columns", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) for (const width of [1100, 420]) {
      await t.test(lang + " " + width, async () => {
        const errors = [];
        const ctx = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
        const p = await ctx.newPage();
        p.setDefaultTimeout(5000);
        p.setDefaultNavigationTimeout(60000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.route("**/*", server(lang));
        await p.goto("http://magpie.test/");
        await p.locator('[data-view="usage"]').first().click();
        await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
        await p.locator("#ledWrap .led tbody tr").first().waitFor();
        await p.waitForTimeout(300);
        await reader.click(p, p.locator("#ledMetric .opt").filter({ hasText: SPEED[lang] }));
        await p.waitForTimeout(150);
        // at 420 the metric switch (Traffic its fifth) wraps the chart-kind
        // toggle under it, and the chart starts below the window
        const col = p.locator("#ledChart rect.col.all").first();
        const view = p.viewportSize();
        const clear = async () => { const b = await col.boundingBox(); return b.y + b.height + 60 < view.height; };
        if (!(await clear())) {
          await p.mouse.move(view.width / 2, view.height / 2);
          await p.mouse.wheel(0, 300);
          // hover once the column is clear of the window's bottom (and the
          // table's scroller pinned there)
          for (const end = Date.now() + 5000; !(await clear()); await p.waitForTimeout(50)) {
            if (Date.now() > end) throw new Error("the chart never scrolled into the window");
          }
        }
        const box = await p.locator("#ledChart rect.col.all").first().boundingBox();
        await p.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
        const tip = p.locator("#ledChart .tip");
        await tip.waitFor();
        const got = await tip.evaluate((tip) => {
          const r = (e) => e.getBoundingClientRect();
          const rows = [...tip.querySelectorAll(":scope > div:not(.sum)")].map((d) => {
            const [name, speed, ttft] = [d.querySelector("span"), ...d.querySelectorAll("em")];
            return {
              name: name.textContent, cut: name.scrollWidth > name.clientWidth + 1,
              speed: speed && r(speed).right, ttft: ttft && r(ttft).right, ttftText: ttft?.textContent || "",
              top: r(speed).top, nameTop: r(name).top,
            };
          });
          const sum = tip.querySelector(".sum");
          const lh = parseFloat(getComputedStyle(tip.querySelector(":scope > div:not(.sum) em")).lineHeight);
          const sumLines = sum ? Math.max(...[...sum.querySelectorAll("em")].map((e) => Math.round(r(e).height / lh))) : 0;
          const plot = r(tip.parentElement), tr = r(tip);
          return { rows, sumLines, inside: tr.left >= plot.left - 1 && tr.right <= plot.right + 1, win: tr.right <= innerWidth + 1 };
        });
        const where = lang + " " + width + " " + JSON.stringify(got);
        assert.equal(got.rows.length, MODELS.length, where);
        for (const m of MODELS) {
          const r = got.rows.find((x) => x.name === m.id);
          assert(r, "row for " + m.id + ": " + where);
          assert(!r.cut, m.id + " is cut short: " + where);
          assert(r.ttftText.length > 0, m.id + " has its first token: " + where);
        }
        // the speeds end in one column and the first tokens in another
        const ends = (k) => new Set(got.rows.map((x) => Math.round(x[k])));
        assert.equal(ends("speed").size, 1, "speeds line up: " + where);
        assert.equal(ends("ttft").size, 1, "first tokens line up: " + where);
        assert(got.rows.every((x) => x.ttft > x.speed), "first token after speed: " + where);
        assert.equal(got.sumLines, 1, "the period's numbers on one line: " + where);
        assert(got.inside && got.win, "the tooltip stays in the chart: " + where);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
