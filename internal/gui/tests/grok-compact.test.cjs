// Run with Node's test runner and Playwright on the module path; see README.md.
// Grok Build's Long conversations with no size typed for it or for every
// agent shows Full window: Grok Build then compacts at its own 85% of the
// window (settings.ForAgent; @taktoshilite on X, magpie's 272K had a 1M
// model compact at 27%), and the row says so. Claude Code's row still
// shows 272K, a size typed for every agent shows on Grok Build's too, and
// Compact at sends Grok Build's own 272K. Every language, Chromium and
// WebKit, wide and narrow; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, agentRow, openAgentSettings } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function serve(lang, posts, compactAt) {
  let settings = { theme: "light", lang, tray: "panel", currency: "usd", version: "0.1.900", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425", proxyNow: "none", proxySource: "none", visionModels: [], imageGenModels: [], titleModels: [], workbuddyCheckins: [], lanURLs: [], redactWords: [], trayUsageEvery: 3, ...(compactAt ? { compactAt } : {}) };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [agentRow("claude", "Claude Code"), agentRow("grok", "Grok Build")], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/settings/agent-compact") {
      const body = req.postDataJSON();
      posts.push({ path: url.pathname, body });
      const own = { ...(settings.agentCompacts || {}) };
      if (body.own) own[body.agent] = { ...(body.at && body.at !== 272000 ? { at: body.at } : {}), ...(body.full ? { full: true } : {}) };
      else delete own[body.agent];
      settings = { ...settings, agentCompacts: own };
      return json(settings);
    }
    if (url.pathname === "/api/settings/full-context") {
      const body = req.postDataJSON();
      posts.push({ path: url.pathname, body });
      settings = { ...settings, fullContext: body.on, ...(body.at !== undefined ? { compactAt: body.at === 272000 ? 0 : body.at } : {}) };
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

const subs = {
  en: "Grok Build compacts at 85% of the window",
  zh: "Grok Build 在窗口的 85% 处压缩",
  "zh-TW": "Grok Build 在視窗的 85% 處壓縮",
  ja: "Grok Build はウィンドウの 85%",
  de: "Grok Build komprimiert bei 85 % des Fensters",
};

const pressed = (row, seg) => row.locator(`.segs button[data-seg="${seg}"]`).getAttribute("aria-pressed");

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(subs)) {
    for (const width of [900, 420]) {
      test(`${engine} ${lang} ${width}px: Grok Build compacts at its own percent by default`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
        t.after(() => browser.close());
        const open = async (compactAt) => {
          const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [], posts = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, posts, compactAt));
          await watchSettingsReads(page);
          await page.goto("http://magpie.test/?view=agents");
          return { page, errors, posts };
        };
        const { page, errors, posts } = await open(0);
        await openAgentSettings(page, "grok");
        const row = page.locator('.agent-compact[data-agent="grok"]');
        await row.waitFor();
        await page.waitForFunction(() => document.querySelector('.agent-compact[data-agent="grok"] .segs button[data-seg="full"]')?.getAttribute("aria-pressed") === "true");
        assert.equal(await pressed(row, "at"), "false");
        assert(await row.locator("input.compact-num").isHidden(), "the size field shows under Full window");
        assert((await row.locator(".sub").textContent()).includes(subs[lang]), await row.locator(".sub").textContent());
        // Compact at is Grok Build's own 272K
        await row.locator('.segs button[data-seg="at"]').click();
        await page.waitForFunction(() => document.querySelector('.agent-compact[data-agent="grok"] input.compact-num')?.hidden === false);
        assert.deepEqual(posts.at(-1), { path: "/api/settings/agent-compact", body: { agent: "grok", own: true, full: false, at: 0 } });
        assert.equal(await pressed(row, "at"), "true");
        const over = await row.evaluate((r) => r.scrollWidth - r.clientWidth);
        assert(over <= 1, `the row overflows by ${over}px`);
        assert.deepEqual(errors, []);
        // a size typed for every agent is Grok Build's too
        const typed = await open(900000);
        await openAgentSettings(typed.page, "grok");
        const row2 = typed.page.locator('.agent-compact[data-agent="grok"]');
        await row2.waitFor();
        await typed.page.waitForFunction(() => document.querySelector('.agent-compact[data-agent="grok"] .segs button[data-seg="at"]')?.getAttribute("aria-pressed") === "true");
        assert.equal(await row2.locator("input.compact-num").inputValue(), "900k");
        assert.deepEqual(typed.errors, []);
        // Claude Code's default is still 272K
        const fresh = await open(0);
        await openAgentSettings(fresh.page, "claude");
        const row3 = fresh.page.locator('.agent-compact[data-agent="claude"]');
        await row3.waitFor();
        await fresh.page.waitForFunction(() => document.querySelector('.agent-compact[data-agent="claude"] .segs button[data-seg="at"]')?.getAttribute("aria-pressed") === "true");
        assert.deepEqual(fresh.errors, []);
      });
    }
  }
}
