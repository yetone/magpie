// Run with Node's test runner and Playwright on the module path; see README.md.
// Routing groups are removed several at once, and the found ones removed
// are no longer listed one by one (lc on Discord: "路由组可否关闭自动发现，
// 并且可编辑批量移除，已经移除的不要显示"). Select, by New group, puts a box
// on each card; a click on a card picks it, the bar's box picks all or
// none, and Remove asks in a dialog before it posts groups/delete
// {ids}. The removed found groups are one line under the list, how many,
// whose menu (the app's, not a native one) brings one back. No click moves
// the page. In English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/m", name: "m", providerName: "A", icon: "generic" },
  { id: "b/m", name: "m", providerName: "B", icon: "generic" },
  { id: "a/x", name: "x", providerName: "A", icon: "generic" },
];
const info = (ids) => ids.map((id) => ({ id, ready: true }));
const all = [
  { id: "auto-m", name: "Model M", members: ["a/m", "b/m"], auto: true, ready: true, memberInfo: info(["a/m", "b/m"]) },
  { id: "auto-n", name: "Model N", members: ["a/m", "b/m"], auto: true, ready: true, memberInfo: info(["a/m", "b/m"]) },
  { id: "mine", name: "Mine", members: ["a/x", "b/m"], ready: true, memberInfo: info(["a/x", "b/m"]) },
  { id: "auto-gone", hidden: true, members: [] },
  { id: "auto-old", hidden: true, members: [] },
];

const words = {
  en: { select: "Select", removed: "2 found groups removed", removed3: "3 found groups removed", picked: "2 selected", cancel: "Cancel", done: "2 groups removed", back: "auto-gone is back" },
  zh: { select: "选择", removed: "已移除 2 个自动创建的组", removed3: "已移除 3 个自动创建的组", picked: "已选 2 个", cancel: "取消", done: "已移除 2 个组", back: "auto-gone" },
};

function serve(lang, posts) {
  let groups = all.map((g) => ({ ...g }));
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    const st = () => ({ models, pools: [], deciders: [], found: true, groups });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(st());
    if (url.pathname === "/api/groups/delete" || url.pathname === "/api/groups/show") {
      const body = JSON.parse(r.request().postData() || "{}");
      posts.push([url.pathname.split("/").pop(), body]);
      if (body.ids) groups = groups.map((g) => body.ids.includes(g.id) ? (g.auto ? { id: g.id, hidden: true, members: [] } : null) : g).filter(Boolean);
      if (body.id) groups = groups.map((g) => g.id === body.id ? { id: g.id, name: "Back", members: ["a/m", "b/m"], auto: true, ready: true, memberInfo: info(["a/m", "b/m"]) } : g);
      return json(st());
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
    test(`${engine} ${lang}: groups removed several at once`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 900 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-group-bulk-remove.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=routing");
      const card = (name) => page.locator(".rt-groups .rt-group", { hasText: name });
      await card("Model M").waitFor();

      // the removed found groups: one line, not a chip each
      const line = page.locator(".rt-ghidden");
      assert.equal((await line.textContent()).trim(), w.removed);
      assert.equal(await line.locator("button").count(), 1);
      assert.equal(await page.locator(".rt-gsec").getByText("gone", { exact: true }).count(), 0, "a removed group isn't listed");
      assert.equal(await page.locator(".rt-gsec select").count(), 0);

      await page.locator(".rt-gsec").evaluate((x) => x.scrollIntoView({ block: "start" })); // by the test, as the reader would
      await page.waitForTimeout(200);
      const at = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => [e.id || e.className, e.scrollTop]).join(";"));
      const before = await at();

      await page.locator(".rt-gsec .row-head button", { hasText: w.select }).click();
      const bar = page.locator(".rt-gsel");
      await bar.waitFor();
      assert.equal(await page.locator(".rt-gpick").count(), 3, "every group listed has a box");
      assert.equal(await page.locator(".rt-groups .rt-ghandle").count(), 0);
      const rm = bar.locator(".rt-gremove");
      assert.equal(await rm.isDisabled(), true, "nothing picked, nothing to remove");

      await card("Model M").click();
      await card("Mine").locator("input").check();
      await page.waitForFunction((s) => document.querySelector(".rt-gsel .note").textContent === s, w.picked);
      assert.equal(await bar.locator("input").evaluate((x) => x.indeterminate), true);
      assert.equal(await page.locator(".rt-groups .rt-editor, .rt-groups .rt-gedit").count(), 0, "a click on a card doesn't open its editor");

      await rm.click();
      const confirm = page.getByRole("alertdialog");
      await confirm.waitFor();
      assert.match(await confirm.textContent(), /Model M/);
      assert.match(await confirm.textContent(), /Mine/);
      assert.deepEqual(posts, [], "one click removes nothing");
      await confirm.getByRole("button", { name: w.cancel, exact: true }).click();
      assert.deepEqual(posts, [], "Cancel keeps every group");
      assert.equal(await page.locator(".rt-gpick input:checked").count(), 2);
      await rm.click();
      await confirm.locator("button").last().click();
      await bar.waitFor({ state: "detached" });
      assert.deepEqual(posts, [["delete", { ids: ["auto-m", "mine"] }]]);
      await card("Model M").waitFor({ state: "detached" });
      assert.equal(await card("Mine").count(), 0);
      assert.equal(await card("Model N").count(), 1);
      await page.waitForFunction((m) => document.querySelector("#status").textContent.includes(m), w.done);
      assert.equal((await line.textContent()).trim(), w.removed3);

      // one brought back from the line's menu
      await line.locator("button").click();
      const menu = page.locator(".row-menu[role=menu]");
      await menu.waitFor();
      assert.deepEqual(await menu.locator(".rm-name").allTextContents(), ["m", "gone", "old"]);
      await menu.locator(".rm-item", { hasText: "gone" }).click();
      await card("Back").waitFor();
      assert.deepEqual(posts[1], ["show", { id: "auto-gone" }]);
      await page.waitForFunction((m) => document.querySelector("#status").textContent.includes(m), w.back);
      assert.equal(await at(), before, "no click moved the page");

      const missing = await page.evaluate(() => [
        "1 found group removed", "{n} found groups removed", "magpie doesn't make them again. Click to bring one back.",
        "Pick several groups to remove together", "Select every group", "Pick the groups to remove", "These routing groups will no longer be available to agents.", "{n} groups removed",
        "Select", "Done", "{n} selected", "Bring it back",
      ].filter((k) => !I18N.zh[k] || !I18N.ja[k] || !I18N.de[k]));
      assert.deepEqual(missing, [], "every string has its Chinese, Japanese and German");
      assert.deepEqual(errors, []);
    });
  }
}
