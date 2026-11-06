// Run with Node's test runner and Playwright on the module path; see README.md.
// Claude Code's prompt cache in its own settings, beside its compaction
// (Linka on Discord: Usage › Context's card offered no way to change a
// lifetime that was already the cheapest). Under "Long conversations in
// Claude Code" are the main conversation's and the subagents', each a
// button opening the app's menu (no native select): Claude Code's default,
// 5 min or 1 hour, posting settings/claude-cache. One a variable sets is
// shown and can't be picked. Codex's sheet has none. Nothing scrolls, the
// rows fit a narrow window. English and Chinese, Chromium and WebKit, wide
// and narrow; no backend, the API is faked here.
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
    claudeCache: [{ knob: "cache_ttl", value: "1h" }, { knob: "subagent_cache_ttl", value: "1h", locked: "CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL" }],
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [agentRow("claude", "Claude Code"), agentRow("codex", "Codex")], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/settings/claude-cache") {
      const body = req.postDataJSON();
      posts.push({ path: url.pathname, body });
      settings = { ...settings, claudeCache: settings.claudeCache.map((c) => c.knob === body.knob ? { knob: c.knob, ...(body.value ? { value: body.value } : {}) } : c) };
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
  en: { main: "Prompt cache · main conversation", sub: "Prompt cache · subagents", dflt: "Claude Code's default", m5: "5 min", h1: "1 hour", held: "CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL is set and comes first" },
  zh: { main: "提示缓存 · 主对话", sub: "提示缓存 · 子代理", dflt: "Claude Code 默认", m5: "5 分钟", h1: "1 小时", held: "已设置 CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL，它优先" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const width of [900, 420]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: Claude Code's prompt cache is set beside its compaction`, async (t) => {
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
        await openAgentSettings(page, "claude");
        const main = page.locator('.ag-prefs .claude-cache[data-knob="cache_ttl"]');
        const sub = page.locator('.ag-prefs .claude-cache[data-knob="subagent_cache_ttl"]');
        await main.waitFor();
        // right under Claude Code's own Long conversations, main first
        assert.equal(await main.evaluate((r) => r.previousElementSibling?.classList.contains("agent-compact")), true);
        assert.equal(await sub.evaluate((r) => r.previousElementSibling?.dataset.knob), "cache_ttl");
        assert.equal(await main.locator(".name").textContent(), w.main);
        assert.equal(await sub.locator(".name").textContent(), w.sub);
        assert.equal(await page.locator(".ag-prefs select").count(), 0, "no native select");
        assert.equal(await main.locator(".sub").count(), 0, "no description");
        const btn = main.locator("button.sess-pick");
        assert.equal((await btn.textContent()).trim(), w.h1);
        const view = () => page.locator("#view-agents").evaluate((v) => v.scrollTop);
        // the reader wheels the rows into sight, as one does at 420px
        const sb0 = sub.locator("button.sess-pick");
        await page.mouse.move(width / 2, 400);
        for (let i = 0; i < 40; i++) {
          const r = await sb0.boundingBox();
          if (r && r.y > 120 && r.y + r.height < 700) break;
          await page.mouse.wheel(0, 120);
          await page.waitForTimeout(30);
        }
        await page.waitForTimeout(300);
        const seen = await sb0.boundingBox();
        assert(seen && seen.y + seen.height < 760, "the rows came into sight");
        const before = await view();
        // 5 min, then Claude Code's default, each from the app's menu
        await btn.click();
        const menu = page.locator(".proto-menu");
        await menu.waitFor();
        assert.deepEqual((await menu.locator(".pm-name").allTextContents()), [w.dflt, w.m5, w.h1]);
        await pickItem(page, menu.locator(".pm-item", { hasText: w.m5 }));
        await page.waitForFunction((v) => document.querySelector('.ag-prefs .claude-cache[data-knob="cache_ttl"] button')?.textContent.trim() === v, w.m5);
        assert.deepEqual(posts.map((p) => [p.path, p.body]), [["/api/settings/claude-cache", { knob: "cache_ttl", value: "5m" }]]);
        await btn.click();
        await menu.waitFor();
        await pickItem(page, menu.locator(".pm-item", { hasText: w.dflt }));
        await page.waitForFunction((v) => document.querySelector('.ag-prefs .claude-cache[data-knob="cache_ttl"] button')?.textContent.trim() === v, w.dflt);
        assert.deepEqual(posts.at(-1).body, { knob: "cache_ttl", value: "" });
        // the same pick again sends nothing
        await btn.click();
        await menu.waitFor();
        const sent = posts.length;
        await pickItem(page, menu.locator(".pm-item", { hasText: w.dflt }));
        await menu.waitFor({ state: "detached" });
        assert.equal(posts.length, sent);
        // the subagents' is a variable's: shown, said, not pickable
        const sb = sub.locator("button.sess-pick");
        assert.equal((await sb.textContent()).trim(), w.h1);
        assert(await sb.isDisabled());
        assert.equal(await sub.locator(".sub").textContent(), w.held);
        assert.equal(await view(), before, "a pick scrolled the page");
        assert(!posts.some((p) => p.path === "/api/settings"), "set on its own, not with the other preferences");
        for (const r of [main, sub]) {
          const over = await r.evaluate((x) => x.scrollWidth - x.clientWidth);
          assert(over <= 1, `the row overflows by ${over}px`);
        }
        // Codex's sheet has no Claude Code cache
        await page.keyboard.press("Escape");
        const codexLink = page.locator('.row.agent[data-id="codex"] .ag-link');
        await codexLink.click();
        await page.locator('.row.agent[data-id="codex"] .ag-prefs').waitFor();
        assert.equal(await page.locator('.row.agent[data-id="codex"] .claude-cache').count(), 0);
        assert.deepEqual(errors, []);
      });
    }
  }
}
