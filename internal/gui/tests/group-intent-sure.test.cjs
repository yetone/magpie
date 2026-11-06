// Run with Node's test runner and Playwright on the module path; see README.md.
// A group may ask Jev to be surer, or less sure, of an intent than 0.4
// before a rule follows it (#5101): `magpie group set <id> intent-sure=0.25`.
// The Routing page has no field for it yet, so every save it makes of the
// group carries the value back as it was: the editor's Save and a pick in
// the card's routing menu. A group without one saves none. In English and
// Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "oa/gpt-6.1-sol", name: "gpt-6.1-sol", providerName: "OpenAI", icon: "generic" },
  { id: "rl/glm-5", name: "glm-5", providerName: "Relay", icon: "generic" },
];
const members = ["oa/gpt-6.1-sol", "rl/glm-5"];
const group = (id, name, extra) => ({ id, name, members, routing: "order", ready: true, classifier: "jev",
  rules: [{ intent: "code", to: "oa/gpt-6.1-sol" }], memberInfo: members.map((m) => ({ id: m, ready: true })), ...extra });
const groups = () => ({ models, pools: [], groups: [group("sure", "Sure", { intentSure: 0.25 }), group("plain", "Plain", {})] });

const words = { en: { edit: "Edit", save: "Save" }, zh: { edit: "编辑", save: "保存" } };

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
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    test(`${engine} ${lang}: the Routing page keeps a group's intent sureness`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 2000 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=groups");
      const last = () => posts.filter((p) => p.path === "/api/groups/save").at(-1)?.body;

      const save = async (id) => {
        const card = page.locator(`.rt-group[data-id="${id}"]`);
        await card.locator("button", { hasText: w.edit }).click();
        const ed = page.locator(".rt-gedit");
        await ed.locator("button.primary", { hasText: w.save }).click();
        await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
        return last();
      };
      const pick = async (id) => {
        const n = posts.length;
        await page.locator(`.rt-group[data-id="${id}"] button.rt-groute`).click();
        const menu = page.locator(".proto-menu");
        await menu.waitFor();
        await menu.locator('.pm-item[aria-checked="false"]').first().click();
        for (let i = 0; i < 100 && posts.length === n; i++) await page.waitForTimeout(20); // the post is the state waited for
        return last();
      };

      await page.locator('.rt-group[data-id="sure"]').waitFor();
      assert.equal((await save("sure")).intentSure, 0.25, "the editor's Save keeps it");
      assert.equal((await save("plain")).intentSure ?? null, null, "a group without one saves none");
      const picked = await pick("sure");
      assert.notEqual(picked.routing, "order", "the menu's pick was saved");
      assert.equal(picked.intentSure, 0.25, "a routing pick keeps it");
      assert.deepEqual(errors, []);
    });
  }
}
