// Run with Node's test runner and Playwright on the module path; see README.md.
// Where in its window an agent compacts, a percent (moooyo on Discord:
// compact once 65% of the context is used). Claude Code's, Codex's, Grok
// Build's and Qwen Code's sheets have a "Compact at" row under Advanced,
// folded by default, with no description: Default, or a percent of the
// window, from the app's own menu (no native select), each posting
// settings/agent-compact-pct for that agent alone. An agent with no knob
// for it (OpenCode here) has none. A value outside the list (73, set
// elsewhere) is shown and offered. Every language, Chromium and WebKit,
// 1100 and 420 wide; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, agentRow, openAgentSettings, pickItem, reach } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function serve(lang, posts) {
  let settings = { theme: "light", lang, tray: "panel", currency: "usd", version: "0.1.900", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425", proxyNow: "none", proxySource: "none", visionModels: [], imageGenModels: [], titleModels: [], workbuddyCheckins: [], lanURLs: [], redactWords: [], trayUsageEvery: 3, agentCompactPcts: { grok: 73 } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [agentRow("claude", "Claude Code"), agentRow("qwen", "Qwen Code"), agentRow("grok", "Grok Build"), agentRow("opencode", "OpenCode")], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/settings/agent-compact-pct") {
      const body = req.postDataJSON();
      posts.push({ path: url.pathname, body });
      const pcts = { ...settings.agentCompactPcts };
      if (body.pct) pcts[body.agent] = body.pct;
      else delete pcts[body.agent];
      settings = { ...settings, agentCompactPcts: pcts };
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
  en: { more: "Advanced", name: "Compact at", def: "Default", pct: (n) => `${n}% of the window` },
  zh: { more: "高级", name: "压缩阈值", def: "默认", pct: (n) => `窗口的 ${n}%` },
  "zh-TW": { more: "進階", name: "壓縮閾值", def: "預設", pct: (n) => `視窗的 ${n}%` },
  ja: { more: "詳細設定", name: "圧縮しきい値", def: "デフォルト", pct: (n) => `ウィンドウの ${n}%` },
  de: { more: "Erweitert", name: "Komprimieren ab", def: "Standard", pct: (n) => `${n} % des Fensters` },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [1100, 420]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: an agent compacts at a percent of its window`, async (t) => {
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
        const view = () => page.locator("#view-agents").evaluate((v) => v.scrollTop);
        const menu = page.locator(".proto-menu");
        const label = (id) => page.locator(`.row.agent[data-id="${id}"] .compact-pct button.sess-pick`).evaluate((b) => b.textContent.trim());

        for (const id of ["claude", "qwen"]) {
          await openAgentSettings(page, id);
          const fold = page.locator(`.row.agent[data-id="${id}"] details.own-fields`);
          await fold.waitFor();
          assert.equal(await fold.evaluate((d) => d.open), false, "Advanced is folded");
          assert.equal((await fold.locator(":scope > summary").textContent()).trim(), w.more);
          const row = fold.locator(".compact-pct");
          assert(await row.isHidden(), "the row shows before Advanced is opened");
          await reach(page, fold.locator(":scope > summary"));
          await fold.locator(":scope > summary").click();
          await row.waitFor();
          assert.equal(await row.locator(".name").textContent(), w.name);
          assert.equal(await row.locator(".sub").count(), 0, "no description");
          assert.equal(await fold.locator("select").count(), 0, "no native select");
          assert.equal(await label(id), w.def);
          // brought into sight as a user scrolls to it (at 420 it sits below
          // the window); from there, the menu and its picks move nothing
          await reach(page, row.locator("button.sess-pick"));
          const before = await view();
          await row.locator("button.sess-pick").click();
          await menu.waitFor();
          assert.deepEqual(await menu.locator(".pm-name").allTextContents(), [w.def, ...[50, 60, 65, 70, 75, 80, 85, 90].map(w.pct)]);
          await pickItem(page, menu.getByText(w.pct(65), { exact: true }));
          await page.waitForFunction(([id, v]) => document.querySelector(`.row.agent[data-id="${id}"] .compact-pct button`)?.textContent.trim() === v, [id, w.pct(65)]);
          assert.deepEqual(posts.at(-1), { path: "/api/settings/agent-compact-pct", body: { agent: id, pct: 65 } });
          assert.equal(await view(), before, "picking a percent scrolled the page");
          await row.locator("button.sess-pick").click();
          await menu.waitFor();
          await pickItem(page, menu.getByText(w.def, { exact: true }));
          await page.waitForFunction(([id, v]) => document.querySelector(`.row.agent[data-id="${id}"] .compact-pct button`)?.textContent.trim() === v, [id, w.def]);
          assert.deepEqual(posts.at(-1).body, { agent: id, pct: 0 });
          assert.equal(await view(), before, "picking Default scrolled the page");
          const over = await row.evaluate((r) => r.scrollWidth - r.clientWidth);
          assert(over <= 1, `the row overflows by ${over}px`);
          await page.keyboard.press("Escape");
        }
        assert.equal(posts.length, 4, "each pick posts once");
        assert(!posts.some((p) => p.path === "/api/settings"), "set on its own, not with the other preferences");

        // a percent set elsewhere is shown, and offered in its place
        await openAgentSettings(page, "grok");
        const grok = page.locator('.row.agent[data-id="grok"] details.own-fields');
        await reach(page, grok.locator(":scope > summary"));
        await grok.locator(":scope > summary").click();
        assert.equal(await label("grok"), w.pct(73));
        await reach(page, grok.locator(".compact-pct button.sess-pick"));
        await grok.locator(".compact-pct button.sess-pick").click();
        await menu.waitFor();
        assert.deepEqual(await menu.locator(".pm-name").allTextContents(), [w.def, ...[50, 60, 65, 70, 73, 75, 80, 85, 90].map(w.pct)]);
        await page.keyboard.press("Escape");

        // no knob, no row
        await openAgentSettings(page, "opencode");
        assert.equal(await page.locator('.row.agent[data-id="opencode"] .compact-pct').count(), 0);
        assert.deepEqual(errors, []);
      });
    }
  }
}
