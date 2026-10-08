// Run with Node's test runner and Playwright on the module path; see README.md.
// Usage's Context tab: each agent's prompts as the gateway read them, a
// score out of 100 with its grade, tags with what they are told by, and
// its sessions' fill; a session opened draws its latest request's context
// window — 400 cells by what the prompt holds, a cell hovered telling its
// part and the largest things in it, the contents by part. The refresh
// that brings the same answer keeps the pane as it was. In every language,
// at a desk and a phone's width.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const ago = (m) => new Date(now - m * 60000).toISOString();

const latest = {
  window: 272000, tokens: 21400, counted: true, turns: 3,
  parts: [
    { kind: "system", tokens: 5200, items: [{ name: "prompt", tokens: 4900 }, { name: "environment_context", tokens: 300 }] },
    { kind: "tools", tokens: 5000, items: [{ name: "multi_agent_v1", tag: "namespace", n: 5, tokens: 2900 }, { name: "exec_command", tokens: 2100 }] },
    { kind: "memory", tokens: 3300, items: [{ name: "skills", tag: "skills", tokens: 3300 }] },
    { kind: "files", tokens: 5900, items: [{ name: "/work/app/prompt.go", tag: "shell", tokens: 2950 }, { name: "/work/app/context.go", tag: "shell", tokens: 2950 }] },
    { kind: "results", tokens: 1200, items: [{ name: "exec_command", tag: "result", n: 4, tokens: 1200 }] },
    { kind: "chat", tokens: 800, items: [{ name: "turn", tag: "turn", n: 3, tokens: 800 }] },
  ],
};
const context = (days) => ({
  days,
  agents: [
    { agent: "codex", requests: 9, sessions: 1, errors: 0, calls: 9, median: 17700, p90: 21400, baseline: 13300, window: 272000, cache: 0.74, growth: 228, peakFill: 0.08, compacts: 0,
      shares: { system: 0.29, tools: 0.28, memory: 0.18, files: 0.18, results: 0.04, chat: 0.03 }, mcp: 0,
      score: 94, scores: [{ key: "cache", points: 29, most: 35 }, { key: "lean", points: 25, most: 25 }, { key: "pace", points: 20, most: 20 }, { key: "reliable", points: 20, most: 20 }],
      tags: [{ key: "lean", tone: "good", value: 13300 }, { key: "memory-heavy", tone: "info", value: 0.18 }],
      models: ["live/codex/gpt-5.6-luna"], latestId: 19, latestTime: ago(4) },
    { agent: "claude", requests: 12, sessions: 2, errors: 3, calls: 15, median: 140000, p90: 180000, baseline: 70000, window: 200000, cache: 0.2, growth: 9000, peakFill: 0.9, compacts: 1,
      shares: { system: 0.1, tools: 0.6, memory: 0.05, files: 0.1, results: 0.1, chat: 0.05 }, mcp: 28000,
      score: 31, scores: [{ key: "cache", points: 8, most: 35 }, { key: "lean", points: 3, most: 25 }, { key: "pace", points: 10, most: 20 }, { key: "reliable", points: 10, most: 20 }],
      tags: [{ key: "cache-misses", tone: "warn", value: 0.2 }, { key: "heavy-start", tone: "warn", value: 70000 }, { key: "mcp-heavy", tone: "info", value: 28000 }],
      models: ["claude-sonnet-5"], latestId: 30, latestTime: ago(9) },
  ],
  sessions: [
    { agent: "codex", key: "01a117b1-aecd-76e2-9b2f-3c1d", title: "Port the parser", model: "live/codex/gpt-5.6-luna", first: ago(12), last: ago(4), requests: 3, peak: 21400, window: 272000,
      latest, latestId: 19, points: [{ id: 17, time: ago(12), tokens: 13000, cache: 0 }, { id: 18, time: ago(8), tokens: 17700, cache: 12000 }, { id: 19, time: ago(4), tokens: 21400, cache: 20900 }] },
    { agent: "claude", key: "ce0c93f6-1111-2222-3333-444444444444", model: "claude-sonnet-5", first: ago(30), last: ago(9), requests: 1, peak: 180000, window: 200000,
      latest: { ...latest, window: 200000, tokens: 180000 }, latestId: 30, points: [{ id: 30, time: ago(9), tokens: 180000 }] },
  ],
});

// the reader scrolls (the wheel) until what they'll point at is in view:
// the app puts back a scroll that isn't the reader's, a test's own included
async function wheelTo(page, l) {
  await page.mouse.move(200, 300);
  for (let i = 0; i < 40 && await l.evaluate((b) => b.getBoundingClientRect().bottom > innerHeight - 70); i++) {
    await page.mouse.wheel(0, 120);
    await page.waitForTimeout(50);
  }
  // a wheel scroll is eased: hovered while it still runs, the cell moves out
  // from under the pointer and its tip closes again. Wait until it stops.
  const view = page.locator("#view-usage");
  for (let last = -1, i = 0; i < 40; i++) {
    await page.waitForTimeout(100);
    const now = await view.evaluate((v) => v.scrollTop);
    if (now === last) break;
    last = now;
  }
}

const words = {
  en: { tab: "Context", excellent: "Excellent", care: "Needs care", lean: "Lean start", heavy: "Heavy start", one: "1 session", two: "2 sessions", card: "Context window", tools: "Tools", free: "Free space" },
  zh: { tab: "上下文", excellent: "优秀", care: "待改进", lean: "起步轻", heavy: "起步重", one: "1 个会话", two: "2 个会话", card: "上下文窗口", tools: "工具", free: "剩余空间" },
  "zh-TW": { tab: "上下文", excellent: "優秀", care: "待改進", lean: "起步輕", heavy: "起步重", one: "1 個會話", two: "2 個會話", card: "上下文視窗", tools: "工具", free: "剩餘空間" },
  ja: { tab: "コンテキスト", excellent: "優秀", care: "要改善", lean: "軽いスタート", heavy: "重いスタート", one: "1 件のセッション", two: "2 件のセッション", card: "コンテキストウィンドウ", tools: "ツール", free: "空き" },
  de: { tab: "Kontext", excellent: "Ausgezeichnet", care: "Ausbaufähig", lean: "Schlanker Start", heavy: "Schwerer Start", one: "1 Sitzung", two: "2 Sitzungen", card: "Kontextfenster", tools: "Tools", free: "Frei" },
};

function serve(lang, asked) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }, { id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/context") {
      asked.push(url.searchParams.get("days"));
      return json(context(+url.searchParams.get("days")));
    }
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType, status: 200 });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [1100, 420]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: the Context tab`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 1400 } })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [], asked = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, asked));
        await page.addInitScript(() => { try { if (!sessionStorage.getItem("set")) { localStorage.clear(); localStorage.setItem("magpie.usageTab", "context"); sessionStorage.setItem("set", "1"); } } catch {} });
        await page.goto("http://magpie.test/?view=usage");
        assert.equal(await page.locator("#usageTab button.on").innerText(), w.tab);

        // each agent: its score, grade and tags; one session or more
        const codex = page.locator(".ctx-agent", { hasText: "Codex" });
        const claude = page.locator(".ctx-agent", { hasText: "Claude Code" });
        await codex.waitFor();
        assert.equal(asked[0], "7", "a week at first");
        assert.match(await codex.locator(".ctx-ring").innerText(), /94/);
        assert.equal((await codex.locator(".ctx-grade-word").innerText()).trim(), w.excellent);
        assert.equal((await claude.locator(".ctx-grade-word").innerText()).trim(), w.care);
        assert.ok(await codex.locator(".ctx-chip", { hasText: w.lean }).isVisible());
        assert.ok(await claude.locator(".ctx-chip", { hasText: w.heavy }).isVisible());
        assert.match(await codex.locator(".ctx-who-sub").innerText(), new RegExp(w.one));
        assert.match(await claude.locator(".ctx-who-sub").innerText(), new RegExp(w.two));
        // a score bar is toned by how much of it there is
        assert.equal(await codex.locator(".ctx-score.ok").count(), 4);
        assert.equal(await claude.locator(".ctx-score.bad").count(), 2);
        assert.equal(await claude.locator(".ctx-score.warn").count(), 2);

        // the refresh that brings the same answer keeps the pane
        const before = await page.locator(".ctx-agent").first().elementHandle();
        await page.evaluate(() => window.loadContext());
        assert.ok(await before.evaluate((e) => e.isConnected), "the same answer redrew the pane");

        // a session opened draws its latest request's window
        const row = page.locator(".ctx-sess-row", { hasText: "Port the parser" });
        await wheelTo(page, row);
        const y = await page.evaluate(() => document.querySelector("#view-usage").scrollTop);
        await row.click();
        const card = page.locator(".ctx-sess.open .ctx-card");
        await card.waitFor();
        assert.equal(await page.evaluate(() => document.querySelector("#view-usage").scrollTop), y, "a click doesn't move the page");
        assert.equal(await row.getAttribute("aria-expanded"), "true");
        assert.match(await card.locator(".ctx-head").innerText(), new RegExp(w.card));
        assert.equal(await card.locator(".ctx-waffle > i").count(), 400);
        assert.match(await card.locator(".ctx-legend").innerText(), new RegExp(w.free));

        // a cell hovered tells its part and what is largest in it, and
        // dims the others
        const cell = card.locator(".ctx-waffle > i.k-tools").first();
        await wheelTo(page, cell);
        await cell.hover();
        const tip = card.locator(".ctx-tip");
        await tip.waitFor({ state: "visible" });
        assert.match(await tip.innerText(), new RegExp(w.tools));
        assert.match(await tip.innerText(), /multi_agent_v1/);
        assert.equal(await card.locator(".ctx-waffle").getAttribute("data-f"), "tools");

        // the contents by part: a file by its name, its folder beside it
        const contents = card.locator(".ctx-contents");
        const seg = contents.locator(".segs button", { hasText: w.tools }).first();
        await wheelTo(page, seg);
        await seg.click();
        const names = await contents.locator(".ctx-row .ctx-name").allInnerTexts();
        assert.ok(names.some((n) => n.includes("multi_agent_v1")) && !names.some((n) => n.includes("prompt.go")), names.join(" | "));

        // nothing wider than the window
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), "the page scrolls sideways");
        assert.deepEqual(errors, []);
      });
    }
  }
}
