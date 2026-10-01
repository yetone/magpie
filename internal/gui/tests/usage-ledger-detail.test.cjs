// Run with Node's test runner and Playwright on the module path; see README.md.
// The Usage page's Requests says how a request went and what is known of it:
// a failure the gateway logged shows its status and the vendor's error type in
// the row, and what the vendor said when the row is opened, with the request's
// id and endpoint; a call read from an agent's session file is marked "session
// log", says only that it went well ("Succeeded") or the error that ended it,
// and says in its details that the file records no status. A click on a row
// opens and closes its details without moving the page, the keyboard does the
// same, text selected in them is not a click, and a row stays open when the list
// refreshes. English and Chinese; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();

const ROWS = [
  // the gateway's: a failure the vendor explained
  { t: new Date(now - 60e3).toISOString(), agent: "codex", agentName: "Codex", icon: "codex-color", provider: "relay", providerName: "Relay", host: "team", req: "sol", model: "gpt-6-sol", in: 0, out: 0, ms: 610, status: 429, err: "Relay: slow down, please", err_type: "rate_limit_error", rid: "req_011abc", ep: "/v1/responses", session: "019a2b", cost: 0, priced: false },
  // the gateway's: answered, with the id and the protocol it was turned into
  { t: new Date(now - 120e3).toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color", provider: "relay", providerName: "Relay", host: "team", req: "sonnet", model: "sonnet", served: "sonnet", in: 300, out: 40, ms: 2380, ttft_ms: 700, status: 200, rid: "chatcmpl-77", ep: "/v1/messages → /v1/chat/completions", cost: 0.01, priced: true },
  // a session file's: nothing went wrong
  { t: new Date(now - 180e3).toISOString(), agent: "claude-desktop", agentName: "Claude Desktop", icon: "claude-color", provider: "claude", providerName: "Claude", req: "claude-opus-5[1m]", model: "claude-opus-5", served: "claude-opus-5", effort: "xhigh", ms: 4200, in: 1000, out: 200, cache_read: 4000, cache_write: 500, status: 0, rid: "req_log", session: "s2", session_account: "claude@example.com", source: "log", cost: 0.02, priced: true },
  // a session file's: the error that ended the call
  { t: new Date(now - 240e3).toISOString(), agent: "claude-desktop", agentName: "Claude Desktop", icon: "claude-color", provider: "claude", providerName: "Claude", model: "", in: 0, out: 0, status: 0, err: "You've hit your limit · resets 3am", err_type: "rate_limit", rid: "req_lim", session: "s3", source: "log", cost: 0, priced: false },
];

function page(q) {
  let rows = ROWS;
  if (q.get("failed") === "1") rows = rows.filter((r) => r.status >= 400 || r.err);
  return {
    period: q.get("period"), rows, offset: 0, total: rows.length, calls: rows.length, errors: 2,
    input: 1300, output: 240, cache_read: 4000, cache_write: 500, reasoning: 0, cost: 0.03, unpriced: 0,
    agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }, { id: "claude-desktop", name: "Claude Desktop", icon: "claude-color" }, { id: "codex", name: "Codex", icon: "codex-color" }],
  };
}

function server(lang, refreshed) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: { rate: 7.2, at: new Date().toISOString() } });
    if (url.pathname === "/api/usage/requests") { refreshed.n++; return json(page(url.searchParams)); }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 4, errors: 2, input: 1300, output: 240, cache_read: 4000, cache_write: 500, reasoning: 0, unpriced: 0, cost: 0.03, bucket: "day", series: [], agents: [], models: [], path: "~/.config/magpie/usage.jsonl" });
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
    statuses: ["429 · rate_limit_error", "200", "Succeeded", "rate_limit"], badge: "Local session",
    labels: { fail: ["Status", "Error type", "Upstream said", "Request ID", "Endpoint", "Session ID"], ok: ["Request ID", "Endpoint", "First token"], log: ["Request ID", "Session ID", "Data source"], logFail: ["Status", "Error type", "Error", "Request ID", "Session ID", "Data source"] },
    noStatus: "Read from the agent's session file. The account is shown only when local metadata identifies it; no service provider is inferred.",
  },
  zh: {
    statuses: ["429 · rate_limit_error", "200", "成功", "rate_limit"], badge: "本地会话",
    labels: { fail: ["状态", "错误类型", "上游返回", "请求 ID", "终结点", "会话 ID"], ok: ["请求 ID", "终结点", "首响"], log: ["请求 ID", "会话 ID", "数据来源"], logFail: ["状态", "错误类型", "错误", "请求 ID", "会话 ID", "数据来源"] },
    noStatus: "读自 Agent 的会话文件；仅在本地元数据能够明确识别时显示账号，不推断供应商。",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Requests tab's details", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      const w = L[lang];
      await t.test(lang, async () => {
        const errors = [], refreshed = { n: 0 };
        const p = await (await browser.newContext({ viewport: { width: 1180, height: 640 }, reducedMotion: "reduce" })).newPage();
        p.setDefaultTimeout(5000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.route("**/*", server(lang, refreshed));
        await p.goto("http://magpie.test/");
        await p.locator('[data-view="usage"]').first().click();
        await p.locator("#usageTab .opt").nth(1).click();
        await p.locator("#ledWrap .led tbody tr").first().waitFor();

        const rows = p.locator(".led tbody tr.led-row");
        assert.equal(await rows.count(), 4);
        // how each went, in its row: the gateway's status and the vendor's error
        // type; a session file's plain "Succeeded" or the error that ended it
        const status = await rows.evaluateAll((trs) => trs.map((tr) => tr.querySelector(".st").textContent.trim()));
        assert.deepEqual(status, w.statuses);
        assert.deepEqual(await rows.evaluateAll((trs) => trs.map((tr) => tr.classList.contains("bad"))), [true, false, false, true]);
        // a session file's time for a call is about (told from its lines' stamps); with none, a dash, not 0 ms
        const cell = (n) => rows.nth(n).locator("td").nth(12).textContent();
        assert.equal(await cell(2), lang === "zh" ? "≈4.2 秒" : "≈4.2 s", "about, from the file's times");
        assert.equal(await cell(3), "—", "a call with no time is a dash");
        assert.notEqual(await cell(0), "—");
        assert(!(await cell(0)).startsWith("≈"), "the gateway's time is its own");
        // the models and the effort: a session file names the model the API
        // answered with (Claude Code's) in Served, has none of the gateway's
        // "sent", and says the thinking effort; the gateway's row has its own
        const cells = async (n) => rows.nth(n).locator("td").evaluateAll((tds) => tds.map((td) => td.textContent.trim()));
        const logged = await cells(2), gateway = await cells(0);
        assert.deepEqual([logged[2], logged[4], logged[5], logged[6]], ["claude-opus-5[1m]", "claude-opus-5", "claude-opus-5", "xhigh"], "a session file's models and effort");
        assert.deepEqual([gateway[2], gateway[4]], ["sol", "gpt-6-sol"], "the gateway's model asked for and sent");
        // only the session file's rows are marked as such
        assert.deepEqual(await rows.evaluateAll((trs) => trs.map((tr) => tr.querySelector(".src")?.textContent || "")), ["", "", w.badge, ""]);
        assert.equal(await p.locator(".led tbody tr.led-detail").count(), 0, "closed to begin with");

        const labels = async () => p.locator(".led tbody tr.led-detail dt").allTextContents();
        const values = async () => p.locator(".led tbody tr.led-detail dd").allTextContents();
        const top = async () => p.locator("#view-usage").evaluate((v) => v.scrollTop);
        // scrolled by the wheel, as a reader does: the page puts back any scroll
        // that no wheel, key or drag asked for
        await p.mouse.move(600, 400);
        await p.mouse.wheel(0, 30);
        for (let i = 0; i < 40 && (await top()) < 30; i++) await p.waitForTimeout(25);
        await p.waitForTimeout(300);
        const before = await top();
        assert(before > 0, "the page is long enough to scroll");

        // a failure the gateway logged: what the vendor said, in the details
        await reader.click(p, rows.nth(0));
        assert.deepEqual(await labels(), w.labels.fail);
        const v = await values();
        assert.equal(v[0], "429");
        assert.equal(v[1], "rate_limit_error");
        assert.equal(v[2], "Relay: slow down, please");
        assert.equal(v[3], "req_011abc");
        assert.equal(v[4], "/v1/responses");
        assert.equal(await rows.nth(0).getAttribute("aria-expanded"), "true");
        assert.equal(await top(), before, "the click moved the page");
        // the details sit under their row, and no row is pushed out of the table
        assert.equal(await p.locator(".led tbody tr").nth(1).getAttribute("class"), "led-detail");
        const box = await p.locator(".led-box").boundingBox(), wrap = await p.locator("#ledWrap").boundingBox();
        assert(box.x >= wrap.x - 1 && box.x + box.width <= wrap.x + wrap.width + 1, "the details are within the table's box");

        // the others: the id and the endpoint it was turned into; a session file's
        await reader.click(p, rows.nth(0));
        assert.equal(await p.locator(".led tbody tr.led-detail").count(), 0, "a second click closes it");
        assert.equal(await top(), before);
        await reader.click(p, rows.nth(1));
        assert.deepEqual(await labels(), w.labels.ok);
        assert.deepEqual((await values()).slice(0, 2), ["chatcmpl-77", "/v1/messages → /v1/chat/completions"]);
        await reader.click(p, rows.nth(2));
        assert.deepEqual(await p.locator(".led tbody tr.led-detail").count(), 2);
        assert(!(await p.locator(".led-detail").nth(1).locator("dd.bad").count()), "a session file's success has nothing in red");
        assert.equal((await p.locator(".led-detail").nth(1).locator("dd").last().textContent()), w.noStatus);
        await reader.click(p, rows.nth(3));
        assert.deepEqual(await p.locator(".led-detail").nth(2).locator("dt").allTextContents(), w.labels.logFail);
        assert.equal(await p.locator(".led-detail").nth(2).locator("dd").nth(2).textContent(), "You've hit your limit · resets 3am");

        // a refresh of the list keeps the rows open
        const n = refreshed.n;
        await reader.click(p, p.locator("#ledStatus .opt").nth(1)); // Failed: asks the server
        for (let i = 0; i < 60 && refreshed.n === n; i++) await p.waitForTimeout(40);
        await reader.click(p, p.locator("#ledStatus .opt").nth(0));
        for (let i = 0; i < 60 && refreshed.n < n + 2; i++) await p.waitForTimeout(40);
        await p.waitForTimeout(150);
        assert.equal(await p.locator(".led tbody tr.led-detail").count(), 3, "the rows stayed open");

        // the keyboard: focus a row, Enter closes it and Space opens it
        await rows.nth(1).focus();
        await p.keyboard.press("Enter");
        assert.equal(await rows.nth(1).getAttribute("aria-expanded"), "false");
        await p.keyboard.press("Space");
        assert.equal(await rows.nth(1).getAttribute("aria-expanded"), "true");

        // text selected in the details is the reader copying: not a click
        await reader.inView(p,p.locator(".led-detail").first().locator("dd").first());
        await p.locator(".led-detail").first().locator("dd").first().dblclick();
        assert.equal(await p.locator(".led tbody tr.led-detail").count(), 3, "selecting text closed a row");

        // no left-border stripe on a failed row
        assert.equal(await rows.nth(0).locator("td").first().evaluate((td) => getComputedStyle(td).borderLeftWidth), "0px");
        assert.deepEqual(errors, []);
      });
    }
  });
}
