// Run with Node's test runner and Playwright on the module path; see README.md.
// A group's free: rule (#5903, provider.FreePrefix): in the editor's
// pattern box free:wb/* takes, of WorkBuddy's models, only those the
// catalog marks free (free: true), never one priced at $0 (zero), at a
// rate or with free in its id, nor another provider's free one. It shows
// its live count as any pattern does, and one line says the flag is the
// last catalog's, not a promise of no charge. Saved, the rule is sent as
// typed. In English and Chinese; every language has the line.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "wb/hy4free", name: "hy4free", providerName: "WorkBuddy", icon: "generic", free: true },
  { id: "wb/glm-5.1", name: "glm-5.1", providerName: "WorkBuddy", icon: "generic" },
  { id: "wb/hy3", name: "hy3", providerName: "WorkBuddy", icon: "generic", free: true },
  { id: "wb/deepseek-v4-free", name: "deepseek-v4-free", providerName: "WorkBuddy", icon: "generic" },
  { id: "wb/hy-zero", name: "hy-zero", providerName: "WorkBuddy", icon: "generic", zero: true },
  { id: "wb/hy4flash-exp4", name: "hy4flash-exp4", providerName: "WorkBuddy", icon: "generic", free: true },
  { id: "cl/mimo", name: "mimo", providerName: "Cline", icon: "generic", free: true },
  { id: "an/claude-opus-5-5", name: "claude-opus-5-5", providerName: "Anthropic", icon: "generic" },
];
const members = ["an/claude-opus-5-5"];
const groups = () => ({
  models, pools: [],
  groups: [{ id: "pool", name: "Pool", members, match: [], matched: [], fast: [], off: [], routing: "order", ready: true, patterns: [],
    memberInfo: members.map((id) => ({ id, ready: true })) }],
});
const LINE = "free: before a pattern keeps the models the last catalog marked free, as in free:workbuddy/*. It is the catalog's flag, not a promise of no charge.";

const words = {
  en: { edit: "Edit", save: "Save", add: "Add pattern", advanced: "Advanced", three: "3 models", line: LINE },
  zh: { edit: "编辑", save: "保存", add: "添加模式", advanced: "高级", three: "3 个模型", line: "模式前加 free: 只保留最近一次目录标为免费的模型，如 free:workbuddy/*。这是目录的标记，不保证不扣费。" },
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
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname.startsWith("/api/groups/")) {
      posts.push({ path: url.pathname, body: JSON.parse(r.request().postData() || "{}") });
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
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a free: rule takes the models the catalog marks free`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 2000 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=groups");
      const card = page.locator(".rt-group", { hasText: "Pool" });
      await card.waitFor();
      await card.locator("button", { hasText: w.edit }).click();
      const ed = page.locator(".rt-gedit");
      const names = () => ed.locator(".fallback:not(.rt-pats) .fbrow .n > span:first-child").allTextContents();
      await ed.locator(".fallback:not(.rt-pats) .fbrow").first().waitFor();
      assert.deepEqual(await names(), ["claude-opus-5-5"]);

      const adv = ed.locator("details.rt-gadv");
      if (!(await adv.evaluate((d) => d.open))) await adv.locator("summary").click();
      assert.equal(await adv.locator(".hint", { hasText: w.line }).count(), 1, "the line says what free: is");
      const pbox = ed.locator(".rt-pats");
      await pbox.locator("input").fill("free:wb/*");
      await pbox.locator("button", { hasText: w.add }).click();
      assert.deepEqual(await names(), ["claude-opus-5-5", "hy4free", "hy3", "hy4flash-exp4"], "only those marked free, of WorkBuddy's, in the catalog's order");
      const rule = pbox.locator(".fbrow").first();
      assert.equal(await rule.locator("code").textContent(), "free:wb/*");
      assert.equal(await rule.locator("small").textContent(), w.three, "its live count");

      // the plain pattern beside it is as it was: every one of WorkBuddy's
      await pbox.locator("input").fill("wb/*-free");
      await pbox.locator("button", { hasText: w.add }).click();
      assert.deepEqual(await names(), ["claude-opus-5-5", "hy4free", "hy3", "deepseek-v4-free", "hy4flash-exp4"]);
      await pbox.locator(".fbrow").nth(1).locator("button").click();
      assert.deepEqual(await names(), ["claude-opus-5-5", "hy4free", "hy3", "hy4flash-exp4"]);
      // free: with nothing after it is refused, the box keeping it
      await pbox.locator("input").fill("free:");
      await pbox.locator("button", { hasText: w.add }).click();
      assert.equal(await pbox.locator(".fbrow").count(), 1);
      assert.equal(await pbox.locator("input").inputValue(), "free:");
      await pbox.locator("input").fill("");

      const b = ed.locator("button.primary", { hasText: w.save });
      await b.scrollIntoViewIfNeeded();
      await b.click();
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      const sent = posts.find((p) => p.path === "/api/groups/save");
      assert.deepEqual(sent.body.members, ["an/claude-opus-5-5"], "only the model named is sent");
      assert.deepEqual(sent.body.match, ["free:wb/*"], "the rule as typed");

      const missing = await page.evaluate((k) => ["zh", "zh-TW", "ja", "de"].filter((l) => !I18N[l][k]), LINE);
      assert.deepEqual(missing, [], "every language has the line");
      assert.deepEqual(errors, []);
    });
  }
}
