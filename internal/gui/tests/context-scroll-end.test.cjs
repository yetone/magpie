// Run with Node's test runner and Playwright on the module path; see README.md.
// Usage's Context tab scrolls to its end as it refreshes (#1249, the Routing
// page's sibling): in WebKit a session's card drawn again, while the page
// was at its end, pulled the page back up by up to the card's height, by
// the container queries its narrow layouts went by. Wheeled on at its end
// while the tab reads its sessions again (a fuller one each time), the view
// stays at its end, in Chromium and WebKit, at a phone's width and a desk's.
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

function serve(lang, onRead = () => {}) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }, { id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  let reads = 0;
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/context") {
      // each read a little fuller: the open session's card is drawn again
      const c = context(+url.searchParams.get("days")), s = c.sessions[0], tokens = 21400 + ++reads * 300;
      onRead();
      s.latest = { ...s.latest, tokens };
      s.points[2].tokens = s.peak = tokens;
      s.requests = 3 + reads;
      return json(c);
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
  test(`${engine}: the Context tab stays at its end as it refreshes`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of ["en", "zh"]) for (const [width, height] of [[420, 620], [700, 620], [1100, 620]]) {
      await t.test(`${lang}, ${width}x${height}`, async () => {
        const ctx = await browser.newContext({ viewport: { width, height }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { if (!sessionStorage.getItem("set")) { localStorage.clear(); localStorage.setItem("magpie.usageTab", "context"); sessionStorage.setItem("set", "1"); } } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        let reads = 0;
        await page.route("**/*", serve(lang, () => reads++));
        await page.goto("http://magpie.test/?view=usage");
        await page.locator(".ctx-sess-row").first().waitFor();
        // the session opened draws its card
        await page.locator(".ctx-sess-row").first().evaluate((b) => b.click());
        await page.locator(".ctx-card").first().waitFor();
        await page.waitForTimeout(500);

        const view = page.locator("#view-usage");
        const box = await view.boundingBox();
        await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
        for (let i = 0; i < 30; i++) { await page.mouse.wheel(0, 300); await page.waitForTimeout(40); }
        await page.waitForTimeout(300);
        // on at the end until the page has read the sessions again by
        // itself (every 5 s), so the open card is drawn afresh: each turn of
        // the wheel there leaves the view at its end, not pulled back from it
        const read = reads;
        const short = [];
        for (let i = 0, after = 0; i < 100 && after < 6; i++) {
          if (reads > read) after++;
          await page.mouse.wheel(0, 300);
          await page.waitForTimeout(100);
          const { range, top } = await view.evaluate((v) => ({ range: v.scrollHeight - v.clientHeight, top: v.scrollTop }));
          assert(range > 0, "the page is taller than the window here");
          if (top < range - 1) short.push(`${top} of ${range}`);
        }
        assert(reads > read, "read again");
        assert.deepEqual(short, [], "scrolled to");
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
