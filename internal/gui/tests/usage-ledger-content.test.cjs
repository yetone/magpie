// Run with Node's test runner and Playwright on the module path; see README.md.
// A request's details say what was said in it: what its agent was given and
// what came back, read from the agent's own session file when the row is opened
// (the server finds the call by its session and time; magpie keeps no copy).
// "Loading…" first; then the input and the output as parts — who said each, its
// words in a box, a tool's call with its name — the reasoning and what the agent
// put in itself folded, a long one showing a part of itself and unrolling, and
// what is left off a part or the whole said. A request the files can't tell says
// why: no session named, an agent whose files aren't read, no such call. A gateway
// request is looked for in the span it took, a call of a session file at its own
// time. A row opened again, or drawn again as the list is read anew, doesn't ask
// again. English and Chinese; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const iso = (ms) => new Date(now - ms).toISOString();
const LONG = Array.from({ length: 30 }, (_, i) => `line ${i + 1} of a long answer`).join("\n");

const ROWS = [
  // a request the gateway logged, of a session that has its file
  { t: iso(60e3), agent: "claude", agentName: "Claude Code", icon: "claudecode-color", provider: "relay", providerName: "Relay", req: "opus", model: "claude-opus-5-5", in: 10, out: 5, ms: 3100, status: 200, session: "sess-gw", rid: "req_gw", cost: 0.01, priced: true },
  // a call read from a session file
  { t: iso(120e3), agent: "codex", agentName: "Codex", icon: "codex-color", provider: "codex", providerName: "Codex", req: "gpt-6-luna", model: "gpt-6-luna", in: 10, out: 5, status: 0, session: "sess-log", source: "log", cost: 0.01, priced: true },
  // one that named no session
  { t: iso(180e3), agent: "claude", agentName: "Claude Code", icon: "claudecode-color", provider: "relay", providerName: "Relay", req: "opus", model: "claude-opus-5-5", in: 1, out: 1, ms: 900, status: 200, cost: 0, priced: false },
  // one of an agent whose files aren't read
  { t: iso(240e3), agent: "Bob", agentName: "Bob", icon: "generic", provider: "relay", providerName: "Relay", req: "x", model: "x", in: 1, out: 1, ms: 900, status: 200, session: "sess-bob", cost: 0, priced: false },
  // one the files don't have
  { t: iso(300e3), agent: "claude", agentName: "Claude Code", icon: "claudecode-color", provider: "relay", providerName: "Relay", req: "opus", model: "claude-opus-5-5", in: 1, out: 1, ms: 900, status: 200, session: "sess-gone", cost: 0, priced: false },
];

const CONTENT = {
  "sess-gw": {
    found: true, model: "claude-opus-5-5",
    input: [{ role: "user", kind: "text", text: "Fix the login bug" }, { role: "user", kind: "context", text: "<system-reminder>be careful</system-reminder>" }],
    output: [
      { role: "assistant", kind: "thinking", text: "The user wants the login fixed." },
      { role: "assistant", kind: "text", text: LONG },
      { role: "assistant", kind: "tool_use", name: "Read", text: '{\n  "file_path": "/a/login.go"\n}' },
      { role: "assistant", kind: "text", text: "the end of a part", cut: 250 },
    ],
    cut: true,
  },
  "sess-log": { found: true, model: "gpt-6-luna", input: [{ role: "tool", kind: "tool_result", text: "a.go\nb.go" }], output: [{ role: "assistant", kind: "text", text: "There are two files." }] },
};

function server(lang, asked, refreshed) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: { rate: 7.2, at: new Date().toISOString() } });
    if (url.pathname === "/api/usage/requests") {
      refreshed.n++;
      return json({ period: "30d", rows: ROWS, offset: 0, total: ROWS.length, calls: ROWS.length, errors: 0, input: 23, output: 13, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0.02, unpriced: 0, bucket: "day", series: [], by: { provider: [], agent: [], model: [] }, agents: [], providers: [] });
    }
    if (url.pathname === "/api/usage/requests/content") {
      const q = url.searchParams;
      asked.push(Object.fromEntries(q));
      await new Promise((r) => setTimeout(r, 250)); // long enough to be seen loading
      const agent = q.get("agent"), s = q.get("session");
      if (!s) return json({ found: false, why: "session", input: [], output: [] });
      if (!["claude", "claude-desktop", "codex"].includes(agent)) return json({ found: false, why: "agent", input: [], output: [] });
      return json(CONTENT[s] || { found: false, why: "missing", input: [], output: [] });
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 1, errors: 0, input: 1, output: 1, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 0, cost: 1, bucket: "day", series: [], agents: [], models: [], path: "~/.config/magpie/usage.jsonl" });
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
  en: { loading: "Loading…", input: "Input", output: "Output", roles: ["You", "Context"], out: ["Thinking", "Assistant", "Tool call", "Assistant"], tool: "Tool result", more: "Show full content", less: "Collapse content", cut: "… 250 more characters not shown", whole: "There was more than is shown here",
    why: ["No session was named with this request, so its session file can't be found", "magpie reads the session files of Claude Code, Claude Desktop and Codex only", "This request isn't in the agent's session files: they may be deleted, moved, or not written yet"],
    src: "Read from the agent's session file; magpie keeps no copy" },
  zh: { loading: "加载中…", input: "输入", output: "输出", roles: ["你", "上下文"], out: ["思考", "助手", "工具调用", "助手"], tool: "工具结果", more: "展开全部", less: "收起", cut: "……还有 250 个字符未显示", whole: "内容太多，这里只显示了一部分",
    why: ["这个请求没有带会话 ID，找不到它的会话文件", "magpie 只读 Claude Code、Claude Desktop 和 Codex 的会话文件", "在 Agent 的会话文件里找不到这个请求：文件可能已被删除、移走，或者还没写入"],
    src: "读自 Agent 的会话文件，magpie 不保存副本" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": what was said in a request", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const shots = process.env.ARTIFACT_DIR;
    if (shots) await fs.mkdir(shots, { recursive: true });
    for (const lang of ["en", "zh"]) {
      const w = L[lang];
      await t.test(lang, async () => {
        const errors = [], asked = [], refreshed = { n: 0 };
        const p = await (await browser.newContext({ viewport: { width: 1180, height: 800 }, reducedMotion: "reduce" })).newPage();
        p.setDefaultTimeout(5000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.route("**/*", server(lang, asked, refreshed));
        await p.goto("http://magpie.test/");
        await p.locator('[data-view="usage"]').first().click();
        await p.locator("#usageTab .opt").nth(1).click();
        await p.locator("#ledWrap .led tbody tr").first().waitFor();
        const rows = p.locator(".led tbody tr.led-row");

        // opened: loading first, then the input and the output
        await reader.click(p, rows.nth(0));
        assert.equal((await p.locator(".led-cx").first().textContent()).trim(), w.loading);
        await p.locator(".led-cx .cx-sec").first().waitFor();
        assert.deepEqual(await p.locator(".led-cx .cx-sec h4").allTextContents(), [w.input, w.output]);
        const roles = await p.locator(".led-cx .cx-role").allTextContents();
        assert.deepEqual(roles, [...w.roles, ...w.out]);
        assert.equal(await p.locator(".led-cx .cx-name").textContent(), "Read");
        // the reasoning and the agent's own context are folded, the words are not
        assert.deepEqual(await p.locator(".led-cx details.cx-part").evaluateAll((ds) => ds.map((d) => d.open)), [false, false]);
        assert(!(await p.locator(".led-cx details.cx-part .cx-t").first().isVisible()), "folded");
        await reader.click(p, p.locator(".led-cx details.cx-part summary").first());
        assert(await p.locator(".led-cx details.cx-part").first().evaluate((d) => d.open));
        assert.equal((await p.locator(".led-cx details.cx-part .cx-t").first().textContent()).trim(), "<system-reminder>be careful</system-reminder>");
        // a long part shows some of itself and unrolls
        assert.equal(await p.locator(".led-cx .cx-part.clamp").count(), 1, "the long one only");
        const long = p.locator(".led-cx .cx-part").filter({ has: p.locator(".cx-more") }); // the same part, unrolled or not
        const short = await long.locator(".cx-t").evaluate((e) => e.getBoundingClientRect().height);
        await reader.click(p, long.locator(".cx-more"));
        assert(!(await long.evaluate((e) => e.classList.contains("clamp"))), "unrolled");
        const tall = await long.locator(".cx-t").evaluate((e) => e.getBoundingClientRect().height);
        assert(tall > short * 2, `the whole is taller: ${short} → ${tall}`);
        assert.equal((await long.locator(".cx-more").textContent()).trim(), w.less);
        assert.equal((await p.locator(".led-cx .cx-part .cx-t").last().textContent()).replace(/\s+/g, " ").trim(), "the end of a part " + w.cut);
        assert.equal((await p.locator(".led-cx .cx-none").textContent()).trim(), w.whole);
        assert.equal((await p.locator(".led-cx .cx-src").textContent()).trim(), w.src);
        // the details are inside the table's box
        const box = await p.locator(".led-box").boundingBox(), wrap = await p.locator("#ledWrap").boundingBox();
        assert(box.x >= wrap.x - 1 && box.x + box.width <= wrap.x + wrap.width + 1, "within the box");
        // asked for by its session and the span the request took, a little either side
        const g = asked.at(-1);
        assert.equal(g.session, "sess-gw");
        assert.equal(g.agent, "claude");
        const t0 = Date.parse(ROWS[0].t);
        assert.equal(Date.parse(g.from), t0 - 5e3);
        assert.equal(Date.parse(g.to), t0 + 3100 + 30e3);

        // a call of a session file: at its own time
        await reader.click(p, rows.nth(1));
        await p.locator(".led-detail").nth(1).locator(".cx-sec").first().waitFor();
        const l = asked.at(-1);
        assert.equal(l.session, "sess-log");
        assert.equal(Date.parse(l.to) - Date.parse(l.from), 2, "at its own time");
        const logged = p.locator(".led-detail").nth(1);
        assert.deepEqual(await logged.locator(".cx-role").allTextContents(), [w.tool, w.out[1]]);
        assert.equal((await logged.locator(".cx-t").last().textContent()).trim(), "There are two files.");

        // and those the files can't tell, each saying why
        for (const [i, why] of [[2, w.why[0]], [3, w.why[1]], [4, w.why[2]]]) {
          await reader.click(p, rows.nth(i));
          await p.locator(".led-detail").nth(i).locator(".cx-none", { hasText: why.slice(0, 20) }).waitFor();
          assert.equal((await p.locator(".led-detail").nth(i).locator(".cx-none").textContent()).trim(), why);
        }

        // opened again, or the list read anew, asks no more
        const n = asked.length;
        await reader.click(p, rows.nth(0)); // closed
        await reader.click(p, rows.nth(0)); // opened
        await p.locator(".led-detail").first().locator(".cx-sec").first().waitFor();
        const r = refreshed.n;
        await reader.click(p, p.locator("#ledStatus .opt").nth(1));
        await reader.click(p, p.locator("#ledStatus .opt").nth(0));
        for (let i = 0; i < 60 && refreshed.n < r + 2; i++) await p.waitForTimeout(40);
        await p.waitForTimeout(200);
        await p.locator(".led-detail").first().locator(".cx-sec").first().waitFor();
        assert.equal(asked.filter((q) => q.session === "sess-gw").length, 1, "successful content stays cached");
        assert(asked.length > n, "missing content is retried after a refresh");
        // A source that appears later can be read on reopening the detail.
        await p.route("**/api/usage/requests/content?**", async (route) => {
          const q = new URL(route.request().url()).searchParams;
          if (q.get("session") === ROWS[4].session) return route.fulfill({ json: { found: true, input: [], output: [{role:"assistant",text:"Written later"}] } });
          return route.fallback();
        });
        await reader.click(p, rows.nth(4));
        await reader.click(p, rows.nth(4));
        await p.getByText("Written later", { exact: true }).waitFor();

        if (shots) await p.screenshot({ path: path.join(shots, `content-${engine}-${lang}.png`) });
        assert.deepEqual(errors, []);
      });
    }

    await t.test("narrow", async () => {
      const errors = [], asked = [], refreshed = { n: 0 };
      const p = await (await browser.newContext({ viewport: { width: 560, height: 800 }, reducedMotion: "reduce" })).newPage();
      p.setDefaultTimeout(5000);
      p.on("pageerror", (e) => errors.push(e.message));
      await p.route("**/*", server("en", asked, refreshed));
      await p.goto("http://magpie.test/");
      await p.locator('[data-view="usage"]').first().click();
      await p.locator("#usageTab .opt").nth(1).click();
      await p.locator("#ledWrap .led tbody tr").first().waitFor();
      await reader.click(p, p.locator(".led tbody tr.led-row").nth(0));
      await p.locator(".led-cx .cx-sec").first().waitFor();
      const box = await p.locator(".led-box").boundingBox(), wrap = await p.locator("#ledWrap").boundingBox();
      assert(box.x >= wrap.x - 1 && box.x + box.width <= wrap.x + wrap.width + 1, `the details fit the box at 560: ${box.width} in ${wrap.width}`);
      assert(await p.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "the page doesn't scroll sideways");
      assert.deepEqual(errors, []);
    });
  });
}
