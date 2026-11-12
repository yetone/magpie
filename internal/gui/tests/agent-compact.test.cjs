// Run with Node's test runner and Playwright on the module path; see README.md.
// An agent's Long conversations, one row in its sheet (molianyin4227 on
// Discord: models differ, so one size for every agent fits none; the owner,
// 11-12: two rows for the same thing read as a duplicate). Claude Code's
// and Codex's sheets show one "Long conversations" row, in place of every
// agent's, with this agent's value (its own, else every agent's): Compact
// at a size or Full window, each posting settings/agent-compact for that
// agent alone. Every agent's row is not shown in the sheet. Its label
// follows every agent's size until the agent has its own, no click or
// typing scrolls the page, there is no native select.
// English and Chinese, Chromium and WebKit, wide and narrow; no backend,
// the API is faked here.
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
    if (url.pathname === "/api/state") return json({ agents: [agentRow("claude", "Claude Code"), agentRow("codex", "Codex")], profiles: [], settings: { lang, theme: "light" } });
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

const words = {
  en: { name: "Long conversations", at: (n) => `Compact at ${n}`, full: "Full window", bad: "Compact at: lots is not a number of tokens like 272k" },
  zh: { name: "长对话", at: (n) => `${n} 时压缩`, full: "完整窗口", bad: "压缩阈值：lots 不是 272k 这样的 token 数" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const width of [900, 420]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: an agent sets its own Long conversations`, async (t) => {
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
        const row = page.locator('.agent-compact[data-agent="claude"]');
        await row.waitFor();
        assert.equal(await row.locator(".name").textContent(), w.name);
        assert.equal(await row.locator("select").count(), 0, "no native select");
        // in every agent's place: that row is not shown in the sheet, so the
        // setting reads once
        assert.equal(await row.evaluate((r) => r.previousElementSibling?.id), "fullContextRow");
        assert(await page.locator(".agent-compact ~ #fullContextRow, #fullContextRow").first().isHidden(), "every agent's row shows beside it");
        assert.equal(await page.getByText(w.name, { exact: true }).filter({ visible: true }).count(), 1, "Long conversations shown twice");
        const seg = (label) => row.getByText(label, { exact: true });
        const on = (label) => seg(label).evaluate((b) => b.classList.contains("on") || b.getAttribute("aria-pressed") === "true" || b.getAttribute("aria-checked") === "true");
        // every agent's value until it has its own
        assert(await on(w.at("400K")), "every agent's size is shown");
        const num = row.locator("input.compact-num");
        assert.equal(await num.inputValue(), "400k");
        const view = () => page.locator("#view-agents").evaluate((v) => v.scrollTop);
        const before = await view();
        await seg(w.full).click();
        await page.waitForFunction(() => document.querySelector(".agent-compact input.compact-num")?.hidden);
        assert.deepEqual(posts.map((p) => [p.path, p.body]), [["/api/settings/agent-compact", { agent: "claude", own: true, full: true, at: 400000 }]]);
        await seg(w.at("400K")).click();
        await page.waitForFunction(() => document.querySelector(".agent-compact input.compact-num")?.hidden === false);
        assert.deepEqual(posts.at(-1).body, { agent: "claude", own: true, full: false, at: 400000 });
        await num.fill("150k");
        await num.press("Enter");
        await seg(w.at("150K")).waitFor();
        assert.deepEqual(posts.at(-1).body, { agent: "claude", own: true, full: false, at: 150000 });
        assert(!posts.some((p) => p.path === "/api/settings/full-context"), "every agent's Long conversations was not sent");
        assert(!posts.some((p) => p.path === "/api/settings"), "set on its own, not with the other preferences");
        assert.equal(await view(), before, "the row scrolled the page");
        // a size that isn't one is refused, nothing sent
        const sent = posts.length;
        await num.fill("lots");
        await num.press("Enter");
        await page.getByText(w.bad).waitFor();
        assert.equal(posts.length, sent, "a bad size sent nothing");
        // it fits a narrow window
        const over = await row.evaluate((r) => r.scrollWidth - r.clientWidth);
        assert(over <= 1, `the row overflows by ${over}px`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
