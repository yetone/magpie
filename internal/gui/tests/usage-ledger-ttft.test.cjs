// Run with Node's test runner and Playwright on the module path; see README.md.
// Each request's first token in a column of its own on the Usage page's
// Requests (tianxia3111, #2522): the duration holds the wait for it and the
// writing after it, so a list of durations can't tell a vendor slow to
// start from one slow to write. The column says the row details' "First
// token", "≈" for one told from a session file's times, and a dash for one
// with none (failed, or not streamed). A narrow window leaves it out with
// Sent, Effort and the cache, and then it reads small under each duration
// instead (unscientific9 on Discord), a row with none adding nothing; the
// hint names only what is in the details alone. Every language, both
// engines, three widths; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const base = { agent: "claude", agentName: "Claude Code", icon: "claudecode-color", provider: "relay", providerName: "Relay", host: "team", req: "sol", model: "gpt-6-sol", in: 1200, out: 300, cost: 0.01, priced: true };
// the reporter's own sample (ttft_ms 5504 of 8502), a Codex turn read from
// its session file, a failure before any token, and a reply not streamed
const ROWS = [
  { ...base, t: new Date(now - 60e3).toISOString(), ms: 8502, ttft_ms: 5504, sent_ms: 119, flow_ms: 2658, status: 200, rid: "req_a", ep: "/v1/messages" },
  { ...base, t: new Date(now - 120e3).toISOString(), source: "log", agent: "codex", agentName: "Codex", icon: "codex-color", session: "s1", ms: 4200, ttft_ms: 2100 },
  { ...base, t: new Date(now - 180e3).toISOString(), in: 0, out: 0, ms: 610, status: 429, err: "rate limited", priced: false },
  { ...base, t: new Date(now - 240e3).toISOString(), ms: 3000, status: 200 },
];

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: { rate: 7.2, at: new Date().toISOString() } });
    if (url.pathname === "/api/usage/requests") {
      return json({ period: url.searchParams.get("period"), rows: ROWS, offset: 0, total: ROWS.length, calls: ROWS.length, errors: 1, input: 3600, output: 900, cache_read: 0, cache_write: 0, cost: 0.03, unpriced: 0, agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }, { id: "codex", name: "Codex", icon: "codex-color" }] });
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

const L = {
  en: { head: "TTFT", detail: "First token", duration: "Duration", hint: "Sent, Effort and the cache are in each row's details", under: "TTFT " },
  zh: { head: "首字耗时", detail: "首字耗时", duration: "耗时", hint: "窗口较窄：发送模型、推理强度和缓存在每行的详情里", under: "首字 " },
  "zh-TW": { head: "首字耗時", detail: "首字耗時", duration: "耗時", hint: "視窗較窄：傳送模型、推理強度和快取在每行的詳情裡", under: "首字 " },
  ja: { head: "TTFT", detail: "最初のトークン", duration: "所要時間", hint: "ウィンドウが狭いため、送信したモデル・推論レベル・キャッシュは各行の詳細にあります", under: "TTFT " },
  de: { head: "TTFT", detail: "Erstes Token", duration: "Dauer", hint: "Schmales Fenster: Gesendet, Effort und Cache stehen in den Details jeder Zeile", under: "TTFT " },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": each request's first token, a column of its own", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) {
      const w = L[lang];
      await t.test(lang, async () => {
        const errors = [];
        const ctx = await browser.newContext({ viewport: { width: 1600, height: 800 }, reducedMotion: "reduce" });
        t.after(() => ctx.close());
        const p = await ctx.newPage();
        p.setDefaultTimeout(5000);
        p.setDefaultNavigationTimeout(60000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.route("**/*", server(lang));
        await p.goto("http://magpie.test/");
        await p.locator('[data-view="usage"]').first().click();
        await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
        await p.locator("#ledWrap .led tbody tr").first().waitFor();
        await p.waitForTimeout(200);

        // the column, just before the duration it is part of
        const heads = await p.locator(".led thead th").allTextContents();
        assert.equal(heads.indexOf(w.head) + 1, heads.indexOf(w.duration), "first token before duration: " + heads.join(", "));
        assert((await p.locator(".led thead th.lc-opt2").getAttribute("title")).length > 0, "its header says what it is in full");
        const rows = p.locator(".led tbody tr.led-row");
        const cell = (i) => rows.nth(i).locator("td.ttft");
        assert(await cell(0).isVisible(), "a wide window shows it");
        // it says what the details say, "≈" for a session file's
        const detail = async (i) => {
          await reader.click(p, rows.nth(i).locator("td").nth(2));
          const d = p.locator(".led tbody tr.led-detail");
          const dts = await d.locator("dt").allTextContents(), dds = await d.locator("dd").allTextContents();
          await reader.click(p, rows.nth(i).locator("td").nth(2));
          return dds[dts.indexOf(w.detail)];
        };
        const first = await detail(0);
        assert(first && /5/.test(first), "the details' first token: " + first);
        assert.equal((await cell(0).textContent()).trim(), first);
        assert((await cell(0).getAttribute("title")).length > 0, "its exact ms in the tooltip");
        assert.equal((await cell(1).textContent()).trim(), "≈" + (await detail(1)), "about, from the session file's times");
        // a failure before any token, and a reply not streamed: a dash
        assert.equal((await cell(2).textContent()).trim(), "—");
        assert.equal((await cell(3).textContent()).trim(), "—");
        assert(await cell(3).evaluate((c) => c.classList.contains("faint")), "a dash reads faint, as the others' do");
        // numbers line up as the duration's do
        assert.deepEqual(await cell(0).evaluate((c) => getComputedStyle(c).textAlign), await rows.nth(0).locator("td.duration").evaluate((c) => getComputedStyle(c).textAlign));
        // what reads under each duration: nothing while the column is there
        const under = (i) => rows.nth(i).locator("td.duration").evaluate((c) => {
          const a = getComputedStyle(c, "::after");
          return a.content === "none" || a.content === "normal" || a.display === "none" ? "" : JSON.parse(a.content);
        });
        const durText = await rows.nth(0).locator("td.duration").textContent();
        for (let i = 0; i < 4; i++) assert.equal(await under(i), "", "a wide window adds nothing under the duration");

        // a narrow window: the column goes, the details keep it, the hint says so
        await p.setViewportSize({ width: 760, height: 800 });
        await p.waitForTimeout(300);
        assert.equal(await cell(0).isVisible(), false, "a narrow window leaves it out");
        assert.equal((await p.locator("#ledTight").textContent()).trim(), w.hint);
        // and says it under the duration, small and faint; a row with none, nothing
        assert.equal(await under(0), w.under + first);
        assert.equal(await under(1), w.under + "≈" + (await detail(1)), "about, from the session file's times");
        assert.equal(await under(2), "", "a failure before any token adds nothing");
        assert.equal(await under(3), "", "nor a reply not streamed");
        assert.equal(await rows.nth(0).locator("td.duration").textContent(), durText, "the cell's own text is the duration alone");
        const look = await rows.nth(0).locator("td.duration").evaluate((c) => {
          const a = getComputedStyle(c, "::after"), d = getComputedStyle(c);
          return { small: parseFloat(a.fontSize) < parseFloat(d.fontSize), block: a.display, color: a.color !== d.color };
        });
        assert.deepEqual(look, { small: true, block: "block", color: true }, "a small second line, muted");
        assert(await rows.nth(0).evaluate((r) => r.getBoundingClientRect().height) < 60, "the row stays a row");
        await reader.click(p, rows.nth(0).locator("td").nth(2));
        const shown = await p.locator(".led tbody tr.led-detail dt").evaluateAll((dts) => dts.filter((x) => x.offsetParent).map((x) => x.textContent));
        assert(shown.includes(w.detail), "the details still say it: " + shown.join(", "));
        assert.equal(await p.locator("#view-usage").evaluate((v) => v.scrollWidth <= v.clientWidth + 1), true, "the page doesn't scroll sideways");
        await reader.click(p, rows.nth(0).locator("td").nth(2));

        // a phone-narrow window: still under the duration, the page not sideways
        await p.setViewportSize({ width: 420, height: 800 });
        await p.waitForTimeout(300);
        if (await rows.nth(0).locator("td.duration").isVisible()) {
          assert.equal(await under(0), w.under + first, "still under the duration at 420");
          assert.equal(await under(3), "");
        }
        assert.equal(await p.locator("#view-usage").evaluate((v) => v.scrollWidth <= v.clientWidth + 1), true, "the page doesn't scroll sideways at 420");
        assert.deepEqual(errors, []);
      });
    }
  });
}
