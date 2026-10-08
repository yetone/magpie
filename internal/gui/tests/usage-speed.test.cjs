// Run with Node's test runner and Playwright on the module path; see README.md.
// How fast the replies came, on the Usage page's Requests tab (huoranxuanyuan,
// #860): each row's speed in tokens a second after its first, a KPI of the
// period's with its first-token time, a Speed trend that sets each model's
// beside the others', and the ranking's tok/s. At the reporter's own window
// (975px, 125%) the table fits without scrolling sideways, the columns it
// leaves out in each row's details; and a wide window keeps the Agents and
// Settings rows a readable width rather than stretched across it. English
// and Chinese; no backend, the API is faked.
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

const row = (i, model, out, ttft) => ({
  route_id: 700 + i, t: new Date(now - (i + 1) * 60e3).toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color",
  provider: "zai", providerName: "Z.ai Coding Plan", host: "me@example.com", req: "claude-opus-4-1-20250805", model, served: model + "-2026",
  effort: "high", in: 41200 + i, out, cache_read: 30000, cache_write: 2000, ms: 600 + 1800 * (model === "glm-fast" ? 1 : 0) + (model === "kimi-slow" ? 2000 : 0),
  ttft_ms: ttft, status: 200, rid: "req_" + i, ep: "/v1/messages", cost: 0.0123, priced: true,
});
// glm-fast: 135 tokens in the 1.8 s after its first, 75 tok/s; kimi-slow: 20
// in 2 s, 10 tok/s; the last wasn't streamed, so it has no speed
const ROWS = [row(0, "glm-fast", 135, 600), row(1, "kimi-slow", 20, 600), row(2, "glm-fast", 135, 600)];
ROWS.push({ ...row(3, "glm-fast", 135, 0), ttft_ms: undefined, ms: 3000 });
for (let i = 4; i < 30; i++) ROWS.push(row(i, "glm-fast", 135, 600));

const FAST = { id: "glm-fast", calls: 28, input: 1, output: 3780, cache_read: 0, cache_write: 0, cost: 0.3, timed: 27, ttft_ms: 27 * 600, decode_ms: 27 * 1800, decode_out: 27 * 135 };
const SLOW = { id: "kimi-slow", calls: 1, input: 1, output: 20, cache_read: 0, cache_write: 0, cost: 0.01, timed: 1, ttft_ms: 600, decode_ms: 2000, decode_out: 20 };
const part = (x) => ({ calls: x.calls, tokens: x.output, cost: x.cost, timed: x.timed, ttft_ms: x.ttft_ms, decode_ms: x.decode_ms, decode_out: x.decode_out });
const sum = (k) => FAST[k] + SLOW[k];

function ledger(period) {
  return {
    period, rows: ROWS, offset: 0, total: ROWS.length, calls: ROWS.length, errors: 0,
    input: sum("input"), output: sum("output"), cache_read: 0, cache_write: 0, cost: sum("cost"), unpriced: 0,
    // 3665 tokens in 50.6 s after the first: 72 tok/s
    timed: sum("timed"), ttft_ms: sum("ttft_ms"), decode_ms: sum("decode_ms"), decode_out: sum("decode_out"),
    by: { model: [FAST, SLOW] }, bucket: "hour",
    series: [
      { label: "", time: hour(1), calls: 14, input: 1, output: 1890, ...{ timed: 14, ttft_ms: 8400, decode_ms: 25200, decode_out: 1890 }, by: { model: { "glm-fast": part({ ...FAST, calls: 14, output: 1890, timed: 14, ttft_ms: 8400, decode_ms: 25200, decode_out: 1890 }) } } },
      // hour(0): the hour's own is 1910 tokens in 27.4 s (69.7 tok/s) while
      // glm-fast answered 1755 in 23.4 s (75.0), so its mark leaves the column
      // and gets a leader line; kimi-slow (10) stays under it. hour(1) is the
      // hour's own equal to the one model that answered in it, so no line
      { label: "", time: hour(0), calls: 15, input: 1, output: 1910, ...{ timed: 14, ttft_ms: 8400, decode_ms: 27400, decode_out: 1910 }, by: { model: { "glm-fast": part({ ...FAST, calls: 14, output: 1890, timed: 13, ttft_ms: 7800, decode_ms: 23400, decode_out: 1755 }), "kimi-slow": part(SLOW) } } },
    ],
    agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }],
  };
}

const STATE = (lang) => ({
  agents: [
    { id: "claude", name: "Claude Code", path: "/t/c.json", icon: "claudecode-color", wired: true, fields: [{ key: "model", label: "model", value: "a", options: [{ value: "a", label: "Claude Sonnet 4.5", ref: "claude/a" }] }] },
    { id: "codex", name: "Codex", path: "/t/c.toml", icon: "codex-color", wired: true, fields: [{ key: "model", label: "model", value: "b", options: [{ value: "b", label: "GPT", ref: "openai/b" }] }] },
  ],
  profiles: [], settings: { lang, theme: "light" }, fx: { rate: 7.2, at: new Date().toISOString() },
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
  en: { speed: "Speed", kpi: "Output speed", fast: "75 tok/s", slow: "10 tok/s", all: "72 tok/s", first: "first token in 600 ms on average", sent: "Sent", hint: "Sent, Effort and the cache are in each row's details" },
  zh: { speed: "速度", kpi: "输出速度", fast: "75 token/秒", slow: "10 token/秒", all: "72 token/秒", first: "首字平均 600 毫秒", sent: "发送模型", hint: "窗口较窄：发送模型、推理强度和缓存在每行的详情里" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": how fast the replies came, and the window's widths", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      const w = L[lang];
      await t.test(lang, async () => {
        const errors = [];
        // the reporter's window: 1220×973 at 125%
        const ctx = await browser.newContext({ viewport: { width: 975, height: 778 }, reducedMotion: "reduce" });
        const p = await ctx.newPage();
        p.setDefaultTimeout(5000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.route("**/*", server(lang));
        await p.goto("http://magpie.test/");
        assert(await p.evaluate(() => document.body.classList.contains("window")), "the app's window");
        await p.locator('[data-view="usage"]').first().click();
        await p.locator("#usageTab .opt").nth(1).click();
        await p.locator("#ledWrap .led tbody tr").first().waitFor();
        await p.waitForTimeout(300);

        // each row's speed, a column of its own; "—" for one not streamed
        const heads = await p.locator(".led thead th").evaluateAll((ths) => ths.filter((th) => th.offsetParent).map((th) => th.textContent));
        const at = heads.indexOf(w.speed);
        assert(at > 0, "a Speed column: " + heads.join(", "));
        const cell = (i) => p.locator(".led tbody tr.led-row").nth(i).locator("td").filter({ visible: true }).nth(at);
        assert.equal((await cell(0).textContent()).trim(), w.fast);
        assert.equal((await cell(1).textContent()).trim(), w.slow);
        assert.equal((await cell(3).textContent()).trim(), "—");

        // the table fits the window; what it leaves out is in the details
        const wrap = p.locator("#ledWrap");
        assert(await wrap.evaluate((e) => e.scrollWidth <= e.clientWidth + 1), "the table fits: " + JSON.stringify(await wrap.evaluate((e) => [e.scrollWidth, e.clientWidth])));
        assert.equal(await p.locator("#ledHScroll").isVisible(), false, "no sideways scrollbar");
        assert.equal((await p.locator("#ledTight").textContent()).trim(), w.hint);
        assert(await p.locator("#ledTight").isVisible(), "says where the columns went");
        await reader.click(p, p.locator(".led tbody tr.led-row").nth(0).locator("td").nth(2));
        const d = p.locator(".led tbody tr.led-detail");
        const shown = await d.locator("dt").evaluateAll((dts) => dts.filter((x) => x.offsetParent).map((x) => x.textContent));
        assert(shown.includes(w.sent), "the details say what was sent: " + shown.join(", "));
        assert(shown.includes(w.speed), "and the speed: " + shown.join(", "));
        await reader.click(p, p.locator(".led tbody tr.led-row").nth(0).locator("td").nth(2));

        // the period's speed and its first-token time
        const kpi = p.locator("#ledKpi .blk").filter({ hasText: w.kpi });
        assert.equal((await kpi.locator(".v").textContent()).trim(), w.all);
        assert.equal((await kpi.locator(".sub").textContent()).trim(), w.first);
        // the ranking says each one's speed
        assert((await p.locator("#ledRank .rk").first().locator(".rk-b").textContent()).includes(w.fast), "the ranking's tok/s");

        // the trend by speed: each model's mark, the fastest ranked first
        await reader.click(p, p.locator("#ledMetric .opt").filter({ hasText: w.speed }));
        await p.waitForTimeout(150);
        assert.equal((await p.locator("#ledRank .rk").first().locator(".rk-nm").textContent()).trim(), "glm-fast");
        assert.equal((await p.locator("#ledRank .rk").first().locator(".rk-val").textContent()).trim(), w.fast);
        assert.equal((await p.locator("#ledRank .rk").nth(1).locator(".rk-val").textContent()).trim(), w.slow);
        assert.equal(await p.locator("#ledChart rect.col.mark").count(), 3, "a mark for each model at each hour it answered");
        assert.equal(await p.locator("#ledChart rect.col.all").count(), 2, "and the hour's own");
        // a model faster than the hour's own leaves its mark above the column:
        // a dotted leader line ties the two, so the mark reads as this hour's
        // rather than as a stray dash (huoranxuanyuan, #860). The hour(0)
        // fixture is the one where that happens: 1755 tokens in 23.4 s is
        // 75.0 tok/s for glm-fast against the hour's own 1910 in 27.4 s,
        // 69.7, while kimi-slow answers 10
        const stems = p.locator("#ledChart line.stem");
        assert.equal(await stems.count(), 1, "a leader line for the mark that leaves its column");
        const stem = await stems.first().evaluate((l) => ({ x1: l.x1.baseVal.value, x2: l.x2.baseVal.value, y1: l.y1.baseVal.value, y2: l.y2.baseVal.value }));
        const mark = await p.locator("#ledChart rect.col.mark").nth(1).evaluate((r) => ({ x: r.x.baseVal.value, y: r.y.baseVal.value, w: r.width.baseVal.value, h: r.height.baseVal.value }));
        const col = await p.locator("#ledChart rect.col.all").nth(1).evaluate((r) => ({ y: r.y.baseVal.value, color: r.dataset.color, style: r.style.fill }));
        assert(Math.abs(stem.x1 - (mark.x + mark.w / 2)) < 0.51 && Math.abs(stem.x2 - stem.x1) < 0.01, "the line runs down the mark's middle");
        assert(Math.abs(stem.y1 - col.y) < 0.01, "and starts at its column's top");
        assert(Math.abs(stem.y2 - (mark.y + mark.h)) < 0.01, "and ends at the mark's foot");
        assert(stem.y1 > stem.y2, "downward: the mark is above its column");
        assert.equal(await stems.first().evaluate((l) => getComputedStyle(l).strokeDasharray), "2px, 2px", "dotted");
        // the column is the track the marks sit on, not the stacked charts'
        // "Other" grey, and the colour goes through style, where var()
        // substitutes — not through the attribute, where it does not
        assert.equal(col.color, "var(--pill)", "the track's colour, named");
        assert.notEqual(col.style, "", "set through style");
        const fill = await p.locator("#ledChart rect.col.all").nth(1).evaluate((r) => getComputedStyle(r).fill);
        assert(fill.startsWith("rgb"), "the style resolves to a colour, not the var() text: " + fill);
        assert.equal(fill, await p.locator("#ledChart rect.col.all").nth(0).evaluate((r) => getComputedStyle(r).fill), "both hours' tracks read the same");

        // a wide window: the Agents and Settings rows don't stretch across it
        await p.setViewportSize({ width: 1670, height: 1060 });
        await p.locator('[data-view="agents"]').first().click();
        await p.locator(".row.agent").first().waitFor();
        await p.waitForTimeout(200);
        const centred = async (sel, what) => {
          const view = await p.locator(sel).first().evaluate((e) => { const v = e.closest(".view").getBoundingClientRect(), r = e.getBoundingClientRect(); return { w: r.width, l: r.left - v.left, r: v.right - r.right }; });
          assert(view.w <= 1200, what + " at most 1200 wide: " + JSON.stringify(view));
          assert(Math.abs(view.l - view.r) < 24, what + " in the middle: " + JSON.stringify(view));
        };
        await centred(".row.agent", "an agent's row");
        await p.locator("#prefs").click();
        await p.locator("#view-settings .row.pref").first().waitFor();
        await p.waitForTimeout(200);
        await centred("#view-settings .row.pref", "a setting's row");
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
