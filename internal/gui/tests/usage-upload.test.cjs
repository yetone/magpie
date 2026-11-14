// Run with Node's test runner and Playwright on the module path; see README.md.
// What the bytes sent upstream were made of (theforeveriris on X: 8169
// requests, "↑ 8.6 GB" from plain-text projects). The Requests card's ↑ is
// a button that opens a card under the strip: the bytes a request, a line
// saying each request sends the whole conversation again, a bar and its
// legend of the conversation, the tools' definitions, the system prompt,
// images and what wasn't measured, and the bytes of each agent and each model,
// largest first. It stays open as left across a reload and closes from its
// ×. A request's details say what its bytes were made of; one that kept no
// parts says nothing. A period with nothing sent has no button. At 1180 and
// 420 wide nothing scrolls sideways. Chromium and WebKit, every language;
// no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();

const row = (rid, more) => ({ t: new Date(now - 60e3).toISOString(), agent: "codex", agentName: "Codex", icon: "codex-color", provider: "openai", providerName: "OpenAI", req: "gpt-5", model: "gpt-5", served: "gpt-5", in: 300, out: 40, ms: 2380, status: 200, rid, ep: "/v1/chat/completions", cost: 0.01, priced: true, ...more });
const ROWS = [
  row("req_parts", { up_bytes: 1_000_000, down_bytes: 3_000, up_parts: { system: 200_000, tools: 50_000 } }),
  row("req_plain", { up_bytes: 40_000, down_bytes: 900 }),
];
const UP = { up_bytes: 8_600_000_000, up_calls: 8169, up_measured: 8_000_000_000, up_system: 1_000_000_000, up_tools: 500_000_000, up_images: 100_000_000, down_bytes: 1_000_000_000 };
const share = (id, name, up, calls) => ({ id, name, calls, input: 1, output: 1, up_bytes: up, up_calls: calls });
const BY = {
  agent: [share("claude", "Claude Code", 2_100_000_000, 4000), share("codex", "Codex", 6_500_000_000, 4169), share("gemini", "Gemini CLI", 0, 0)],
  model: [share("gpt-5", "gpt-5", 6_500_000_000, 4169), share("claude-sonnet", "claude-sonnet", 2_100_000_000, 4000)],
};

function page(q, sent) {
  return {
    period: q.get("period"), rows: ROWS, offset: 0, total: ROWS.length, calls: 8169, errors: 0,
    input: 700, output: 80, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0.03, unpriced: 0,
    ...(sent ? UP : {}), by: sent ? BY : {},
    agents: [{ id: "codex", name: "Codex", icon: "codex-color" }],
  };
}

function server(lang, sent) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: { rate: 7.2, at: new Date().toISOString() } });
    if (url.pathname === "/api/usage/requests") return json(page(url.searchParams, sent));
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 3, errors: 0, input: 700, output: 80, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 0, cost: 0.03, bucket: "day", series: [], agents: [], models: [], path: "~/.config/magpie/usage.jsonl" });
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
  en: { each: "≈ 1.1 MB a request", conv: "Conversation", tools: "Tool definitions", system: "System prompt", images: "Images", rest: "Not measured", agent: "Agent", model: "Model", made: "Made of", note: /whole conversation again/ },
  zh: { each: "每个请求约 1.1 MB", conv: "会话内容", tools: "工具定义", system: "系统提示词", images: "图像", rest: "未测量", agent: "Agent", model: "模型", made: "组成", note: /整段对话/ },
  "zh-TW": { each: "每個請求約 1.1 MB", conv: "會話內容", tools: "工具定義", system: "系統提示詞", images: "影像", rest: "未測量", agent: "Agent", model: "模型", made: "組成", note: /整段對話/ },
  ja: { each: "1 リクエストあたり約 1.1 MB", conv: "会話", tools: "ツール定義", system: "システムプロンプト", images: "画像", rest: "未計測", agent: "エージェント", model: "モデル", made: "内訳", note: /会話全体/ },
  de: { each: "≈ 1.1 MB pro Anfrage", conv: "Unterhaltung", tools: "Tool-Definitionen", system: "Systemprompt", images: "Bilder", rest: "Nicht gemessen", agent: "Agent", model: "Modell", made: "Zusammensetzung", note: /ganze Unterhaltung/ },
};

async function open(browser, lang, width, sent, storage) {
  const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce", ...(storage ? { storageState: storage } : {}) });
  const p = await ctx.newPage();
  p.setDefaultTimeout(5000);
  p.setDefaultNavigationTimeout(60000);
  const errors = [];
  p.on("pageerror", (e) => errors.push(e.message));
  await p.route("**/*", server(lang, sent));
  await p.goto("http://magpie.test/");
  await p.locator('[data-view="usage"]').first().click();
  await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
  await p.locator("#ledWrap .led tbody tr.led-row").first().waitFor();
  await p.locator("#ledKpi .blk").first().waitFor();
  return { ctx, p, errors };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": what the bytes sent upstream were made of", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      const w = L[lang];
      for (const width of [1180, 420]) {
        await t.test(lang + " " + width, async () => {
          const { ctx, p, errors } = await open(browser, lang, width, true);
          const btn = p.locator("#ledKpi .led-io .led-up-open");
          assert.equal(await btn.count(), 1);
          assert.equal((await btn.textContent()).trim(), "↑ 8.6 GB");
          assert.equal(await btn.getAttribute("aria-expanded"), "false");
          assert.equal(await p.locator("#ledUp").isVisible(), false);
          await btn.click();
          const box = p.locator("#ledUp");
          await box.waitFor();
          assert.equal(await btn.getAttribute("aria-expanded"), "true");
          assert.equal((await box.locator(".led-up-each").textContent()).trim(), w.each);
          assert.match(await box.locator(".led-up-note").textContent(), w.note);
          // the legend: each part, its bytes, its share, in this order
          const keys = await box.locator(".led-up-key").evaluateAll((ks) => ks.map((k) => [...k.querySelectorAll("span, b")].map((s) => s.textContent)));
          assert.deepEqual(keys, [
            [w.conv, "6.4 GB", "74%"], [w.tools, "500 MB", "6%"], [w.system, "1.0 GB", "12%"], [w.images, "100 MB", "1%"], [w.rest, "600 MB", "7%"],
          ]);
          // the bar's segments are as wide as their shares
          const segs = await box.locator(".led-up-bar").evaluate((b) => [b.getBoundingClientRect().width, ...[...b.children].map((s) => s.getBoundingClientRect().width)]);
          assert.equal(segs.length, 6);
          assert(segs[1] > segs[3] && segs[3] > segs[2] && segs[2] > segs[4], `segments ${segs}`);
          // by agent and by model, largest first, with the bytes a request; none for an agent that sent nothing
          const tables = await box.locator(".led-up-by").evaluateAll((ts) => ts.map((tb) => [...tb.querySelectorAll("tr")].map((tr) => [...tr.children].map((c) => c.textContent.trim()))));
          assert.deepEqual(tables, [
            [[w.agent, tables[0][0][1], tables[0][0][2]], ["Codex", "6.5 GB", "1.6 MB"], ["Claude Code", "2.1 GB", "525 KB"]],
            [[w.model, tables[1][0][1], tables[1][0][2]], ["gpt-5", "6.5 GB", "1.6 MB"], ["claude-sonnet", "2.1 GB", "525 KB"]],
          ]);
          const sideways = await p.evaluate(() => [document.documentElement.scrollWidth - innerWidth, ...[...document.querySelectorAll("#ledKpi .blk, #ledUp")].map((b) => b.scrollWidth - b.clientWidth)]);
          assert(sideways.every((n) => n <= 1), `scrolls sideways: ${sideways}`);

          // a request's details: what its bytes were made of; one without parts, nothing
          const rows = p.locator(".led tbody tr.led-row");
          const pairs = () => p.locator("#ledReqPane .led-detail").evaluate((d) => Object.fromEntries([...d.querySelectorAll("dt")].map((dt) => [dt.textContent, dt.nextElementSibling.textContent])));
          await reader.click(p, rows.nth(0));
          await p.locator("#ledReqPane .led-detail").waitFor();
          assert.equal((await pairs())[w.made], `${w.conv} 750 KB · ${w.tools} 50 KB · ${w.system} 200 KB`);
          await reader.closeRequest(p);
          await reader.click(p, rows.nth(1));
          await p.locator("#ledReqPane .led-detail").waitFor();
          assert(!(w.made in (await pairs())), "a row with no parts says what it was made of");
          await reader.closeRequest(p);

          // kept open across a reload, closed from its ×
          const state = await ctx.storageState();
          const again = await open(browser, lang, width, true, state);
          await again.p.locator("#ledUp").waitFor();
          assert.equal(await again.p.locator("#ledKpi .led-up-open").getAttribute("aria-expanded"), "true");
          await again.p.locator("#ledUp .led-up-x").click();
          assert.equal(await again.p.locator("#ledUp").isVisible(), false);
          assert.equal(await again.p.locator("#ledKpi .led-up-open").getAttribute("aria-expanded"), "false");
          assert.deepEqual([...errors, ...again.errors], []);
          await again.ctx.close();
          await ctx.close();
        });
      }
      await t.test(lang + " nothing sent", async () => {
        const { ctx, p, errors } = await open(browser, lang, 1180, false);
        assert.equal(await p.locator("#ledKpi .led-up-open").count(), 0);
        assert.equal(await p.locator("#ledUp").isVisible(), false);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
