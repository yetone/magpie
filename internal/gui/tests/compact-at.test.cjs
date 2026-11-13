// Run with Node's test runner and Playwright on the module path; see README.md.
// Where Codex and Claude Code compact a long conversation (#876, hisiling:
// 272K was the only size, and only for every model at once). A provider's
// editor has Compact at beside Context window and Max output, in the same
// form: one size for all its models, model=size for one, shown as set,
// sent with the Save, a value that isn't a number of tokens refused before
// anything is sent. Long conversations, in Codex's and Claude Code's
// settings on their opened rows (here Claude Code's, its one row since the
// owner merged the two, 11-12), takes a size of its own beside Compact at /
// Full window; a typed size posts settings/agent-compact for that agent with
// it, and no click or typing scrolls the page. The editor's Compact at
// says that is where its empty value follows (#3425). A size's chip, with
// models to name, opens a menu of All models and each model, so several get
// one size at once (Yanzhongixuy on X), at a phone's width too.
// English and Chinese (the chip's menu in every language), Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, agentRow, openAgentSettings } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function serve(lang, posts) {
  let settings = { theme: "light", lang, tray: "panel", currency: "usd", version: "0.1.900", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425", proxyNow: "none", proxySource: "none", visionModels: [], imageGenModels: [], titleModels: [], workbuddyCheckins: [], lanURLs: [], redactWords: [], trayUsageEvery: 3 };
  const models = ["deepseek-v4-flash", "deepseek-v4-pro"].map((id) => ({ id, name: id, on: true, context: 1000000 }));
  const provider = { id: "deepseek", name: "DeepSeek", icon: "generic", chat: "https://api.deepseek.example/v1", responses: "", anthropic: "", models, agents: [], key: { set: true, masked: "sk-…1234" }, ready: true, compacts: { "*": 500000, "deepseek-v4-flash": 272000 } };
  const providers = { providers: [provider], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [agentRow("claude", "Claude Code")], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/provider/")) {
      posts.push({ path: url.pathname, body: req.postDataJSON() });
      return json(providers);
    }
    if (url.pathname === "/api/settings/full-context") {
      const body = req.postDataJSON();
      posts.push({ path: url.pathname, body });
      settings = { ...settings, fullContext: body.on, ...(body.at !== undefined ? { compactAt: body.at === 272000 ? 0 : body.at } : {}) };
      return json(settings);
    }
    if (url.pathname === "/api/settings/agent-compact") {
      const body = req.postDataJSON();
      posts.push({ path: url.pathname, body });
      settings = { ...settings, agentCompacts: { ...(settings.agentCompacts || {}), [body.agent]: { full: body.full, at: body.at === 272000 ? 0 : body.at } } };
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
  en: { all: "All models", head: (n) => `Compact at ${n}`, field: "Compact at", save: "Save", bad: "Compact at: lots is not a number of tokens like 272k", at: (n) => `Compact at ${n}`, full: "Full window", follows: "empty follows Long conversations, in their settings on the Agents page (else in Settings)", notSettings: "Settings → Long conversations" },
  "zh-TW": { all: "全部模型", head: (n) => `${n} 時壓縮`, save: "儲存" },
  ja: { all: "すべてのモデル", head: (n) => `${n} で圧縮`, save: "保存" },
  de: { all: "Alle Modelle", head: (n) => `Bei ${n} komprimieren`, save: "Speichern" },
  zh: { all: "全部模型", head: (n) => `${n} 时压缩`, field: "压缩阈值", save: "保存", bad: "压缩阈值：lots 不是 272k 这样的 token 数", at: (n) => `${n} 时压缩`, full: "完整窗口", follows: "留空按 Agent 页里它们设置中的「长对话」（否则在「设置」里）", notSettings: "设置 → 长对话" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    const w = words[lang];
    const open = async (t, view, height = 720, width = 900) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await watchSettingsReads(page);
      await page.goto(`http://magpie.test/?view=${view}`);
      return { page, errors, posts };
    };

    if (w.field) test(`${engine} ${lang}: a provider's compaction threshold is set in its editor`, async (t) => {
      const { page, errors, posts } = await open(t, "providers");
      await page.locator('.row.provider[data-id="deepseek"]').click();
      assert(await page.getByText(w.field, { exact: true }).isVisible(), "the field is there");
      // empty follows Long conversations, which is in the agents' rows on
      // the Agents page, not in Settings, while one of them is here (#3425)
      assert(await page.getByText(w.follows).isVisible(), "says where Long conversations is");
      assert.equal(await page.getByText(w.notSettings).count(), 0, "not Settings → Long conversations");
      const box = page.locator("input.compacts");
      assert.equal(await box.inputValue(), "500k, deepseek-v4-flash=272k", "what is set is shown");
      // not a number of tokens: refused, nothing sent
      await box.fill("lots");
      await page.getByRole("button", { name: w.save, exact: true }).click();
      await page.getByText(w.bad).waitFor();
      assert.deepEqual(posts, []);
      // a size's chip fills the field for every model
      const picks = page.locator("input.compacts + .cxpicks .cxpick");
      assert.deepEqual(await picks.allTextContents(), ["128K", "200K", "272K", "500K", "1M"]);
      const top = await page.evaluate(() => document.scrollingElement.scrollTop);
      await picks.filter({ hasText: "1M" }).click();
      assert.equal(await box.inputValue(), "1m");
      assert.equal(await picks.filter({ hasText: "1M" }).evaluate((b) => b.classList.contains("on")), true);
      assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), top, "a chip scrolled the page");
      await box.fill("1m, deepseek-v4-pro=400k");
      await page.getByRole("button", { name: w.save, exact: true }).click();
      await page.waitForFunction(() => !document.querySelector("input.compacts"));
      assert.deepEqual(posts.map((p) => p.path), ["/api/provider/save"]);
      assert.deepEqual(posts[0].body.compacts, { "*": 1000000, "deepseek-v4-pro": 400000 });
      assert.deepEqual(errors, []);
    });

    for (const width of [900, 400]) test(`${engine} ${lang} ${width}px: a size's chip gives it to the models ticked`, async (t) => {
      const { page, errors, posts } = await open(t, "providers", 720, width);
      await page.locator('.row.provider[data-id="deepseek"]').click();
      const box = page.locator("input.compacts");
      const picks = page.locator("input.compacts + .cxpicks .cxpick");
      // flash is at 272K, so its chip is lit, and 500K for all of them
      assert.equal(await picks.filter({ hasText: "272K" }).evaluate((b) => b.classList.contains("on")), true);
      assert.equal(await picks.filter({ hasText: "128K" }).evaluate((b) => b.classList.contains("on")), false);
      await picks.filter({ hasText: "272K" }).scrollIntoViewIfNeeded();
      const top = await page.evaluate(() => document.scrollingElement.scrollTop);
      await picks.filter({ hasText: "272K" }).click();
      const menu = page.locator(".proto-menu");
      await menu.waitFor();
      assert.equal(await menu.locator(".pm-head").textContent(), w.head("272K"));
      assert.deepEqual(await menu.locator(".pm-name").allTextContents(), [w.all, "deepseek-v4-flash", "deepseek-v4-pro"]);
      const item = (name) => menu.locator(".pm-item").filter({ has: page.locator(".pm-name", { hasText: new RegExp(`^${name}$`) }) });
      const ticked = () => menu.locator('.pm-item[aria-checked="true"] .pm-name').allTextContents();
      assert.deepEqual(await ticked(), ["deepseek-v4-flash"]);
      assert.equal(await menu.locator("select").count() + await page.locator(".cxfield select").count(), 0, "no native select");
      await item("deepseek-v4-pro").click();
      assert.equal(await box.inputValue(), "500k, deepseek-v4-flash=272k, deepseek-v4-pro=272k");
      await item(w.all).click();
      assert.equal(await box.inputValue(), "272k, deepseek-v4-flash=272k, deepseek-v4-pro=272k");
      await item("deepseek-v4-flash").click();
      assert.equal(await box.inputValue(), "272k, deepseek-v4-pro=272k");
      assert.deepEqual(await ticked(), [w.all, "deepseek-v4-pro"]);
      assert(await menu.isVisible(), "the menu stays open while ticking");
      const r = await menu.boundingBox();
      assert(r.x >= 0 && r.x + r.width <= width, "the menu fits the window");
      assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), top, "ticking scrolled the page");
      await page.keyboard.press("Escape");
      await menu.waitFor({ state: "detached" });
      // another size leaves the models at 272K as they are
      await picks.filter({ hasText: "1M" }).click();
      await item("deepseek-v4-flash").click();
      assert.equal(await box.inputValue(), "272k, deepseek-v4-flash=1m, deepseek-v4-pro=272k");
      await page.keyboard.press("Escape");
      await menu.waitFor({ state: "detached" });
      await page.getByRole("button", { name: w.save, exact: true }).click();
      await page.waitForFunction(() => !document.querySelector("input.compacts"));
      assert.deepEqual(posts.map((p) => p.path), ["/api/provider/save"]);
      assert.deepEqual(posts[0].body.compacts, { "*": 272000, "deepseek-v4-flash": 1000000, "deepseek-v4-pro": 272000 });
      assert.deepEqual(errors, []);
    });

    if (w.field) test(`${engine} ${lang}: Long conversations compacts at a size the user types`, async (t) => {
      const { page, errors, posts } = await open(t, "agents");
      await openAgentSettings(page, "claude");
      const row = page.locator('.row.agent-compact[data-agent="claude"]');
      const num = row.locator("input.compact-num");
      await num.waitFor();
      assert.equal(await num.getAttribute("aria-label"), w.field);
      assert.equal(await row.locator("select").count(), 0, "no native select");
      assert(await row.getByText(w.at("272K"), { exact: true }).isVisible(), "272K by default");
      assert.equal(await num.inputValue(), "");
      const view = () => page.locator("#view-agents").evaluate((v) => v.scrollTop);
      const before = await view();
      await num.fill("lots");
      await num.press("Enter");
      await page.getByText(w.bad).waitFor();
      assert.deepEqual(posts, []);
      await num.fill("500k");
      await num.press("Enter");
      await row.getByText(w.at("500K"), { exact: true }).waitFor();
      assert.deepEqual(posts.map((p) => [p.path, p.body]), [["/api/settings/agent-compact", { agent: "claude", own: true, full: false, at: 500000 }]]);
      assert.equal(await row.locator("input.compact-num").inputValue(), "500k");
      // the whole window hides the size; back to it shows it again
      await row.getByText(w.full, { exact: true }).click();
      await page.waitForFunction(() => document.querySelector(".agent-compact input.compact-num")?.hidden);
      assert.deepEqual(posts.at(-1).body, { agent: "claude", own: true, full: true, at: 500000 });
      await row.getByText(w.at("500K"), { exact: true }).click();
      await page.waitForFunction(() => document.querySelector(".agent-compact input.compact-num")?.hidden === false);
      assert.deepEqual(posts.at(-1).body, { agent: "claude", own: true, full: false, at: 500000 });
      assert(!posts.some((p) => p.path === "/api/settings/full-context"), "every agent's value left as it was");
      assert.equal(await view(), before, "the row scrolled the page");
      assert(!posts.some((p) => p.path === "/api/settings"), "set on its own, not with the other preferences");
      assert.deepEqual(errors, []);
    });
  }
}
