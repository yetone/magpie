// Run with Node's test runner and Playwright on the module path; see README.md.
// An agent's own context windows (ul3140773077392 on X: dsh compacts earlier
// than the other agents at the same window). Any agent with models has, in
// its sheet, "Context window": empty follows each model's own
// window; a size or model=size pairs post settings/agent-contexts for that
// agent alone, never the general preferences. A bad size is refused and
// sends nothing, the row fits a narrow window, there is no native select.
// Chromium and WebKit, every language, wide and narrow; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, agentRow, openAgentSettings } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function serve(lang, posts) {
  let settings = { theme: "light", lang, tray: "panel", currency: "usd", version: "0.1.900", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425", proxyNow: "none", proxySource: "none", visionModels: [], imageGenModels: [], titleModels: [], workbuddyCheckins: [], lanURLs: [], redactWords: [], trayUsageEvery: 3, compactAt: 400000 };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [agentRow("dsh", "Dsh"), agentRow("codex", "Codex")], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/settings/agent-contexts") {
      const body = req.postDataJSON();
      posts.push({ path: url.pathname, body });
      const own = { ...(settings.agentContexts || {}) };
      if (Object.keys(body.contexts).length) own[body.agent] = body.contexts;
      else delete own[body.agent];
      settings = { ...settings, agentContexts: own };
      return json(settings);
    }
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") posts.push({ path: url.pathname, body: req.postDataJSON() });
      return json(settings);
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    try {
      await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
    } catch {
      await route.fulfill({ status: 404, body: "" });
    }
  };
}

const words = {
  en: { name: (a) => "Context window", bad: "Context window: lots is not a length like 128k or 1m" },
  zh: { name: (a) => "上下文窗口", bad: "上下文窗口：lots 不是 128k、1m 这样的长度" },
  "zh-TW": { name: (a) => "上下文視窗", bad: "上下文視窗：lots 不是 128k、1m 這樣的長度" },
  ja: { name: (a) => "コンテキストウィンドウ", bad: "コンテキストウィンドウ：lots は 128k や 1m のような長さではありません" },
  de: { name: (a) => "Kontextfenster", bad: "Kontextfenster: lots ist keine Länge wie 128k oder 1m" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [900, 420]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: an agent sets its own context window`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        await watchSettingsReads(page);
        await page.goto("http://magpie.test/?view=agents");
        await openAgentSettings(page, "dsh");
        const row = page.locator('.agent-window[data-agent="dsh"]');
        await row.waitFor();
        assert.equal(await row.locator(".name").textContent(), w.name("Dsh"));
        assert.equal(await row.locator("select").count(), 0, "no native select");
        const num = row.locator("input.window-in");
        assert.equal(await num.inputValue(), "", "empty while it follows each model's own window");
        const view = () => page.locator("#view-agents").evaluate((v) => v.scrollTop);
        const before = await view();
        await num.fill("500k");
        await num.press("Enter");
        await page.waitForFunction(() => document.querySelector(".agent-window input")?.value === "500k");
        assert.deepEqual(posts.map((p) => [p.path, p.body]), [["/api/settings/agent-contexts", { agent: "dsh", contexts: { "*": 500000 } }]]);
        await num.fill("relay/m1=1m, 300k");
        await num.press("Enter");
        await page.waitForFunction(() => document.querySelector(".agent-window input")?.value.includes("relay/m1"));
        assert.deepEqual(posts.at(-1).body, { agent: "dsh", contexts: { "relay/m1": 1000000, "*": 300000 } });
        assert(!posts.some((p) => p.path === "/api/settings"), "set on its own, not with the other preferences");
        assert.equal(await view(), before, "the row scrolled the page");
        // a size that isn't one is refused, nothing sent
        const sent = posts.length;
        await num.fill("lots");
        await num.press("Enter");
        await page.getByText(w.bad).waitFor();
        assert.equal(posts.length, sent, "a bad size sent nothing");
        // emptied, the agent follows each model's window again
        await num.fill("");
        await num.press("Enter");
        await page.waitForFunction(() => document.querySelector(".agent-window input")?.value === "");
        assert.deepEqual(posts.at(-1).body, { agent: "dsh", contexts: {} });
        // every agent has one, its own
        await page.locator('.row.agent[data-id="codex"] .ag-link').scrollIntoViewIfNeeded();
        const over = await row.evaluate((r) => r.scrollWidth - r.clientWidth);
        assert(over <= 1, `the row overflows by ${over}px`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
