// Run with Node's test runner and Playwright on the module path; see README.md.
// #925: the Usage page's Agent list said how much each agent read from the
// cache ("9.0M cached") but not what share of its prompt that was, so one
// agent's caching couldn't be told from another's. Each row now adds its hit
// rate, counted as the Cache read tile counts it: cache read over input +
// cache read + cache write. A row with no cache read shows none; in English
// and Chinese. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const row = (name, input, cache_read, cache_write) => ({ name, icon: "", calls: 10, errors: 0, input, output: 1000, cache_read, cache_write, reasoning: 0, cost: 0, unpriced: 0 });
// Pi: 9M of 10M from the cache → 90%; OpenCode: 1M read, 2M in, 1M written → 25%; omp: none
const agents = [row("Pi", 900000, 9000000, 100000), row("OpenCode", 2000000, 1000000, 1000000), row("omp", 500000, 0, 0)];
const sum = (k) => agents.reduce((n, a) => n + a[k], 0);
const usage = { name: "", calls: 30, errors: 0, input: sum("input"), output: 3000, cache_read: sum("cache_read"), cache_write: sum("cache_write"), reasoning: 0, cost: 0, unpriced: 0,
  path: "/test/usage.jsonl", bucket: "day", agents, models: [], series: [] };

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], presets: [], excluded: [], gateway: { running: true, calls: [], groups: [] } } });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [], pools: [], models: [] } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/usage") return route.fulfill({ json: usage });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404 }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, rate] of [["en", "hit rate"], ["zh", "命中率"]]) {
    test(`${engine} ${lang}: each agent row on the Usage page shows its cache hit rate`, async () => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      try {
        const page = await browser.newPage({ viewport: { width: 1280, height: 900 }, reducedMotion: "reduce" });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        await page.route("http://magpie.test/**", server(lang));
        await page.goto("http://magpie.test/?view=usage");
        await page.locator("#usageAgents .row.stat").nth(2).waitFor();
        const notes = await page.locator("#usageAgents .row.stat .num small").allTextContents();
        assert.equal(notes.length, 3, notes.join(" | "));
        assert.ok(notes[0].includes(`${rate} 90.00%`), notes[0]);
        assert.ok(notes[1].includes(`${rate} 25.00%`), notes[1]);
        assert.ok(!notes[2].includes(rate), notes[2]);
      } finally {
        await browser.close();
      }
    });
  }
}
