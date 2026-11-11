// Run with Node's test runner and Playwright on the module path; see README.md.
// Claude Code's Co-Authored-By commit trailer (9_bishi on X): a row in
// Claude Code's settings, On or Off from the app's menu, posting
// settings/claude-attribution; the user's own trailer text is shown and not
// pickable. Chromium and WebKit, English and Chinese; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, agentRow, openAgentSettings, pickItem } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function serve(lang, posts) {
  let settings = {
    theme: "light", lang, tray: "panel", currency: "usd", version: "0.1.900", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425", proxyNow: "none", proxySource: "none", visionModels: [], imageGenModels: [], titleModels: [], workbuddyCheckins: [], lanURLs: [], redactWords: [], trayUsageEvery: 3, compactAt: 400000,
    claudeAttribution: "on",
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [agentRow("claude", "Claude Code"), agentRow("codex", "Codex")], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/settings/claude-attribution") {
      const body = req.postDataJSON();
      posts.push({ path: url.pathname, body });
      settings = { ...settings, claudeAttribution: body.on ? "on" : "off" };
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
  en: { name: "Commit signature (Co-Authored-By)", on: "On", off: "Off", own: "Your own text" },
  zh: { name: "提交署名（Co-Authored-By）", on: "开启", off: "关闭", own: "你自己的文本" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: Claude Code's commit signature is switched from its settings`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await watchSettingsReads(page);
      await page.goto("http://magpie.test/?view=agents");
      await openAgentSettings(page, "claude");
      const row = page.locator(".ag-prefs .claude-attribution");
      await row.waitFor();
      assert.equal(await row.locator(".name").textContent(), w.name);
      assert.equal(await row.locator(".sub").count(), 0, "no description");
      const btn = row.locator("button.sess-pick");
      assert.equal((await btn.textContent()).trim(), w.on);
      await btn.click();
      const menu = page.locator(".proto-menu");
      await menu.waitFor();
      assert.deepEqual(await menu.locator(".pm-name").allTextContents(), [w.on, w.off]);
      await pickItem(page, menu.locator(".pm-item", { hasText: w.off }));
      await page.waitForFunction((v) => document.querySelector(".ag-prefs .claude-attribution button")?.textContent.trim() === v, w.off);
      assert.deepEqual(posts.map((p) => [p.path, p.body]), [["/api/settings/claude-attribution", { on: false }]]);
      await btn.click();
      await menu.waitFor();
      await pickItem(page, menu.locator(".pm-item", { hasText: w.on }));
      await page.waitForFunction((v) => document.querySelector(".ag-prefs .claude-attribution button")?.textContent.trim() === v, w.on);
      assert.deepEqual(posts.at(-1).body, { on: true });
      assert(!posts.some((p) => p.path === "/api/settings"), "set on its own");
      await page.keyboard.press("Escape");
      await page.locator('.row.agent[data-id="codex"] .ag-link').click();
      await page.locator('.row.agent[data-id="codex"] .ag-prefs').waitFor();
      assert.equal(await page.locator('.row.agent[data-id="codex"] .claude-attribution').count(), 0);
      assert.deepEqual(errors, []);
    });
  }
}
