// Run with Node's test runner and Playwright on the module path; see README.md.
// The speed chart on the Usage page's Requests tab draws every model timed
// at a point, over Today, 7 days and 30 days (zzzkl3994 on Discord: five
// models used today, one mark on today's column over 7 or 30 days). Its
// colours went to the period's fastest models, a few replies each on an
// older day, and a speed has no "Other" column, so today's models were drawn
// nowhere. Now the colours go to the most timed, and a model past them is a
// faint mark of its own; the tooltip names every one, and the ranking's
// colours are the chart's. Columns and lines, both engines, wide and narrow,
// every language; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = process.env.MAGPIE_SPEED_ALL_ASSETS || path.resolve(__dirname, "../assets");
const D = 86400e3, H = 3600e3, now = Date.now();
const today0 = new Date(new Date(now).setHours(0, 0, 0, 0)).getTime();

// today's five, many replies each at a usual speed
const TODAY = ["glm-5.1", "kimi-k3", "deepseek-v4.1", "qwen4-coder", "minimax-m3"].map((id, i) => ({ id, calls: 40 - i * 5, speed: 60 + i * 10 }));
// eight fast ones of a reply or two, four days ago: the period's fastest
const FAST = Array.from({ length: 8 }, (_, i) => ({ id: "fast-" + (i + 1), calls: 1 + (i % 2), speed: 300 + i * 20 }));

const timing = (calls, speed) => ({ timed: calls, ttft_ms: calls * 700, decode_ms: calls * 2000, decode_out: Math.round(calls * 2 * speed), decode_n: calls });
const share = (m) => ({ id: m.id, name: m.id, calls: m.calls, input: m.calls * 1000, output: Math.round(m.calls * 2 * m.speed), cache_read: 0, cache_write: 0, cost: 0, ...timing(m.calls, m.speed) });
const part = (m) => { const s = share(m); return { calls: s.calls, tokens: s.input + s.output, cost: 0, timed: s.timed, ttft_ms: s.ttft_ms, decode_ms: s.decode_ms, decode_out: s.decode_out }; };
const sum = (ms) => ms.reduce((a, m) => {
  const s = share(m);
  for (const k of ["calls", "input", "output", "timed", "ttft_ms", "decode_ms", "decode_out", "decode_n"]) a[k] = (a[k] || 0) + s[k];
  return a;
}, { cache_read: 0, cache_write: 0, cost: 0 });
const point = (time, ms) => ({ label: "", time: new Date(time).toISOString(), ...sum(ms), by: { model: Object.fromEntries(ms.map((m) => [m.id, part(m)])) } });

const row = (m, i) => ({
  route_id: 900 + i, t: new Date(now - (i + 1) * 60e3).toISOString(), agent: "claude", agentName: "Claude Code", provider: "zai", providerName: "Z.ai",
  req: m.id, model: m.id, served: m.id, in: 1000, out: 258, cache_read: 0, cache_write: 0, ms: 1762, ttft_ms: 762, status: 200, rid: "req_" + i, ep: "/v1/messages", cost: 0.01, priced: true,
});

function ledger(period) {
  const days = period === "30d" ? 30 : period === "7d" ? 7 : 0;
  const models = days ? [...TODAY, ...FAST] : TODAY;
  const series = days
    ? Array.from({ length: days }, (_, i) => { const at = today0 - (days - 1 - i) * D; return point(at, at === today0 ? TODAY : at === today0 - 4 * D ? FAST : []); })
    : Array.from({ length: 24 }, (_, i) => { const at = today0 + i * H; return point(at, at <= now && at + H > now ? TODAY : []); });
  return {
    period, rows: TODAY.map(row), offset: 0, total: TODAY.length, ...sum(models), errors: 0, unpriced: 0,
    by: { model: models.map(share) }, bucket: days ? "day" : "hour", series, agents: [],
  };
}

function server(lang, answered) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/usage/requests") {
      await route.fulfill({ json: ledger(url.searchParams.get("period")) });
      answered.push(url.searchParams.get("period"));
      return;
    }
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

const PERIOD_AT = { today: 0, "7d": 1, "30d": 2 };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the speed chart draws every model timed at a point", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1100, 420]) {
      await t.test(lang + " " + width, async () => {
        const errors = [];
        const ctx = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
        const p = await ctx.newPage();
        p.setDefaultTimeout(5000);
        p.setDefaultNavigationTimeout(60000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.addInitScript(() => { try { localStorage.setItem("magpie.ledMetric", "speed"); } catch {} });
        const answered = [];
        await p.route("**/*", server(lang, answered));
        await p.goto("http://magpie.test/");
        await p.locator('[data-view="usage"]').first().click();
        await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
        await p.locator("#ledChart .plot svg").waitFor();
        for (const style of ["columns", "lines"]) for (const period of ["today", "7d", "30d"]) {
          const where = `${lang} ${width} ${style} ${period}`;
          if (period === "today") {
            await p.locator(`#ledStyle .opt[data-style="${style === "lines" ? "line" : "bars"}"]`).evaluate((b) => b.click());
            await p.locator(style === "lines" ? "#ledChart g.col.ln" : "#ledChart rect.col.mark").first().waitFor({ state: "attached" });
          }
          const asked = answered.length;
          await p.locator("#period .opt").nth(PERIOD_AT[period]).evaluate((b) => b.click());
          for (let i = 0; i < 100 && !answered.slice(asked).includes(period); i++) await p.waitForTimeout(50);
          assert(answered.includes(period), "asked for " + period + ": " + where);
          await p.waitForTimeout(300);
          // the last column's marks: one for each of today's models (columns),
          // a line for every model (lines)
          const got = await p.evaluate(({ style, ids }) => {
            const ks = (sel) => [...document.querySelectorAll(sel)].map((e) => e.dataset.k);
            const rank = [...document.querySelectorAll("#ledRank .rk")].map((b) => ({ name: b.querySelector(".rk-nm")?.textContent, color: b.querySelector(".rk-sw")?.style.background }));
            const marks = style === "lines" ? ks("#ledChart g.col.ln") : (() => {
              const all = [...document.querySelectorAll("#ledChart rect.col.mark")];
              const last = Math.max(...all.map((r) => +r.getAttribute("x")));
              return all.filter((r) => +r.getAttribute("x") === last).map((r) => r.dataset.k);
            })();
            const color = Object.fromEntries([...document.querySelectorAll("#ledChart .col:is(.mark, .ln)")].map((e) => [e.dataset.k, e.dataset.color]));
            return { marks, rank, color, ids };
          }, { style, ids: TODAY.map((m) => m.id) });
          const info = where + " " + JSON.stringify(got);
          for (const m of TODAY) assert(got.marks.includes(m.id), m.id + " is drawn: " + info);
          if (style === "lines" && period !== "today") for (const m of FAST) assert(got.marks.includes(m.id), m.id + " is drawn: " + info);
          // today's models have colours of their own, and the ranking's are the chart's
          for (const m of TODAY) assert.match(got.color[m.id], /--c\d/, m.id + " has a colour: " + info);
          for (const r of got.rank) if (got.color[r.name] && r.color) assert.equal(r.color, got.color[r.name], r.name + "'s colour: " + info);
          // the tooltip on the last column names each of today's models
          if (style === "columns") {
            // the pointer's move, sent to the chart: a phone's head can sit over it
            await p.locator("#ledChart rect.col.all").last().evaluate((r) => {
              const b = r.getBoundingClientRect();
              r.closest("svg").dispatchEvent(new PointerEvent("pointermove", { clientX: b.x + b.width / 2, clientY: b.bottom - 2, bubbles: true }));
            });
            const tip = p.locator("#ledChart .tip");
            await tip.waitFor();
            const names = await tip.locator(":scope > div:not(.sum) span").allTextContents();
            for (const m of TODAY) assert(names.includes(m.id), m.id + " in the tooltip: " + where + " " + JSON.stringify(names));
            await p.locator("#ledChart svg").evaluate((g) => g.dispatchEvent(new PointerEvent("pointerleave")));
          }
        }
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
