// Run with Node's test runner and Playwright on the module path; see README.md.
// The Usage page's Requests says how a request went and what is known of it:
// a failure the gateway logged shows its status and the vendor's error type in
// the row, and what the vendor said when the row is opened, with the request's
// id and endpoint, and what a call that isn't a turn of the conversation
// was for (a subagent's, #714); a call read from an agent's session file is marked "session
// log", says only that it went well ("Succeeded") or the error that ended it,
// and says in its details that the file records no status. A click on a row
// opens the request on a page of its own (geesehowardt7 on X) and Requests ‹
// goes back to the list where it was left, the keyboard does the same, and
// text selected on the page is not a click. English and Chinese; no backend,
// the API is faked.
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
  { t: new Date(now - 120e3).toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color", provider: "relay", providerName: "Relay", host: "team", req: "sonnet", model: "sonnet", served: "sonnet", in: 300, out: 40, ms: 2380, ttft_ms: 700, sent_ms: 40, status: 200, kind: "collab_spawn", rid: "chatcmpl-77", ep: "/v1/messages → /v1/chat/completions", cost: 0.01, priced: true },
  // a session file's: nothing went wrong
  { t: new Date(now - 180e3).toISOString(), agent: "claude-desktop", agentName: "Claude Desktop", icon: "claude-color", provider: "claude", providerName: "Claude", req: "claude-opus-5[1m]", model: "claude-opus-5", served: "claude-opus-5", effort: "xhigh", ms: 4200, in: 1000, out: 200, cache_read: 4000, cache_write: 500, status: 0, rid: "req_log", session: "s2", session_account: "claude@example.com", source: "log", cost: 0.02, priced: true },
  // a session file's: the error that ended the call
  { t: new Date(now - 240e3).toISOString(), agent: "claude-desktop", agentName: "Claude Desktop", icon: "claude-color", provider: "claude", providerName: "Claude", model: "", in: 0, out: 0, status: 0, err: "You've hit your limit · resets 3am", err_type: "rate_limit", rid: "req_lim", session: "s3", source: "log", cost: 0, priced: false },
  // the gateway's, answered on its second try (#1532): the vendor held the
  // first ~103 s before its 503, which isn't magpie's time
  { t: new Date(now - 300e3).toISOString(), agent: "codex", agentName: "Codex", icon: "codex-color", provider: "opencode-go", providerName: "OpenCode Go", host: "opencode.ai", req: "opencode-go/deepseek-flash", model: "deepseek-flash", served: "deepseek-flash", effort: "max", in: 7064, out: 5373, cache_read: 314624, reasoning: 4946, ms: 160000, ttft_ms: 141000, sent_ms: 104000, own_ms: 180, masking_ms: 150, retried_ms: 103000, tries: 2, status: 200, ep: "/backend-api/codex/responses", session: "01a12671", cost: 0.005, priced: true },
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
    statuses: ["429 · rate_limit_error", "200", "Succeeded", "rate_limit", "200 · 2 tries"], badge: "Local session",
    split: [["Masking", "150 ms"], ["In magpie", "30 ms"], ["Tries that failed first", "103 s"], ["Connecting and sending", "820 ms"], ["Vendor's first token", "37 s"]],
    summary: ["Time", "Agent", "Source IP", "Requested", "Provider", "Account", "Sent", "Served", "Effort", "Cache hit rate", "Cost"], notRecorded: "Not recorded", back: "Requests",
    speed: "24 tok/s", tokens: "300 in · 40 out · 0 cache read · 0 cache creation", labels: { fail: ["Status", "Error type", "Upstream said", "Duration", "Request ID", "Endpoint", "Protocol", "Session ID"], ok: ["Status", "Duration", "Tokens", "Speed", "Request ID", "Endpoint", "Protocol", "Called for", "First token", "In magpie", "Vendor's first token"], log: ["Request ID", "Session ID", "Data source"], logFail: ["Status", "Error type", "Error", "Request ID", "Session ID", "Data source"] },
    subagent: "Subagent",
    noStatus: "Read from the agent's session file. The account is shown only when local metadata identifies it; no service provider is inferred.",
  },
  zh: {
    statuses: ["429 · rate_limit_error", "200", "成功", "rate_limit", "200 · 2 次尝试"], badge: "本地会话",
    split: [["脱敏", "150 毫秒"], ["magpie 内耗时", "30 毫秒"], ["之前失败的尝试", "103 秒"], ["连接并发送请求", "820 毫秒"], ["厂商首字等待", "37 秒"]],
    summary: ["时间", "Agent", "来源 IP", "请求模型", "供应商", "账号", "发送模型", "实际模型", "推理强度", "缓存命中率", "费用"], notRecorded: "未记录", back: "请求",
    speed: "24 token/秒", tokens: "输入 300 · 输出 40 · 缓存读取 0 · 缓存创建 0", labels: { fail: ["状态", "错误类型", "上游返回", "耗时", "请求 ID", "接口地址", "协议", "会话 ID"], ok: ["状态", "耗时", "Token", "速度", "请求 ID", "接口地址", "协议", "用途", "首字耗时", "magpie 内耗时", "厂商首字等待"], log: ["请求 ID", "会话 ID", "数据来源"], logFail: ["状态", "错误类型", "错误", "请求 ID", "会话 ID", "数据来源"] },
    subagent: "子代理",
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
        p.setDefaultNavigationTimeout(60000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.route("**/*", server(lang, refreshed));
        await p.goto("http://magpie.test/");
        await p.locator('[data-view="usage"]').first().click();
        await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
        await p.locator("#ledWrap .led tbody tr").first().waitFor();

        const rows = p.locator(".led tbody tr.led-row");
        assert.equal(await rows.count(), 5);
        // how each went, in its row: the gateway's status and the vendor's error
        // type; a session file's plain "Succeeded" or the error that ended it
        const status = await rows.evaluateAll((trs) => trs.map((tr) => tr.querySelector(".st").textContent.trim()));
        assert.deepEqual(status, w.statuses);
        assert.deepEqual(await rows.evaluateAll((trs) => trs.map((tr) => tr.classList.contains("bad"))), [true, false, false, true, false]);
        // a session file's time for a call is about (told from its lines' stamps); with none, a dash, not 0 ms
        const cell = (n) => rows.nth(n).locator("td.duration").textContent();
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
        // only the session file's rows are marked as such; the gateway's say
        // the protocol they were sent upstream in (a session file keeps no path)
        const badge = (sel) => rows.evaluateAll((trs, sel) => trs.map((tr) => tr.querySelector(sel)?.textContent || ""), sel);
        assert.deepEqual(await badge(".src.local"), ["", "", w.badge, "", ""]);
        assert.deepEqual(await badge(".src.proto"), ["Responses", "Anthropic → Chat", "", "", "Responses"]);
        assert.equal(await p.locator(".led tbody tr.led-detail").count(), 0, "no row opens inside the table");
        assert(await p.locator("#ledReqPane").isHidden(), "no request's page to begin with");

        // the page's rows: what each request says first (when, who, from
        // where, what was asked and what answered) is told apart from the rest
        const pairs = () => p.locator("#ledReqPane .led-dl").evaluate((d) => [...d.querySelectorAll("dt")].map((dt) => [dt.textContent, dt.nextElementSibling.textContent]));
        const labels = async () => (await pairs()).filter(([k]) => !w.summary.includes(k)).map(([k]) => k);
        const values = async () => (await pairs()).filter(([k]) => !w.summary.includes(k)).map(([, v]) => v);
        const head = async () => (await pairs()).filter(([k]) => w.summary.includes(k));
        const top = async () => p.locator("#view-usage").evaluate((v) => v.scrollTop);
        // scrolled by the wheel, as a reader does: the page puts back any scroll
        // that no wheel, key or drag asked for
        await p.mouse.move(600, 400);
        await p.mouse.wheel(0, 30);
        for (let i = 0; i < 40 && (await top()) < 30; i++) await p.waitForTimeout(25);
        await p.waitForTimeout(300);
        await reader.inView(p, rows.nth(0));
        let before = await top();
        assert(before > 0, "the page is long enough to scroll");
        const open = async (n) => {
          // where the list is when the row is clicked is where back returns
          await reader.inView(p, rows.nth(n));
          before = await top();
          await rows.nth(n).click();
          await p.locator("#ledReqPane .led-dl").waitFor();
          assert(await p.locator("#ledgerPane").isHidden(), "the list gives way to the request");
        };
        const back = async () => {
          await reader.click(p, p.locator("#ledReqPane .led-req-back"));
          await rows.first().waitFor();
          assert(await p.locator("#ledReqPane").isHidden());
          assert.equal(await top(), before, "back to the list where it was left");
        };

        // a failure the gateway logged: what the vendor said, on its page
        await open(0);
        assert.equal((await p.locator("#ledReqPane .led-req-back").textContent()).trim(), w.back);
        assert.equal(await p.locator("#ledReqPane .led-req-title").textContent(), "gpt-6-sol");
        const h = Object.fromEntries(await head());
        assert.deepEqual(Object.keys(h), w.summary.filter((k) => ![w.summary[5], w.summary[7], w.summary[8], w.summary[9], w.summary[10]].includes(k)), "a failure has no answer, effort or price");
        assert.equal(h[w.summary[2]], w.notRecorded, "a row kept before the address was has none");
        assert.equal(h[w.summary[3]], "sol");
        assert.equal(h[w.summary[4]], "Relay · team");
        assert.equal(h[w.summary[6]], "gpt-6-sol");
        assert.deepEqual(await labels(), w.labels.fail);
        const v = await values();
        assert.equal(v[0], "429");
        assert.equal(v[1], "rate_limit_error");
        assert.equal(v[2], "Relay: slow down, please");
        assert.equal(v[4], "req_011abc");
        assert.equal(v[5], "/v1/responses");
        assert.equal(v[6], "Responses", "the protocol it was sent upstream in");
        await back();

        // the others: the id and the endpoint it was turned into; a session file's
        await open(1);
        assert.deepEqual(await labels(), w.labels.ok);
        // how it went and how long, which the row says only in its last columns (#799)
        assert.deepEqual((await values()).slice(0, 2).map((v) => v.replace(/[^\d.]/g, "")), ["200", "2.4"]);
        // its tokens, the cache's beside in and out, though none was read (#1839)
        assert.equal((await values())[2], w.tokens);
        // how fast it wrote (#860)
        assert.equal((await values())[3], w.speed);
        assert.deepEqual((await values()).slice(4, 8), ["chatcmpl-77", "/v1/messages → /v1/chat/completions", "Anthropic → Chat", w.subagent]);
        // the first token split: magpie's own 40 ms, the vendor's 660 ms
        assert.deepEqual((await values()).slice(9, 11).map((v) => v.match(/^\d+/)?.[0]), ["40", "660"]);
        await back();
        await open(2);
        assert(!(await p.locator("#ledReqPane dd.bad").count()), "a session file's success has nothing in red");
        assert.equal(await p.locator("#ledReqPane .led-dl dd").last().textContent(), w.noStatus);
        assert(!(await head()).some(([k]) => k === w.summary[2]), "a session file's call has no address to show");
        await back();
        await open(3);
        assert.deepEqual(await labels(), w.labels.logFail);
        assert.equal((await values())[2], "You've hit your limit · resets 3am");
        await back();

        // #1532: of the 104 s before the vendor had the request, magpie's own
        // 180 ms, the try that failed first 103 s, connecting and sending the
        // rest; the 40 ms of a record from before the split was kept stays
        // "In magpie" (above). Of magpie's own, the 150 ms masking it took
        // is told apart (fremontdango on Discord)
        await open(4);
        const all = await pairs();
        const at = all.findIndex(([k]) => k === w.split[0][0]);
        assert.deepEqual(all.slice(at, at + w.split.length), w.split);

        // a refresh of the list leaves the request's page as it is
        const n = refreshed.n;
        await reader.click(p, p.locator("#usageReload"));
        for (let i = 0; i < 60 && refreshed.n === n; i++) await p.waitForTimeout(40);
        await p.waitForTimeout(150);
        assert(await p.locator("#ledReqPane").isVisible(), "a refresh closed the request");

        // text selected on the page is the reader copying, nothing else
        await p.locator("#ledReqPane .led-dl dd").first().dblclick();
        assert(await p.locator("#ledReqPane").isVisible());

        // the keyboard: Escape goes back, the row it came from has the focus,
        // and Enter or Space opens it again
        await p.keyboard.press("Escape");
        await rows.first().waitFor();
        assert(await p.locator("#ledReqPane").isHidden(), "Escape went back");
        assert.equal(await p.evaluate(() => document.activeElement?.dataset?.key), await rows.nth(4).getAttribute("data-key"));
        await rows.nth(1).focus();
        await p.keyboard.press("Enter");
        await p.locator("#ledReqPane .led-dl").waitFor();
        assert.equal(await p.locator("#ledReqPane .led-req-title").textContent(), "sonnet");
        await p.keyboard.press("Escape");
        await rows.nth(1).focus();
        await p.keyboard.press("Space");
        await p.locator("#ledReqPane .led-dl").waitFor();
        await p.keyboard.press("Escape");
        await rows.first().waitFor();

        // no left-border stripe on a failed row
        assert.equal(await rows.nth(0).locator("td").first().evaluate((td) => getComputedStyle(td).borderLeftWidth), "0px");
        assert.deepEqual(errors, []);
      });
    }
  });
}
