// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group carries its own auto-compaction size (NovaStudiosss on X:
// no need to set a context size and a compaction size on each provider
// model). The group editor's Advanced "Compact at" box takes 160k or 1m;
// empty is none (0, the members' own). It is saved with the group and a
// bad length is marked as it is typed. English, Chinese, Traditional
// Chinese, Japanese and German, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
// the rows a group's editor folds under Advanced, opened
const openAdvanced = async (page) => {
  const d = page.locator(".rt-gedit .rt-gadv");
  await d.waitFor({ state: "attached" });
  if (!(await d.evaluate((e) => e.open))) await d.locator(":scope > summary").click();
};

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "oa/gpt-6.1-sol", name: "gpt-6.1-sol", providerName: "OpenAI", icon: "generic", context: 1000000 },
  { id: "an/claude-opus-5-5", name: "claude-opus-5-5", providerName: "Anthropic", icon: "generic", context: 200000 },
  { id: "ds/deepseek-flash", name: "deepseek-flash", providerName: "DeepSeek", icon: "generic", context: 128000 },
];
const members = models.map((m) => m.id);
const saved = { context: 0, compact: 0 };
const groups = () => ({
  models, pools: [],
  groups: [{ id: "sol", name: "Sol", members, routing: "order", ready: true, context: saved.context, compact: saved.compact,
    memberInfo: members.map((id) => ({ id, ready: true })) }],
});

const words = {
  en: { edit: "Edit", save: "Save", label: "Compact at" },
  zh: { edit: "编辑", save: "保存", label: "压缩阈值" },
  "zh-TW": { edit: "編輯", save: "儲存", label: "壓縮閾值" },
  ja: { edit: "編集", save: "保存", label: "圧縮しきい値" },
  de: { edit: "Bearbeiten", save: "Speichern", label: "Komprimieren ab" },
};

function serve(lang, posts) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname.startsWith("/api/groups/")) {
      const body = JSON.parse(r.request().postData() || "{}");
      posts.push({ path: url.pathname, body });
      saved.context = body.context || 0;
      saved.compact = body.compact || 0;
      return json(groups());
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    test(`${engine} ${lang}: a group carries its own Compact at`, async (t) => {
      saved.context = 0;
      saved.compact = 0;
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 2000 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=groups");
      const card = page.locator(".rt-group", { hasText: "Sol" });
      await card.waitFor();
      const ed = page.locator(".rt-gedit");
      const box = ed.locator("input.cmp-in");
      const save = async () => {
        await ed.locator("button.primary", { hasText: w.save }).click();
        await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
        return posts.filter((p) => p.path === "/api/groups/save").at(-1).body;
      };
      const open = async () => { await card.locator("button", { hasText: w.edit }).click(); await openAdvanced(page); await box.waitFor(); };

      await open();
      assert.equal(await box.getAttribute("aria-label"), w.label);
      assert.equal(await box.inputValue(), "", "none until set");
      assert.equal((await save()).compact, 0);

      await open();
      await box.fill("abc");
      assert.equal(await box.getAttribute("aria-invalid"), "true");
      await box.fill("160k");
      assert.equal(await box.getAttribute("aria-invalid"), "false");
      assert.equal((await save()).compact, 160000);

      await card.locator("button", { hasText: w.edit }).click();
      await box.waitFor();
      assert.equal(await box.inputValue(), "160k", "the editor opens at what was saved");
      assert(await ed.locator(".rt-gadv").evaluate((e) => e.open), "a set size opens Advanced");
      await box.fill("");
      assert.equal((await save()).compact, 0, "emptied, the group has none");

      const missing = await page.evaluate(() => ["Compact at", "Default"].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });
  }
}
