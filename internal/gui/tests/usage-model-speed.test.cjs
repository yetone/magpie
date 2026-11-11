// Run with Node's test runner and Playwright on the module path; see README.md.
// The model speed ranking on the Usage page's Requests tab (@flying_crp on X:
// a ranking of how fast each model answers): each model's median speed and
// first-token wait and its request count, the fastest first, sortable by each
// column, a model with nothing measured always last, in every language, at a
// phone's width without scrolling sideways, and gone when no request counted.
// No backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();

const row = (i, model, out, ttft) => ({
  route_id: 700 + i, t: new Date(now - (i + 1) * 60e3).toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color",
  provider: "zai", providerName: "Z.ai Coding Plan", host: "me@example.com", req: "claude-opus-4-1-20250805", model, served: model,
  effort: "high", in: 1000, out, cache_read: 0, cache_write: 0, ms: 3000, ttft_ms: ttft, status: 200, rid: "req_" + i, ep: "/v1/messages", cost: 0.01, priced: true,
});
const ROWS = [row(0, "glm-fast", 135, 600), row(1, "kimi-slow", 120, 2100), row(2, "burst-model", 200, 400)];
// the ranking the ledger sends: glm-fast 75 tok/s, kimi-slow 10, and one whose
// replies came in a burst, a first token and no speed
const SPEEDS = [
  { model: "glm-fast", name: "GLM Fast", requests: 28, tps: 75.4, tps_n: 27, ttft_ms: 600 },
  { model: "kimi-slow", name: "kimi-slow", requests: 3, tps: 10.2, tps_n: 3, ttft_ms: 2100 },
  { model: "burst-model", name: "burst-model", requests: 5, ttft_ms: 400 },
];
const FAST = { id: "glm-fast", calls: 28, input: 1, output: 3780, cache_read: 0, cache_write: 0, cost: 0.3, timed: 27, ttft_ms: 27 * 600, decode_ms: 27 * 1800, decode_out: 27 * 135 };

let speeds = SPEEDS;
function ledger(period) {
  return {
    period, rows: ROWS, offset: 0, total: ROWS.length, calls: ROWS.length, errors: 0,
    input: 1, output: 3780, cache_read: 0, cache_write: 0, cost: 0.3, unpriced: 0,
    timed: 27, ttft_ms: 27 * 600, decode_ms: 27 * 1800, decode_out: 27 * 135,
    by: { model: [FAST] }, bucket: "hour", series: [],
    agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }],
    speeds, speed_min: 100,
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
  en: { title: "Model speed", heads: ["Model", "Speed", "First token", "Requests"], fast: "75 tok/s", slow: "10 tok/s", wait: "600 ms" },
  zh: { title: "模型速度", heads: ["模型", "速度", "首字耗时", "请求"], fast: "75 token/秒", slow: "10 token/秒", wait: "600 毫秒" },
  "zh-TW": { title: "模型速度", heads: ["模型", "速度", "首字耗時", "請求"], fast: "75 token/秒", slow: "10 token/秒", wait: "600 毫秒" },
  ja: { title: "モデルの速度", heads: ["モデル", "速度", "最初のトークン", "リクエスト"], fast: "75", slow: "10", wait: "600" },
  de: { title: "Modelltempo", heads: ["Modell", "Tempo", "Erstes Token", "Anfragen"], fast: "75", slow: "10", wait: "600" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the model speed ranking", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) {
      const w = L[lang];
      await t.test(lang, async () => {
        speeds = SPEEDS;
        const errors = [];
        // a phone's width, then the window's
        const ctx = await browser.newContext({ viewport: { width: 400, height: 800 }, reducedMotion: "reduce" });
        const p = await ctx.newPage();
        p.setDefaultTimeout(5000);
        p.setDefaultNavigationTimeout(60000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.route("**/*", server(lang));
        await p.goto("http://magpie.test/");
        await p.locator('[data-view="usage"]').first().click();
        await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
        await p.locator("#ledSpeed .led-spd tbody tr").first().waitFor();
        await p.waitForTimeout(200);

        const card = p.locator("#ledSpeed");
        assert.equal((await card.locator(".led-spd-t").textContent()).trim(), w.title);
        const heads = await card.locator("thead th").allTextContents();
        assert.deepEqual(heads.map((x) => x.replace(/[↑↓]/g, "").trim()), w.heads);
        const names = () => card.locator("tbody td.nm").allTextContents();
        const sortOf = () => card.locator("thead th").evaluateAll((ths) => ths.map((th) => th.getAttribute("aria-sort")));
        const head = (i) => card.locator("thead th").nth(i).locator("button");

        // the fastest first; the one with no speed last
        assert.deepEqual(await names(), ["GLM Fast", "kimi-slow", "burst-model"]);
        assert.deepEqual(await sortOf(), ["none", "descending", "none", "none"]);
        const first = await card.locator("tbody tr").first().locator("td").allTextContents();
        assert(first[1].includes(w.fast), "the speed: " + first[1]);
        assert(first[2].includes(w.wait), "the first token: " + first[2]);
        assert.equal(first[3].trim(), "28");
        assert.equal((await card.locator("tbody tr").nth(2).locator("td").nth(1).textContent()).trim(), "—", "burst-model: no speed measured");

        // the same column again turns it: slowest first, the unmeasured still last
        await head(1).click();
        assert.deepEqual(await names(), ["kimi-slow", "GLM Fast", "burst-model"]);
        assert.deepEqual(await sortOf(), ["none", "ascending", "none", "none"]);
        // the first token's wait: shortest first
        await head(2).click();
        assert.deepEqual(await names(), ["burst-model", "GLM Fast", "kimi-slow"]);
        // the request count: most first
        await head(3).click();
        assert.deepEqual(await names(), ["GLM Fast", "burst-model", "kimi-slow"]);
        assert.deepEqual(await sortOf(), ["none", "none", "none", "descending"]);
        // the name: A to Z
        await head(0).click();
        assert.deepEqual(await names(), ["burst-model", "GLM Fast", "kimi-slow"]);
        assert.equal(await p.evaluate(() => document.activeElement?.classList.contains("led-spd-h")), true, "the focus stays on the header");

        // at a phone's width the card and the page don't scroll sideways
        const fits = await card.evaluate((e) => ({ card: e.scrollWidth <= e.clientWidth + 1, scroll: e.querySelector(".led-spd-scroll").scrollWidth <= e.querySelector(".led-spd-scroll").clientWidth + 1, doc: document.documentElement.scrollWidth <= window.innerWidth + 1 }));
        assert.deepEqual(fits, { card: true, scroll: true, doc: true });
        // a long model name is cut, not the numbers
        const cut = await card.locator("tbody td.nm").first().evaluate((e) => getComputedStyle(e).textOverflow);
        assert.equal(cut, "ellipsis");

        // nothing counted: the card is gone
        speeds = [];
        await p.reload();
        await p.locator('[data-view="usage"]').first().click();
        await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
        await p.locator("#ledWrap .led tbody tr").first().waitFor();
        await p.waitForTimeout(200);
        assert.equal(await card.isVisible(), false, "no ranking without a counted request");
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
