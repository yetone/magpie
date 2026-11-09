// Run with Node's test runner and Playwright on the module path; see README.md.
// The speed of a call read from a session file, on the Usage page's Requests
// tab. Claude Code's session file has no first token, only when the line that
// asked for a reply and the reply's last line were written, so its rows had
// no speed at all, and each said "Not streamed", though the replies were
// streamed. A long one now tells the least it wrote at, its tokens over that
// whole time ("≥98 tok/s"); a short one says the file has no first token; a
// call magpie timed keeps its own speed. Claude Code's, Claude Desktop's
// and Codex's files tell one: a Codex call in a turn of several is timed
// from what asked for it, before its first output item, to its last
// (Fallmonkey on Discord). A narrow window shows it as a wide one does. The rows are real ledger rows read
// from Claude Code's session files of claude-opus-5-5 replies; the first,
// 2,558 tokens in 25,987 ms, measured 100.6 tok/s on Claude Code's own stream
// (--include-partial-messages) from its first thinking event to its end.
// English and Chinese; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const at = (i) => new Date(now - (i + 1) * 60e3).toISOString();

const LOG = {
  agent: "claude", provider: "session-unknown", model: "claude-opus-5-5", req: "claude-opus-5-5", served: "claude-opus-5-5",
  in: 2, cache_read: 12380, cache_write: 20122, effort: "medium", status: 0, session: "57956f49-8830-4769-a6c3-93ff87b1fe16",
  cost: 0, priced: false, source: "log", agentName: "Claude Code", icon: "claudecode-color", providerName: "Local session",
};
const GATEWAY = {
  route_id: 900, agent: "claude", agentName: "Claude Code", icon: "claudecode-color", provider: "zai", providerName: "Z.ai Coding Plan",
  host: "me@example.com", req: "claude-opus-4-1-20250805", model: "glm-fast", served: "glm-fast", in: 1200, cache_read: 0, cache_write: 0,
  status: 200, rid: "req_900", ep: "/v1/messages", cost: 0.01, priced: true,
};
const ROWS = [
  // 0: a long reply from a session file: at least 2558 / 25.987 s
  { ...LOG, t: at(0), out: 2558, ms: 25987 },
  // 1: a short one: its tokens over the wait tell too little
  { ...LOG, t: at(1), out: 462, ms: 5340 },
  // 2: a call magpie timed keeps its own speed: 135 tokens in 1.8 s after its first
  { ...GATEWAY, t: at(2), out: 135, ms: 2400, ttft_ms: 600 },
  // 3: a gateway call not streamed is still told so, with the least it
  // wrote at over its whole time: 1500 tokens in 3 s (夜松云 on Discord)
  { ...GATEWAY, route_id: 901, rid: "req_901", t: at(3), out: 1500, ms: 3000 },
  // 4: a session file that timed its turn (Codex's, one call) tells its own: 1200 tokens in 8 s
  { ...LOG, agent: "codex", agentName: "Codex", icon: "codex-color", model: "gpt-6-astra", t: at(4), out: 1200, ms: 10000, ttft_ms: 2000 },
  // 5: a failed call tells none
  { ...LOG, t: at(5), out: 1500, ms: 9000, err_type: "overloaded_error", err: "Overloaded" },
  // 6: a long reply in under a second (lines written together) tells none
  { ...LOG, t: at(6), out: 5000, ms: 300 },
  // 7: a Codex call in a turn of several, timed from what asked for it: at
  // least 1500 tokens in 60 s
  { ...LOG, agent: "codex", agentName: "Codex", icon: "codex-color", model: "gpt-6-astra", t: at(7), out: 1500, ms: 60000 },
  // 8: Claude Desktop's Code tab writes Claude Code's file: at least 1500 / 12 s
  { ...LOG, agent: "claude-desktop", agentName: "Claude Desktop", icon: "claude-color", t: at(8), out: 1500, ms: 12000 },
];

function ledger(period) {
  return {
    period, rows: ROWS, offset: 0, total: ROWS.length, calls: ROWS.length, errors: 1,
    input: 1, output: 12355, cache_read: 0, cache_write: 0, cost: 0.02, unpriced: 5,
    // only what magpie timed: 135 tokens in 1.8 s and 1200 in 8 s
    timed: 2, ttft_ms: 2600, decode_ms: 9800, decode_out: 1335,
    by: { model: [] }, bucket: "hour", series: [],
    agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }, { id: "codex", name: "Codex", icon: "codex-color" }, { id: "claude-desktop", name: "Claude Desktop", icon: "claude-color" }],
  };
}

const STATE = (lang) => ({
  agents: [
    { id: "claude", name: "Claude Code", path: "/t/c.json", icon: "claudecode-color", wired: true, fields: [] },
    { id: "codex", name: "Codex", path: "/t/c.toml", icon: "codex-color", wired: true, fields: [] },
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
  en: {
    speed: "Speed", gatewayFloor: "≥500 tok/s", floor: "≥98 tok/s", desktop: "≥125 tok/s", codexFloor: "≥25 tok/s", timed: "75 tok/s", codex: "150 tok/s",
    floorTip: "At least: 2,558 output tokens in 26 s, from the line that asked for it to its last. The session file has no first token, so the wait for it is counted too",
    noFirst: "The session file has no first token to time a speed from", notStreamed: "Not streamed: no first token to time a speed from",
  },
  zh: {
    speed: "速度", gatewayFloor: "≥500 token/秒", floor: "≥98 token/秒", desktop: "≥125 token/秒", codexFloor: "≥25 token/秒", timed: "75 token/秒", codex: "150 token/秒",
    floorTip: "下限：从发出请求的那一行到最后一行，26 秒 输出 2,558 个 token。会话文件没有首字时间，等首字的时间也算在里面",
    noFirst: "会话文件没有首字时间，算不出速度", notStreamed: "非流式：没有首字时间，算不出速度",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the speed of a call read from a session file", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    // a wide window, and one narrow enough that the table scrolls sideways
    for (const [lang, width] of [["en", 1400], ["zh", 1400], ["en", 560], ["zh", 560]]) {
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

        const heads = await p.locator(".led thead th").evaluateAll((ths) => ths.filter((th) => th.offsetParent).map((th) => th.textContent));
        assert(heads.includes(w.speed), "a Speed column: " + heads.join(", "));
        const cell = (i) => p.locator(".led tbody tr.led-row").nth(i).locator("td.speed");
        const text = async (i) => (await cell(i).textContent()).trim();
        const tip = (i) => cell(i).getAttribute("title");

        // a long reply from a session file: the least it wrote at, and why
        assert.equal(await text(0), w.floor);
        assert.equal(await tip(0), w.floorTip);
        assert(!(await cell(0).getAttribute("class")).includes("faint"), "a floor isn't faint");
        // a short one: no speed, and not called "not streamed"
        assert.equal(await text(1), "—");
        assert.equal(await tip(1), w.noFirst);
        // what magpie timed is as it was
        assert.equal(await text(2), w.timed);
        assert.equal(await text(3), w.gatewayFloor);
        assert((await tip(3)).includes(w.notStreamed), await tip(3));
        // a session file's own timing wins over the floor
        assert.equal(await text(4), w.codex);
        // a failed call, and a reply in under a second, tell none
        assert.equal(await text(5), "—");
        assert.equal(await text(6), "—");
        // a Codex call in a turn of several tells its floor too
        assert.equal(await text(7), w.codexFloor);
        // Claude Desktop's file is Claude Code's
        assert.equal(await text(8), w.desktop);
        // the floor is read whole, at either width
        const clipped = await cell(0).evaluate((td) => td.scrollWidth > td.clientWidth + 1);
        assert(!clipped, "the floor fits its cell");

        // the row's details say it too
        // the first cell, not the time's link: at a narrow window the row is brought into sight first
        const detail = await reader.openRequest(p, p.locator(".led tbody tr.led-row").first());
        const pairs = await detail.locator("dt").evaluateAll((dts) => dts.map((dt) => [dt.textContent, dt.nextElementSibling?.textContent]));
        assert.deepEqual(pairs.find(([k]) => k === w.speed), [w.speed, w.floor], JSON.stringify(pairs));

        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
