// Run with Node's test runner and Playwright on the module path; see README.md.
// jimlee0824v2 on X: a routing group's right-click menu has Remove, last and
// set apart, which asks what the card's own Remove asks and then posts
// groups/delete; cancelled, nothing is posted and the row stays. Chromium
// and WebKit, every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/m", name: "m", providerName: "A", icon: "generic" },
  { id: "a/x", name: "x", providerName: "A", icon: "generic" },
];
const info = (ids) => ids.map((id) => ({ id, ready: true }));
const group = (id, name, members, more = {}) => ({ id, name, members, ready: true, memberInfo: info(members), ...more });


const words = { en: ["Remove", "Cancel"], zh: ["移除", "取消"], "zh-TW": ["移除", "取消"], ja: ["削除", "キャンセル"], de: ["Entfernen", "Abbrechen"] };
function serve(lang, posts) {
  let list = [
    group("one", "One", ["a/x", "a/m"], { routing: "order", rules: [{ use: "a/m", agents: ["codex"] }] }),
    group("two", "Two", ["a/m"]),
  ];
  const groups = () => ({ models, pools: [], deciders: [], found: true, groups: list });
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
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
    if (url.pathname === "/api/groups/delete") {
      const body = JSON.parse(r.request().postData() || "{}");
      posts.push(["delete", body]);
      list = list.filter((g) => g.id !== body.id);
      return json(groups());
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}


const listed = (page) => page.locator(".rt-groups > .rt-group").evaluateAll((rs) => rs.map((r) => r.dataset.id));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const [remove, cancel] = words[lang];
    test(`${engine} ${lang}: a group's right-click menu removes it`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 420, height: 1200 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=groups");
      await page.locator(".rt-groups > .rt-group").nth(1).waitFor();
      await page.waitForTimeout(500);
      const open = async () => {
        await page.locator(".rt-group[data-id=two] .main").click({ button: "right" });
        const menu = page.locator(".row-menu");
        await menu.waitFor();
        const names = await menu.locator(".rm-item").allTextContents();
        assert.equal(names[names.length - 1].trim(), remove, "Remove is last");
        await menu.getByRole("menuitem", { name: remove, exact: true }).click();
        const confirm = page.getByRole("alertdialog");
        await confirm.waitFor();
        return confirm;
      };
      let confirm = await open();
      await confirm.getByRole("button", { name: cancel, exact: true }).click();
      await page.getByRole("alertdialog").waitFor({ state: "detached" }).catch(() => {});
      assert.deepEqual(posts, [], "cancelled, nothing posted");
      assert.deepEqual(await listed(page), ["one", "two"]);
      confirm = await open();
      await confirm.locator("button").last().click();
      await page.waitForFunction(() => !document.querySelector(".rt-group[data-id=two]"));
      assert.deepEqual(posts, [["delete", { id: "two" }]]);
      assert.deepEqual(await listed(page), ["one"]);
      assert.deepEqual(errors, []);
    });
  }
}
