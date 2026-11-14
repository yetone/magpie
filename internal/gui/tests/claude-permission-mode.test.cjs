// Run with Node's test runner and Playwright on the module path; see README.md.
// Claude Code's permission mode (EricChenChannel on X: bypassPermissions set
// by hand in settings.json kept coming back): the user's own setting
// (agent.Field.Own), sent as the agent's own and not among its fields, so
// not a picker on its row but folded under Advanced in its settings, picked
// from the app's menu and posted to /api/set. Chromium and WebKit, English and Chinese; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, agentRow, openAgentSettings, pickItem } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const MODES = [
  ["default", "Default", "asks before each edit and command"],
  ["acceptEdits", "Accept edits", "edits files without asking"],
  ["plan", "Plan mode", "reads and plans, changes nothing"],
  ["auto", "Auto", "a classifier lets safe actions through"],
  ["dontAsk", "Don't ask", "refuses what isn't allowed, never asks"],
  ["bypassPermissions", "Bypass permissions", "runs everything without asking"],
];

function serve(lang, posts) {
  const settings = {
    theme: "light", lang, tray: "panel", currency: "usd", version: "0.1.900", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425", proxyNow: "none", proxySource: "none", visionModels: [], imageGenModels: [], titleModels: [], workbuddyCheckins: [], lanURLs: [], redactWords: [], trayUsageEvery: 3, compactAt: 400000,
  };
  let mode = "bypassPermissions";
  const claude = () => {
    const a = agentRow("claude", "Claude Code");
    a.own = [{ key: "permission_mode", label: "permission mode", value: mode, options: MODES.map(([value, label, note]) => ({ value, label, note })) }];
    return a;
  };
  const state = () => ({ agents: [claude(), agentRow("codex", "Codex")], profiles: [], settings: { lang, theme: "light" } });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state());
    if (url.pathname === "/api/set") {
      const body = req.postDataJSON();
      posts.push(body);
      mode = body.value;
      return json(state());
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/settings") return json(settings);
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
  en: { advanced: "Advanced", name: "Permission mode", bypass: "Bypass permissions", plan: "Plan mode", unset: "Not set", names: ["Not set", ...MODES.map((m) => m[1])] },
  zh: { advanced: "高级", name: "权限模式", bypass: "跳过权限确认", plan: "计划模式", unset: "未设置", names: ["未设置", "默认", "自动接受编辑", "计划模式", "自动", "不询问", "跳过权限确认"] },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: Claude Code's permission mode is set under Advanced in its settings`, async (t) => {
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
      const agent = page.locator('.row.agent[data-id="claude"]');
      await agent.waitFor();
      await openAgentSettings(page, "claude");
      assert.equal(await agent.locator('.field[data-key="permission_mode"]').count(), 0, "not a picker on the row");
      const fold = page.locator('.row.agent[data-id="claude"] details.own-fields');
      await fold.waitFor();
      assert.equal(await fold.evaluate((d) => d.open), false, "folded");
      assert.equal((await fold.locator("summary").textContent()).trim(), w.advanced);
      await fold.locator("summary").click();
      const row = fold.locator('.own-field[data-key="permission_mode"]');
      await row.waitFor();
      assert.equal(await row.locator(".name").textContent(), w.name);
      const btn = row.locator("button.sess-pick");
      assert.equal((await btn.textContent()).trim(), w.bypass);
      await btn.click();
      const menu = page.locator(".proto-menu");
      await menu.waitFor();
      assert.deepEqual(await menu.locator(".pm-name").allTextContents(), w.names);
      await pickItem(page, menu.locator(".pm-item", { hasText: w.plan }).first());
      await page.waitForFunction((v) => document.querySelector('.own-field[data-key="permission_mode"] button')?.textContent.trim() === v, w.plan);
      assert.deepEqual(posts, [{ agent: "claude", field: "permission_mode", value: "plan" }]);
      await btn.click();
      await menu.waitFor();
      await pickItem(page, menu.locator(".pm-item").first());
      await page.waitForFunction((v) => document.querySelector('.own-field[data-key="permission_mode"] button')?.textContent.trim() === v, w.unset);
      assert.deepEqual(posts.at(-1), { agent: "claude", field: "permission_mode", value: "" });
      await page.keyboard.press("Escape");
      await page.locator('.row.agent[data-id="codex"] .ag-link').click();
      await page.locator('.row.agent[data-id="codex"] .ag-prefs').waitFor();
      assert.equal(await page.locator('.row.agent[data-id="codex"] .own-fields').count(), 0);
      assert.deepEqual(errors, []);
    });
  }
}
