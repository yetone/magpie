// The Usage page's Requests, with the purpose on MCP tool calls, counts each
// tool's calls: how many, how many a day and how many failed, the most
// called first (tomtanir and lyl50478600 on X). Another purpose shows no
// such card. Run as described in README.md; no local accounts or providers
// are used.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const assets = process.env.ASSET_DIR || path.resolve(__dirname, "../assets");
const now = new Date();
const rows = [["github", "create_issue", ""], ["fetch", "fetch", ""], ["github", "create_issue", "boom"]].map(([server, tool, error], i) => ({
  route_id: 20 - i, t: new Date(now - i * 60000).toISOString(), kind: "mcp", agent: "claude", agentName: "Claude Code", icon: "claude-color",
  provider: server, providerName: server, model: tool, req: tool, served: tool, in: 0, out: 0, ms: 120, status: error ? 500 : 200, error, cost: 0, priced: true }));
const tools = [
  { server: "github", tool: "create_issue", calls: 7, failed: 1, last: rows[0].t, per_day: 1.75 },
  { server: "fetch", tool: "fetch", calls: 2, last: rows[1].t, per_day: 2 },
];
function ledger(q) {
  const mcp = q.get("purpose") === "kind:mcp";
  return { rows, offset: 0, total: rows.length, calls: rows.length, errors: 1, input: 0, output: 0, cache_read: 0, cache_write: 0, cost: 0, unpriced: 0,
    agents: [{ id: "claude", name: "Claude Code", icon: "claude-color" }], purposes: ["kind:mcp", "unmarked"], ...(mcp ? { tools } : {}) };
}
function server(lang, asked) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs={lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window={};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], models: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/usage/requests") { asked.push(url.searchParams); return json(ledger(url.searchParams)); }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}
const words = {
  en: { title: "Tool calls", head: ["Tool", "Calls", "Per day", "Failed"], rate: "1.8", failed: "1 of 7 failed" },
  zh: { title: "工具调用", head: ["工具", "调用次数", "每天", "失败"], rate: "1.8", failed: "7 次中失败 1 次" },
  "zh-TW": { title: "工具呼叫", head: ["工具", "呼叫次數", "每天", "失敗"], rate: "1.8", failed: "7 次中失敗 1 次" },
  ja: { title: "ツール呼び出し", head: ["ツール", "呼び出し数", "1日あたり", "失敗"], rate: "1.8", failed: "7 回中 1 回失敗" },
  de: { title: "Tool-Aufrufe", head: ["Tool", "Aufrufe", "Pro Tag", "Fehlgeschlagen"], rate: "1,8", failed: "1 von 7 fehlgeschlagen" },
};
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    test(`${engine} ${lang}: MCP tool calls are counted by tool`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 800 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(7000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], asked = [];
      page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
      await page.route("**/*", server(lang, asked));
      // no purpose picked: no count of tools
      await page.goto("http://magpie.test/?view=usage&tab=requests");
      await page.waitForFunction(() => document.querySelectorAll("#ledWrap tbody tr.led-row").length === 3);
      assert.equal(await page.locator("#ledTools").isHidden(), true);

      await page.goto("http://magpie.test/?view=usage&tab=requests&purpose=kind:mcp");
      await page.waitForFunction(() => document.querySelectorAll("#ledTools tbody tr").length === 2);
      assert.equal(asked.at(-1).get("purpose"), "kind:mcp");
      const w = words[lang];
      assert.equal(await page.locator("#ledTools .led-spd-t").textContent(), w.title);
      assert.deepEqual(await page.locator("#ledTools thead th").allTextContents(), w.head);
      const cells = await page.locator("#ledTools tbody tr").evaluateAll((trs) => trs.map((tr) => [...tr.cells].map((c) => c.textContent)));
      assert.deepEqual(cells, [["create_issuegithub", "7", w.rate, "1"], ["fetchfetch", "2", "2", "0"]]);
      assert.equal(await page.locator("#ledTools tbody tr").first().locator("td").last().getAttribute("title"), w.failed);
      assert.match(await page.locator("#ledTools tbody tr").last().locator("td").last().getAttribute("class"), /faint/);
      assert.deepEqual(errors, []);
    });
  }
}
