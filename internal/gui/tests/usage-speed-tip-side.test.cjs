// Run with Node's test runner and Playwright on the module path; see README.md.
// The Usage page's Requests chart, Speed: the tooltip is laid out the same
// whichever way the pointer comes to a column (lwjef on X: once the current
// hour's column had speeds, coming onto it from the right wrapped the
// tooltip's names). The tooltip's width was read where the last column left
// it, so a narrow tooltip just right of a middle column squeezed the next,
// wider one into what was left of the chart, and it was put beside the
// latest column at that squeezed width. Both engines, every language, wide
// and narrow; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets");
const now = Date.now();
const H = 3600e3;
const hour = (i) => new Date(Math.floor(now / H) * H - i * H).toISOString();
const N = 24;
const MID = 10; // hours ago: a column with one short-named model

const stats = (m) => ({ ...m, input: 1, cache_read: 0, cache_write: 0, cost: 0.1, timed: m.calls, decode_out: m.output });
// the current hour's column: long names, as a reporter's models are
const LATEST = [
  { id: "claude-sonnet-5-20260929-thinking", calls: 41, output: 120000, ttft_ms: 41 * 1830, decode_ms: 41 * 2400 },
  { id: "gpt-6.1-sol-codex-max-high", calls: 17, output: 30000, ttft_ms: 17 * 640, decode_ms: 17 * 3000 },
  { id: "deepseek-v4.1-flash", calls: 9, output: 40000, ttft_ms: 9 * 420, decode_ms: 9 * 1500 },
].map(stats);
const SHORT = [{ id: "glm-5", calls: 3, output: 900, ttft_ms: 3 * 500, decode_ms: 3 * 300 }].map(stats);
const sum = (list, k) => list.reduce((a, m) => a + m[k], 0);
const timing = (list) => ({ timed: sum(list, "timed"), ttft_ms: sum(list, "ttft_ms"), decode_ms: sum(list, "decode_ms"), decode_out: sum(list, "decode_out") });
const part = (x) => ({ calls: x.calls, tokens: x.output, cost: x.cost, timed: x.timed, ttft_ms: x.ttft_ms, decode_ms: x.decode_ms, decode_out: x.decode_out });
const point = (i, list) => ({
  label: "", time: hour(i), calls: sum(list, "calls"), input: list.length, output: sum(list, "output"), ...timing(list),
  by: { model: Object.fromEntries(list.map((m) => [m.id, part(m)])) },
});
const ALL = [...LATEST, ...SHORT];
const row = (i, model) => ({
  route_id: 900 + i, t: new Date(now - (i + 1) * 60e3).toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color",
  provider: "zai", providerName: "Z.ai Coding Plan", req: model, model, served: model, in: 1000, out: 258, cache_read: 0, cache_write: 0,
  ms: 1762, ttft_ms: 762, status: 200, rid: "req_" + i, ep: "/v1/messages", cost: 0.01, priced: true,
});

function ledger(period) {
  return {
    period, rows: ALL.map((m, i) => row(i, m.id)), offset: 0, total: ALL.length, calls: sum(ALL, "calls"), errors: 0,
    input: ALL.length, output: sum(ALL, "output"), cache_read: 0, cache_write: 0, cost: sum(ALL, "cost"), unpriced: 0, ...timing(ALL),
    by: { model: ALL }, bucket: "hour",
    series: Array.from({ length: N }, (_, k) => {
      const i = N - 1 - k;
      return point(i, i === 0 ? LATEST : i === MID ? SHORT : []);
    }),
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

const SPEED = { en: "Speed", zh: "速度", "zh-TW": "速度", ja: "速度", de: "Tempo" };

// the tooltip as it is, beside a copy laid out with the whole chart's room:
// the two are the same size, or the tooltip was squeezed where it stands
function look(tip) {
  const copy = tip.cloneNode(true);
  copy.style.left = "0px";
  copy.style.visibility = "hidden";
  tip.parentElement.append(copy);
  const r = tip.getBoundingClientRect(), c = copy.getBoundingClientRect();
  const lines = (box) => [...box.querySelectorAll(":scope > div:not(.sum) > span")].map((s) => {
    const lh = parseFloat(getComputedStyle(s).lineHeight);
    return Math.round(s.getBoundingClientRect().height / lh);
  });
  const names = lines(tip), whole = lines(copy);
  copy.remove();
  // the pointed column: its slot's middle, as wide as a column is drawn
  const slot = tip.parentElement.querySelector("rect.hot:not([style*='none'])")?.getBoundingClientRect();
  const bw = tip.parentElement.querySelector("rect.col.all")?.getBoundingClientRect().width || 0;
  const plot = tip.parentElement.getBoundingClientRect();
  const mid = slot && (slot.left + slot.right) / 2 - plot.left;
  return {
    w: r.width, h: r.height, copyW: c.width, copyH: c.height, names, whole, left: r.left - plot.left, right: r.right - plot.left,
    plotW: plot.width, hot: slot && { left: mid - bw / 2, right: mid + bw / 2 },
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the speed tooltip is the same from either side", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1400, 1100, 820, 420]) {
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
        const cols = p.locator("#ledChart rect.col.all");
        await cols.nth(1).waitFor();
        const view = p.viewportSize();
        const clear = async () => { const b = await cols.first().boundingBox(); return b.y + b.height + 60 < view.height; };
        if (!(await clear())) {
          await p.mouse.move(view.width / 2, view.height / 2);
          await p.mouse.wheel(0, 300);
          for (const end = Date.now() + 5000; !(await clear()); await p.waitForTimeout(50)) {
            if (Date.now() > end) throw new Error("the chart never scrolled into the window");
          }
        }
        const svg = await p.locator("#ledChart svg").first().boundingBox();
        const mid = await cols.nth(0).boundingBox(), latest = await cols.nth(1).boundingBox();
        const y = latest.y + latest.height / 2;
        const tip = p.locator("#ledChart .tip");
        const where = (how, got) => `${engine} ${lang} ${width} ${how}: ${JSON.stringify(got)}`;
        const check = async (how) => {
          if (await tip.isHidden()) return;
          const got = await tip.evaluate(look);
          assert(Math.abs(got.w - got.copyW) <= 1 && Math.abs(got.h - got.copyH) <= 1, "laid out as with the whole chart's room: " + where(how, got));
          assert.deepEqual(got.names, got.whole, "names wrap as with the whole chart's room: " + where(how, got));
          // wide enough for the names: none of them wraps at all
          if (width >= 820) assert(got.names.every((n) => n === 1), "every name on one line: " + where(how, got));
          assert(got.left >= -1 && got.right <= got.plotW + 1, "inside the chart: " + where(how, got));
          // where there is room on a side, the tooltip leaves the column clear
          const room = got.hot && (got.hot.left >= got.w + 14 || got.plotW - got.hot.right >= got.w + 14);
          if (room) assert(got.right <= got.hot.left + 1 || got.left >= got.hot.right - 1, "the column left clear: " + where(how, got));
        };

        // the reporter's way: a short tooltip right of a middle column, the
        // pointer out of the chart, then onto the current hour from the right
        await p.mouse.move(mid.x + mid.width / 2, y);
        await tip.waitFor();
        await p.mouse.move(mid.x + mid.width / 2, svg.y - 40);
        await tip.waitFor({ state: "hidden" });
        await p.mouse.move(svg.x + svg.width + 30, y);
        await p.mouse.move(svg.x + svg.width - 2, y);
        await tip.waitFor();
        await check("into the latest from the right");
        await p.mouse.move(latest.x + latest.width / 2, y);
        await check("on the latest from the right");

        // every column, sweeping right to left and back again
        const step = Math.max(4, latest.width / 2);
        for (let x = svg.x + svg.width - 1; x > svg.x; x -= step) { await p.mouse.move(x, y); await check("leftward at " + Math.round(x - svg.x)); }
        for (let x = svg.x + 1; x < svg.x + svg.width; x += step) { await p.mouse.move(x, y); await check("rightward at " + Math.round(x - svg.x)); }
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
