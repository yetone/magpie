// Run with Node's test runner and Playwright on the module path; see README.md.
// The Usage page's Requests table fits a narrow window with the reporter's
// own rows (#860, huoranxuanyuan, a second round: at 1569px on Windows at
// 175%, about 900 CSS px, the hint said columns were left out and Speed and
// Status were still cut off). Their rows are Codex's through openai ·
// chatgpt.com with a Responses badge, one priced from another model, whose
// "Price reference" line widened the Cost column. Where leaving out Sent,
// Effort and the cache isn't enough, that line goes too (it is in the cost's
// tooltip and the row's details) and names are cut shorter; a window wide
// enough keeps it. Chromium and WebKit, English and Chinese; the API is faked.
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

// the reporter's rows, from their screenshot
const R = (i, req, inn, out, ms, ttft, ref) => ({ route_id: 900 + i, t: new Date(now - (i + 1) * 60e3).toISOString(), agent: "codex", agentName: "Codex", icon: "codex-color",
  provider: "openai", providerName: "openai", host: "chatgpt.com", ep: "/v1/responses", req, model: req, served: req, effort: "medium",
  in: inn, out, cache_read: 110000, cache_write: 0, ms, ttft_ms: ttft, status: 200, rid: "r" + i, cost: 0.02, priced: true, ...(ref ? { pricing_model: ref } : {}) });
const ROWS = [R(0, "gpt-6.1-sol", 3660, 240, 14000, 11000), R(1, "gpt-6.1-sol", 4102, 145, 11000, 10000), R(2, "codex-auto-review", 4493, 134, 11000, 6400, "gpt-5.6-luna"), R(3, "gpt-6.1-sol", 1030, 93, 8900, 6900)];
const FAST = { id: "gpt-6.1-sol", calls: 3, input: 1, output: 478, cache_read: 0, cache_write: 0, cost: 0.06, timed: 3, ttft_ms: 27900, decode_ms: 6000, decode_out: 478 };
const SLOW = { id: "codex-auto-review", calls: 1, input: 1, output: 134, cache_read: 0, cache_write: 0, cost: 0.02, timed: 1, ttft_ms: 6400, decode_ms: 4600, decode_out: 134 };
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

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: the reporter's rows fit a narrow window`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const errors = [];
      const open = async (width) => {
        const p = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        p.setDefaultTimeout(5000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.route("**/*", server(lang));
        await p.goto("http://magpie.test/");
        await p.locator('[data-view="usage"]').first().click();
        await p.locator("#usageTab .opt").nth(1).click();
        await p.locator("#ledWrap .led tbody tr").first().waitFor();
        await p.waitForTimeout(300);
        return p;
      };
      const fits = (p) => p.locator("#ledWrap").evaluate((e) => {
        const box = e.getBoundingClientRect(), last = [...e.querySelectorAll("thead th")].filter((th) => th.offsetParent).at(-1).getBoundingClientRect();
        return { scroll: e.scrollWidth <= e.clientWidth + 1, status: last.right <= box.right + 1 };
      });

      // narrower than the table is with Sent, Effort and the cache left out
      const p = await open(820);
      assert.deepEqual(await fits(p), { scroll: true, status: true }, "the table fits, Status in sight");
      assert(await p.locator("#ledTight").isVisible(), "the hint says where the columns went");
      const ref = p.locator(".led tbody tr.led-row").nth(2).locator("td.cost");
      assert.equal(await ref.locator(".price-reference").isVisible(), false, "the price reference line is left out");
      assert((await ref.getAttribute("title")).includes("gpt-5.6-luna"), "and is in the cost's tooltip");
      await reader.click(p, p.locator(".led tbody tr.led-row").nth(2).locator("td").nth(2));
      assert((await p.locator(".led tbody tr.led-detail").textContent()).includes("gpt-5.6-luna"), "and in the row's details");

      // wide enough: the line is there
      const q = await open(1100);
      assert.deepEqual(await fits(q), { scroll: true, status: true });
      assert(await q.locator(".led tbody tr.led-row").nth(2).locator(".price-reference").isVisible(), "the price reference line at a wider window");
      assert.deepEqual(errors, []);
    });
  }
}
