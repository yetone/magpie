// Run with Node's test runner and Playwright on the module path; see README.md.
// A custom provider's "Searches the web by itself" (#359): a relay in front
// of Anthropic's or OpenAI's own API, whose web search magpie can't tell
// from its host. The editor has a Web search row with it, opening as it was
// saved; Save posts it, ticked or not. A relay with only a Chat address has
// no API to search on: the row comes once an Anthropic one is typed. A
// signed-in account (Codex here) has no such row: magpie knows how it
// searches. Its note names Codex's web.run, which goes to such a relay
// too (YIXIE233 on X), and reads whole at a phone's width. In English,
// Chinese, Traditional Chinese, Japanese and German.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
// the editor folds what few need under Advanced, this field among them:
// opened as a reader would, unless something set in it opened it already
const unfold = async (page) => {
  const adv = page.locator(".editor details.advanced");
  await adv.waitFor({ state: "attached" });
  if (!(await adv.evaluate((d) => d.open))) await adv.locator(":scope > summary").click();
};
const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "", responses: "", anthropic: "https://relay.example.com", catalog: "",
  models: [{ id: "claude-haiku-4-5", name: "Claude Haiku 4.5", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", searches: false,
};
const searching = { ...relay, id: "searching", name: "Searching", searches: true };
const chatOnly = { ...relay, id: "chatonly", name: "Chat Only", chat: "https://chat.example.com/v1", anthropic: "" };
const codex = {
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: { agent: "codex", agentName: "Codex", user: "me@example.com", plan: "PLUS", logins: [{ user: "me@example.com", plan: "PLUS", active: true, on: true }] },
  proxy: "",
};

function serve(lang, posts) {
  const providers = { providers: [relay, searching, chatOnly, codex], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/provider/") && route.request().method() === "POST") {
      posts.push({ action: url.pathname.slice("/api/provider/".length), body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { tick: "Searches the web by itself", save: "Save", more: "More endpoints", anthropic: "Anthropic URL" },
  zh: { tick: "自己能联网搜索", save: "保存", more: "更多端点", anthropic: "Anthropic 地址" },
  "zh-TW": { tick: "自己能聯網搜尋", save: "儲存", more: "更多端點", anthropic: "Anthropic 位址" },
  ja: { tick: "自分でウェブ検索する", save: "保存", more: "その他のエンドポイント", anthropic: "Anthropic URL" },
  de: { tick: "Sucht selbst im Web", save: "Speichern", more: "Weitere Endpunkte", anthropic: "Anthropic-URL" },
};
const note = "For a relay in front of Anthropic's or OpenAI's API: Claude Code's WebSearch and Codex's web_search and web.run go to it as sent, not through magpie's search. magpie doesn't automatically search with it for other models, but you can name it as the searcher.";

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    const w = words[lang];
    const open = async (t, name, width = 900) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${name}-search.png`), fullPage: true });
        }
        await browser.close();
      });
      const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posts = [];
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: name }).click();
      await unfold(page);
      await page.locator(".editor .bar").waitFor();
      return { page, errors, posts };
    };
    const box = (page) => page.locator(".editor label.tick", { hasText: w.tick }).locator("input");
    const save = async (page, posts) => {
      const n = posts.length;
      await page.locator(".editor .bar").getByRole("button", { name: w.save, exact: true }).click();
      for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50);
      return posts.at(-1);
    };

    test(`${engine} ${lang}: a relay is said to search by itself, and saved so`, async (t) => {
      const { page, errors, posts } = await open(t, "Relay");
      assert.equal(await box(page).isChecked(), false, "a relay opens as not searching");
      await box(page).scrollIntoViewIfNeeded();
      await box(page).check();
      const saved = await save(page, posts);
      assert.equal(saved.action, "save");
      assert.equal(saved.body.id, "relay");
      assert.equal(saved.body.searches, true);
      const missing = await page.evaluate((note) => ["zh", "zh-TW", "ja", "de"].flatMap((l) =>
        ["Web search", "Searches the web by itself", note].filter((k) => !I18N[l][k] || !I18N[l][note].includes("web.run")).map((k) => l + ": " + k)), note);
      assert.deepEqual(missing, [], "every string has its Chinese, Japanese and German, web.run named");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: one saved as searching opens ticked, and is saved unticked`, async (t) => {
      const { page, errors, posts } = await open(t, "Searching");
      assert.equal(await box(page).isChecked(), true);
      await box(page).scrollIntoViewIfNeeded();
      await box(page).uncheck();
      const saved = await save(page, posts);
      assert.equal(saved.body.id, "searching");
      assert.equal(saved.body.searches, false);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a Chat-only relay has the row once it has an Anthropic URL`, async (t) => {
      const { page, errors, posts } = await open(t, "Chat Only");
      const tick = page.locator(".editor label.tick", { hasText: w.tick });
      assert.equal(await tick.isVisible(), false, "no row with only a Chat address");
      await page.locator(".editor details.more summary", { hasText: w.more }).click();
      const url = page.locator(".editor details.more label", { hasText: w.anthropic }).locator("xpath=following-sibling::div[1]//input");
      await url.fill("https://relay.example.com");
      assert.equal(await tick.isVisible(), true, "the row comes with an Anthropic address");
      await box(page).scrollIntoViewIfNeeded();
      await box(page).check();
      await url.fill("");
      assert.equal(await tick.isVisible(), false, "and goes with it");
      const saved = await save(page, posts);
      assert.equal(saved.body.searches, false, "not saved as searching with nothing to search on");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: at a phone's width the note names web.run and reads whole`, async (t) => {
      const { page, errors } = await open(t, "Relay", 400);
      const tick = page.locator(".editor label.tick", { hasText: w.tick });
      await tick.scrollIntoViewIfNeeded();
      assert.equal(await tick.isVisible(), true);
      const shown = await page.evaluate((note) => t(note), note);
      assert.match(shown, /web\.run/);
      const help = page.locator(".editor").getByText(shown, { exact: true });
      assert.equal(await help.isVisible(), true, "the note is shown");
      const box = await help.boundingBox();
      assert.ok(box.x >= 0 && box.x + box.width <= 400, `the note fits the width: ${JSON.stringify(box)}`);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a signed-in account has no such row`, async (t) => {
      const { page, errors } = await open(t, "Codex");
      assert.equal(await page.locator(".editor label.tick", { hasText: w.tick }).count(), 0);
      assert.deepEqual(errors, []);
    });
  }
}
