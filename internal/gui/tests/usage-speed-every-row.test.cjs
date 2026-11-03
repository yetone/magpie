// Run with Node's test runner and Playwright on the module path; see README.md.
// Every row of the Usage page's Requests tab says a speed, or says why it
// has none (夜松云 on Discord: "the speed shows for some rows and not for
// others"). A gateway call with no speed of its own tells the least it wrote
// at when that is known: one not streamed, its output over its whole time
// (1,000 tokens or more over a second or more), and one that reasoned with
// no first text recorded, its answer over the time after its first token.
// A dash left has a tooltip that says why: failed, no output, not streamed,
// all reasoning, too little after the first token, or let go in a burst.
// The rows are shaped as real gateway ledger rows (usage.jsonl). English and
// Chinese; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const at = (i) => new Date(now - (i + 1) * 60e3).toISOString();

const GW = {
  agent: "codex", agentName: "Codex", icon: "codex-color", provider: "codex", providerName: "Codex", host: "me@example.com",
  req: "codex/gpt-6-sol", model: "gpt-6-sol", served: "gpt-6-sol", in: 2653, cache_read: 31232, cache_write: 0,
  status: 200, ep: "/backend-api/codex/responses", stop: "completed", cost: 0.01, priced: true,
};
const ROWS = [
  // 0: a chat call not streamed to the agent: 3,832 tokens in 31 s
  { ...GW, route_id: 1, t: at(0), provider: "antigravity", model: "gemini-3.8-flash-tiered", served: "gemini-3.8-flash-tiered", req: "antigravity/gemini-3.8-flash-tiered", out: 3832, reasoning: 3830, ms: 31006, ep: "/v1/chat/completions → /v1internal:streamGenerateContent?alt=sse" },
  // 1: a short one not streamed: too little over its whole time
  { ...GW, route_id: 2, t: at(1), provider: "deepseek", model: "deepseek-flash", served: "deepseek-flash", req: "deepseek/deepseek-flash", out: 105, reasoning: 103, ms: 6683, ep: "/v1/chat/completions" },
  // 2: hidden reasoning then a 6-token tool call 87 ms after the first token
  { ...GW, route_id: 3, t: at(2), out: 47, reasoning: 41, ms: 6788, ttft_ms: 6701, reasoning_hidden: true, flow_ms: 1 },
  // 3: its answer let go in one burst at its end
  { ...GW, route_id: 4, t: at(3), out: 61, reasoning: 16, ms: 5606, ttft_ms: 5436, first_text_ms: 5467, reasoning_hidden: true, flow_ms: 1 },
  // 4: the agent went away
  { ...GW, route_id: 5, t: at(4), out: 0, ms: 13748, ttft_ms: 5851, first_text_ms: 5858, status: 499, err: "the agent canceled the request", cost: 0, priced: false },
  // 5: answered with no tokens
  { ...GW, route_id: 6, t: at(5), provider: "qoder", model: "efficient", served: "efficient", req: "qoder/efficient", in: 0, out: 0, ms: 5388, ep: "/v1/chat/completions" },
  // 6: hidden reasoning, then a 1,000-token tool call over the 10 s after the first token
  { ...GW, route_id: 7, t: at(6), out: 1300, reasoning: 300, ms: 16000, ttft_ms: 6000, reasoning_hidden: true, flow_ms: 9000 },
  // 7: reasoning and nothing after it
  { ...GW, route_id: 8, t: at(7), out: 41, reasoning: 41, ms: 6000, ttft_ms: 5000, reasoning_hidden: true, flow_ms: 1 },
  // 8: measured: 500 tokens in the 5 s after its first
  { ...GW, route_id: 9, t: at(8), out: 500, ms: 7000, ttft_ms: 2000, first_text_ms: 2000, flow_ms: 5000 },
  // 9: pluviophile714 on Discord: gpt-6.1-sol, 392 tokens 255 ms after an
  // 8.2 s first token, spread over most of that (flow 230 ms), read 1,539
  // tok/s: under a second after a wait over four times longer tells none
  { ...GW, route_id: 10, t: at(9), req: "codex/gpt-6.1-sol", model: "gpt-6.1-sol", served: "gpt-6.1-sol", in: 21617, cache_read: 0, out: 392, ms: 8455, ttft_ms: 8200, first_text_ms: 8200, flow_ms: 230 },
];
// row 9's tooltip in every language its words are in
const HELD = {
  en: "It came in under a second after a wait of 8.2 s: it may have been written during the wait and let go at once, so how fast it was written isn't seen",
  zh: "等了 8.2 秒 后不到一秒就全部到达：可能是在等待中写完再一次性发出，看不出写得多快",
  "zh-TW": "等了 8.2 秒 後不到一秒就全部到達：可能是在等待中寫完再一次性送出，看不出寫得多快",
  ja: "8.2 秒 待った後、1 秒足らずで全部届きました。待つ間に書かれて一度に送られたかもしれないため、書く速さはわかりません",
  de: "Sie kam nach einer Wartezeit von 8.2 s in weniger als einer Sekunde: Sie wurde womöglich während des Wartens geschrieben und auf einmal losgelassen, also ist nicht zu sehen, wie schnell sie geschrieben wurde",
};

function ledger(period) {
  return {
    period, rows: ROWS, offset: 0, total: ROWS.length, calls: ROWS.length, errors: 1,
    input: 1, output: 1, cache_read: 0, cache_write: 0, cost: 0.02, unpriced: 0,
    timed: 1, ttft_ms: 2000, decode_ms: 5000, decode_out: 500, decode_n: 1,
    by: { model: [] }, bucket: "hour", series: [],
    agents: [{ id: "codex", name: "Codex", icon: "codex-color" }],
  };
}

const STATE = (lang) => ({
  agents: [{ id: "codex", name: "Codex", path: "/t/c.toml", icon: "codex-color", wired: true, fields: [] }],
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
    whole: "≥124 tok/s", answer: "≥100 tok/s", measured: "100 tok/s",
    wholeTip: "At least: 3,832 output tokens in 31 s, from the request to its last token, the wait for the first one counted too",
    notStreamed: "Not streamed: no first token to time a speed from",
    answerTip: "At least: 1,000 answer tokens in 10 s after the first token, the 300 reasoning tokens left out. No first text was recorded",
    little: "Too little came after the first token to time a speed by: 6 tokens in 87 ms",
    burst: "It came in one burst at its end, not as it was written, so how fast it was written isn't seen",
    failed: "A failed request tells no speed",
    noOut: "No output tokens to time a speed from",
    allReasoning: "All its output was reasoning: no answer after it to time a speed from",
  },
  zh: {
    whole: "≥124 token/秒", answer: "≥100 token/秒", measured: "100 token/秒",
    wholeTip: "下限：从收到请求到最后一个 token，31 秒 输出 3,832 个 token，等首字的时间也算在里面",
    notStreamed: "非流式：没有首字时间，算不出速度",
    answerTip: "下限：首字后 10 秒 输出 1,000 个正文 token，300 个推理 token 不计。没有记录首个正文的时间",
    little: "首字后内容太少，测不出速度：87 毫秒 输出 6 个 token",
    burst: "内容在最后一次性到达，不是边写边发，看不出写得多快",
    failed: "失败的请求没有速度",
    noOut: "没有输出 token，算不出速度",
    allReasoning: "输出全是推理，之后没有正文，算不出速度",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": every request says its speed or why it has none", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      const w = L[lang];
      await t.test(lang, async () => {
        const errors = [];
        const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 }, reducedMotion: "reduce" });
        const p = await ctx.newPage();
        p.setDefaultTimeout(5000);
        p.setDefaultNavigationTimeout(60000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.route("**/*", server(lang));
        await p.goto("http://magpie.test/");
        await p.locator('[data-view="usage"]').first().click();
        await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
        await p.locator("#ledWrap .led tbody tr").first().waitFor();

        const cells = p.locator(".led tbody tr.led-row td.speed");
        assert.equal(await cells.count(), ROWS.length);
        const text = async (i) => (await cells.nth(i).textContent()).trim();
        const tip = async (i) => (await cells.nth(i).getAttribute("title")) || "";

        // no row is left blank: a figure, or a dash that says why
        for (let i = 0; i < ROWS.length; i++) {
          const s = await text(i);
          assert(s, "row " + i + " has a speed or a dash");
          if (s === "—") assert((await tip(i)).length > 0, "row " + i + "'s dash says why");
        }
        // the report: no 1,539 tok/s, and why
        assert.equal(await text(9), "—");
        assert.equal(await tip(9), HELD[lang]);
        if (!w) { assert.deepEqual(errors, []); await ctx.close(); return; }

        // not streamed, long enough: the least it wrote at, and why it's a floor
        assert.equal(await text(0), w.whole);
        assert((await tip(0)).startsWith(w.wholeTip), await tip(0));
        assert((await tip(0)).includes(w.notStreamed), await tip(0));
        // not streamed, too short for a floor
        assert.equal(await text(1), "—");
        assert.equal(await tip(1), w.notStreamed);
        // 6 tokens 87 ms after the first
        assert.equal(await text(2), "—");
        assert.equal(await tip(2), w.little);
        // a burst
        assert.equal(await text(3), "—");
        assert.equal(await tip(3), w.burst);
        // failed, and no output
        assert.equal(await tip(4), w.failed);
        assert.equal(await tip(5), w.noOut);
        // the answer after hidden reasoning, no first text recorded
        assert.equal(await text(6), w.answer);
        assert((await tip(6)).startsWith(w.answerTip), await tip(6));
        // reasoning only
        assert.equal(await text(7), "—");
        assert.equal(await tip(7), w.allReasoning);
        // what was measured is as it was
        assert.equal(await text(8), w.measured);

        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
