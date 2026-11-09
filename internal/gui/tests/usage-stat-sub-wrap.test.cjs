// The line under a name in the Usage page's breakdowns (by agent, model,
// account, key) is read whole in a wide window too: a sign-in's id, then its
// calls, credits and speed. In a wide window the name column is 180px, and
// the line was cut there with an ellipsis, hiding all but the provider
// (shusoyo, Discord 11-09); only the narrow layout wrapped it.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function fixture(lang) {
  const now = Date.now();
  const rows = Array.from({ length: 80 }, (_, i) => ({
    t: new Date(now - i * 60e3).toISOString(), agent: "codex", agentName: "Codex", icon: "generic",
    provider: "workbuddy", providerName: "WorkBuddy", host: "copilot.tencent.com as 15212341261",
    providerAccount: "15212341261", model: "glm-5", req: "glm-5", in: 1000, out: 100, cost: 0.25, ms: 900, status: 200, priced: true,
  }));
  const totals = { calls: rows.length, errors: 0, input: 80000, output: 8000, cache_read: 0, cache_write: 0, reasoning: 0, cost: 20, unpriced: 0 };
  const accounts = [{ id: "workbuddy@15212341261", provider: "workbuddy", account: "15212341261", name: "15212341261", sub: "WorkBuddy · 152****1261", icon: "generic", ...totals, errors: 3, retried: 2, loops: 1, timed: 80, ttft_ms: 80 * 1200 }];
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: false })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ ...totals, accounts, callerKeys: [], agents: [], models: [], series: [], bucket: "day", path: "~/.config/magpie/usage.jsonl" });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml" }[path.extname(file)] });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const width of [1440, 1000]) {
      test(`${engine} ${lang} ${width}px: a breakdown row's second line is read whole`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", fixture(lang));
        await page.goto("http://magpie.test/?view=usage");
        const sub = page.locator("#usageAccounts .row.stat .sub").first();
        await sub.waitFor();
        const text = (await sub.textContent()).trim();
        assert.match(text, /WorkBuddy/);
        assert.match(text, /1[.,]2/, "the fixture's line is long enough to overflow 180px");
        const cut = await sub.evaluate((s) => ({ sw: s.scrollWidth, cw: s.clientWidth, ws: getComputedStyle(s).whiteSpace }));
        assert.notEqual(cut.ws, "nowrap", "the line wraps, not cut");
        assert(cut.sw <= cut.cw + 1, `nothing hides past the column: ${cut.sw} > ${cut.cw}`);
        // and the row's figures are still on its right, not pushed out of the card
        const row = await page.locator("#usageAccounts .row.stat").first().boundingBox();
        const cost = await page.locator("#usageAccounts .row.stat .cost").first().boundingBox();
        assert(cost.x + cost.width <= row.x + row.width + 1);
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.locator("#usageAccounts").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-stat-sub.png`) });
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}
