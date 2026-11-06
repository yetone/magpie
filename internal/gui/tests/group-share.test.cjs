// Run with Node's test runner and Playwright on the module path; see README.md.
// laincatx on X: a routing group is shared as text. Its row's menu has
// Copy to share, which asks groups/share for the group's text and copies
// it; Import by New group opens a box the text is pasted into, which posts
// groups/import and says what was added, what of it the user has none of,
// what came from their own provider instead and how many rules went with
// them, until Done. The text typed stays in the box as the list is drawn
// again. A text that is no shared group is refused in the reader's
// language. No click moves the page. At 420px, in every language,
// Chromium and WebKit.
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

const words = {
  en: { share: "Copy to share", copied: "One copied", imp: "Import", added: "Added One 2", missing: "Not in your magpie, left out: gone/y", instead: "The same model from your own provider: relay/x → a/x", rules: "2 rules left out with them", bad: "This is not a routing group shared from magpie", done: "Done" },
  zh: { share: "分享为文字", copied: "已复制 One", imp: "导入", added: "已添加 One 2", missing: "你的 magpie 里没有，已略过：gone/y", instead: "换成你自己服务商的同一模型：relay/x → a/x", rules: "2 条规则随之略过", bad: "这不是从 magpie 分享的路由组", done: "完成" },
  "zh-TW": { share: "分享為文字", copied: "已複製 One", imp: "匯入", added: "已新增 One 2", missing: "你的 magpie 裡沒有，已略過：gone/y", instead: "換成你自己服務商的同一模型：relay/x → a/x", rules: "2 條規則隨之略過", bad: "這不是從 magpie 分享的路由組", done: "完成" },
  ja: { share: "共有用にコピー", copied: "One をコピーしました", imp: "インポート", added: "One 2", missing: "この magpie にないため除外：gone/y", instead: "自分のプロバイダーの同じモデルを使用：relay/x → a/x", rules: "2 件のルールも一緒に除外", bad: "magpie から共有されたルーティンググループではありません", done: "完了" },
  de: { share: "Zum Teilen kopieren", copied: "One kopiert", imp: "Importieren", added: "One 2", missing: "Nicht in deinem magpie, weggelassen: gone/y", instead: "Dasselbe Modell von deinem eigenen Anbieter: relay/x → a/x", rules: "2 Regeln mit ihnen weggelassen", bad: "Das ist keine aus magpie geteilte Routing-Gruppe", done: "Fertig" },
};
const KEYS = ["Copy to share", "{name} as text to paste into another magpie: its models, routing and rules, no key",
  "{name} copied: paste it into Import on another magpie's Routing page", "Add a routing group someone shared from their magpie",
  "Paste a group copied with Copy to share", "A routing group shared from magpie",
  "Paste a routing group someone shared from their magpie: its models, routing and rules come with it, never a key",
  "Not in your magpie, left out: {models}", "The same model from your own provider: {models}", "1 rule left out with them",
  "{n} rules left out with them", "This group was shared from a newer magpie (format {n}): update magpie to import it",
  "This shared group says no format it is in: copy it again with Copy to share", "This shared group can't be read: copy it again with Copy to share",
  "None of the group's models is in your magpie: {models}",
  "This is not a routing group shared from magpie: copy one with Copy to share in a group's menu", "Import", "Added {name}", "Done", "Cancel"];
const SHARED = '{"magpie":"routing-group","version":1,"group":{"name":"One","members":["a/x","a/m"]}}';

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
    if (url.pathname === "/api/groups/share") {
      posts.push(["share", JSON.parse(r.request().postData() || "{}")]);
      return json({ ...groups(), shared: SHARED });
    }
    if (url.pathname === "/api/copy") {
      posts.push(["copy", JSON.parse(r.request().postData() || "{}").text]);
      return json({});
    }
    if (url.pathname === "/api/groups/import") {
      const { text } = JSON.parse(r.request().postData() || "{}");
      posts.push(["import", text]);
      if (!text.includes("routing-group")) return r.fulfill({ status: 400, json: { error: "this is not a routing group shared from magpie", code: "share", share: "not_shared", format: "0", models: "" } });
      list = [...list, group("one-2", "One 2", ["a/x"])];
      return json({ ...groups(), imported: { id: "one-2", name: "One 2", missing: ["gone/y"], instead: ["relay/x → a/x"], rules: 2 } });
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
    const w = words[lang];
    test(`${engine} ${lang}: a routing group is copied to share and imported`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 420, height: 1800 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-group-share.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      // WebKit reports a ResizeObserver round left to the next frame as a
      // page error; see group-duplicate.test.cjs
      page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=groups");
      await page.locator(".rt-groups > .rt-group").nth(1).waitFor();
      // tall enough that the import box is in view unscrolled in every
      // language: a scroll closes the menu, and the page settles its own
      // scroll as it loads
      await page.waitForTimeout(500);
      const at = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => [e.id || e.className, e.scrollTop]).join(";") + "|" + scrollX);
      const before = await at();
      const status = (m) => page.waitForFunction((m) => document.querySelector("#status").textContent.includes(m), m);

      // Copy to share: the group's text, copied
      await page.locator(".rt-group[data-id=one] .main").click({ button: "right" });
      const menu = page.locator(".row-menu");
      await menu.waitFor();
      const box = await menu.boundingBox();
      assert(box.x >= 0 && box.x + box.width <= 420, `menu off the window: ${JSON.stringify(box)}`);
      await menu.getByRole("menuitem", { name: w.share, exact: true }).click();
      await status(w.copied);
      assert.deepEqual(posts, [["share", { id: "one" }], ["copy", SHARED]]);
      assert.equal(await page.locator(".rt-gedit").count(), 0, "sharing opened the editor");
      posts.length = 0;

      // Import: a box the text is pasted into, kept as the list is redrawn
      const imp = page.locator(".rt-gimportbtn");
      assert.equal((await imp.textContent()).trim(), w.imp);
      await imp.click();
      const text = page.locator(".rt-gimport-text");
      await text.waitFor();
      const ib = await page.locator(".rt-gimport").boundingBox();
      assert(ib.x >= 0 && ib.x + ib.width <= 420, `import box off the window: ${JSON.stringify(ib)}`);
      await text.fill("a group, honest");
      await page.locator(".rt-gfilter").fill("o");
      await page.locator(".rt-gfilter").fill("");
      assert.equal(await text.inputValue(), "a group, honest", "the redraw lost the text");
      await page.locator(".rt-gimport-go").click();
      await status(w.bad);
      assert.equal(await page.locator(".rt-gimport-text").count(), 1, "a refused text closed the box");

      await text.fill("```\n" + SHARED + "\n```");
      await page.locator(".rt-gimport-go").click();
      await page.locator(".rt-gimport-done").waitFor();
      assert((await page.locator(".rt-gimport-done").textContent()).includes(w.added));
      assert.equal(await page.locator(".rt-gimport-missing").textContent(), w.missing);
      const notes = await page.locator(".rt-gimport-note").allTextContents();
      assert.deepEqual(notes, [w.instead, w.rules]);
      assert.deepEqual(await listed(page), ["one", "two", "one-2"]);
      assert.equal(posts[1][1], "```\n" + SHARED + "\n```");
      await page.locator(".rt-gimport").getByRole("button", { name: w.done, exact: true }).click();
      assert.equal(await page.locator(".rt-gimport").count(), 0, "Done left the box");

      assert.equal(await at(), before, "a click moved the page");
      if (lang !== "en") {
        const missing = await page.evaluate(([l, ks]) => ks.filter((k) => !I18N[l][k]), [lang, KEYS]);
        assert.deepEqual(missing, [], `every string has its ${lang}`);
      }
      assert.deepEqual(errors, []);
    });
  }
}
