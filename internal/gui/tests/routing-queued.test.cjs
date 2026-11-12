// Run with Node's test runner and Playwright on the module path; see README.md.
// A request that waits for the turn before it on an account near its usage
// cap has no try yet; its row said "routing…" till that one ended (tmr11235
// on X). Its route has the "queue" phase, and the row says queued. Both
// engines, a narrow window, every language. The API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const at = (s) => new Date(now.getTime() - s * 1e3).toISOString();
const key = { id: "deepseek", provider: "deepseek", name: "DeepSeek", kind: "provider", model: "deepseek-flash" };
const net = { own: 2.4, asks: 1, send: 0.6, wait: 900, head: 950 };
const routes = [
  // waiting for the turn before it
  { id: 302, seq: 302, time: at(20), agent: "pi", model: "codex/gpt-6.1-sol", provider: "magpie", phase: "queue", order: [], tries: [], done: false, status: 0 },
  // not yet routed
  { id: 301, seq: 301, time: at(40), agent: "pi", model: "codex/gpt-6.1-sol", provider: "magpie", order: [], tries: [], done: false, status: 0 },
];

function serve(lang) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/settings.json", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 301, totals: { requests: 2, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/requests") return json({ period: url.searchParams.get("period"), rows: [], offset: 0, total: 0, calls: 0, errors: 0, input: 0, output: 0, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0, unpriced: 0, agents: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 0, errors: 0, input: 0, output: 0, cache_read: 0, cache_write: 0, reasoning: 0, unpriced: 0, cost: 0, bucket: "day", series: [], agents: [], models: [], path: "~/.config/magpie/usage.jsonl" });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const want = {
  en: { queued: "queued…", routing: "routing…" },
  zh: { queued: "排队中…", routing: "路由中…" },
  "zh-TW": { queued: "排隊中…", routing: "路由中…" },
  ja: { queued: "待機中…", routing: "ルーティング中…" },
  de: { queued: "wartet…", routing: "leitet weiter…" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(want)) {
    test(`${engine} ${lang}: a request waiting for a turn says queued`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 440, height: 900 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      t.after(() => browser.close());
      await page.goto("http://magpie.test/?view=routing");
      const rows = page.locator(".rt-req");
      await rows.nth(routes.length - 1).waitFor({ timeout: 15000 });
      const said = await rows.evaluateAll((bs) => bs.map((b) => b.textContent));
      assert.equal(said.length, 2);
      assert(said[0].includes(want[lang].queued) && !said[0].includes(want[lang].routing), said[0]);
      assert(said[1].includes(want[lang].routing) && !said[1].includes(want[lang].queued), said[1]);
      assert.deepEqual(errors, []);
    });
  }
}
