// Run with Node's test runner and Playwright on the module path; see README.md.
// ARNO on Discord: decision models (Jev) make a routing group of their own,
// asked at /v1/systemone (provider/decide_group.go). The group editor's
// Add a model picker offers a group of decision models only decision
// models, and a group of models that hold conversations only those and
// the groups it may hold, never a group of decision models; a decision
// model's row has no reasoning picker. In English and Chinese, Chromium
// and WebKit; no new strings.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/one", name: "one", providerName: "A", icon: "generic" },
  { id: "a/two", name: "two", providerName: "A", icon: "generic" },
];
const deciders = [
  { id: "ja/jev-latest", name: "jev-a", providerName: "JA", icon: "generic" },
  { id: "jb/jev-latest", name: "jev-b", providerName: "JB", icon: "generic" },
];
const words = {
  en: { edit: "Edit", add: "Add another model", own: "Group's reasoning" },
  zh: { edit: "编辑", add: "再添加一个模型" },
};

function serve(lang) {
  const groups = () => ({
    models, deciders, pools: [],
    groups: [
      { id: "g", name: "G", members: ["a/one"], off: [], routing: "order", ready: true, memberInfo: [{ id: "a/one", ready: true }] },
      { id: "other", name: "Other", members: ["a/two"], off: [], routing: "order", ready: true, memberInfo: [{ id: "a/two", ready: true }] },
      { id: "jevs", name: "Jevs", decides: true, members: ["ja/jev-latest"], off: [], routing: "order", ready: true, memberInfo: [{ id: "ja/jev-latest", ready: true }] },
    ],
  });
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
    if (url.pathname.startsWith("/api/groups/")) return json(groups());
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
    test(`${engine} ${lang}: a group of decision models is offered decision models, a chat group none`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 1400 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=groups");
      const card = (id) => page.locator(`.rt-group[data-id="${id}"]`);
      await card("g").waitFor();
      const pop = page.locator("#pop");
      const offered = async (id) => {
        await card(id).locator("button", { hasText: w.edit }).click();
        const ed = page.locator(".rt-gedit");
        await ed.locator("button.rt-gadd", { hasText: w.add }).click();
        await pop.locator("li .v").first().waitFor();
        const names = await pop.locator("li .v").evaluateAll((vs) => vs.map((v) => v.textContent.trim()));
        await page.keyboard.press("Escape");
        await pop.waitFor({ state: "hidden" });
        return { ed, names };
      };

      const jevs = await offered("jevs");
      assert.deepEqual(jevs.names.sort(), ["jev-a", "jev-b"], "a group of decision models offered more than decision models");
      const row = jevs.ed.locator(".fbl").first().locator(".fbrow").first();
      assert.equal(await row.locator(".rt-fixed").count(), 0, "a decision model has a reasoning picker");
      assert.equal(await row.evaluate((r) => r.classList.contains("off")), false, "a decision model's row says no provider serves it");
      await page.reload();
      await card("g").waitFor();

      const chat = await offered("g");
      assert.ok(chat.names.includes("one") && chat.names.includes("two") && chat.names.includes("Other"), `chat group offered ${chat.names}`);
      assert.ok(!chat.names.some((n) => n.startsWith("jev") || n === "Jevs"), `a chat group offered a decision model or group: ${chat.names}`);
      if (w.own) assert.equal(await chat.ed.locator(".fbl").first().locator(".fbrow .rt-fixed", { hasText: w.own }).count(), 1);

      assert.equal(await page.locator("select").count(), 0, "a native select");
      assert.deepEqual(errors, []);
    });
  }
}
