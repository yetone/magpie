// Run with Node's test runner and Playwright on the module path; see README.md.
// Sessions in magpie's trash can be erased for good (#487): each trashed row
// has a Delete forever, and the Trash an Empty trash; both ask first in
// magpie's own dialog (never confirm()), Cancel sends nothing, and the
// confirm posts sessions/purge with the row's key, or all. The note under
// the Trash no longer says magpie never erases them; it says it never does
// by itself. A click leaves the page where it is. In English and Chinese,
// Chromium and WebKit; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const trashed = (id, title, min) => ({
  key: `claude/20261001-${id}`, agent: "claude", id, title, cwd: "/work/app", last: at(min + 60), deleted: at(min), size: 4096,
  items: [{ from: `~/.claude/projects/x/${id}.jsonl`, name: `0-${id}.jsonl` }], name: "Claude Code", icon: "claudecode-color",
});

function serve(lang, calls) {
  const store = { trash: [trashed("a", "fix the login form", 5), trashed("b", "write the post", 10), trashed("c", "", 20)] };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/sessions/manage") {
      return json({
        agents: [{ agent: "claude", count: 0, deletable: true, name: "Claude Code", icon: "claudecode-color" }],
        agent: "claude", sessions: [], terminal: false, trash: store.trash, trashDir: "~/Library/Application Support/magpie/trash/sessions",
      });
    }
    if (url.pathname === "/api/sessions/purge") {
      const body = route.request().postDataJSON();
      calls.push({ path: "purge", body });
      const keys = body.all ? store.trash.map((x) => x.key) : body.keys;
      store.trash = store.trash.filter((x) => !keys.includes(x.key));
      return json({ purged: keys, refused: [] });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: {
    nav: "Sessions", cancel: "Cancel", forever: "Delete forever", empty: "Empty trash", askOne: "Delete this session forever?", askAll: "Empty magpie's trash?",
    one: "Erased for good", all: "2 sessions erased for good", count: "3 sessions", trashEmpty: "Trash is empty",
    note: "Deleted sessions are kept in ~/Library/Application Support/magpie/trash/sessions until you erase them here; magpie never erases them by itself.",
  },
  zh: {
    nav: "会话", cancel: "取消", forever: "彻底删除", empty: "清空回收站", askOne: "彻底删除这个会话？", askAll: "清空 magpie 的回收站？",
    one: "已彻底删除", all: "已彻底删除 2 个会话", count: "3 个会话", trashEmpty: "回收站是空的",
    note: "删除的会话保存在 ~/Library/Application Support/magpie/trash/sessions，直到你在这里彻底删除；magpie 不会自行抹掉它们。",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: trashed sessions are erased for good only after magpie's own dialog`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 560 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
      const calls = [];
      await page.route("**/*", serve(lang, calls));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator("#nav").getByRole("button", { name: w.nav, exact: true }).click();
      const view = page.locator("#view-sessions");
      await view.locator(".sm-trash-btn").click();
      const rows = view.locator(".sm-trash .row");
      await rows.first().waitFor();
      assert.equal(await rows.count(), 3);
      assert.equal((await view.locator(".sm-note, .usage-note").last().textContent()).trim(), w.note);
      assert.equal((await view.locator(".sm-trash-bar .sm-count").textContent()).trim(), w.count);
      const said = (text) => page.waitForFunction((x) => document.querySelector("#status")?.textContent === x, text)
        .catch(async () => assert.equal(await page.locator("#status").textContent(), text));
      const top = (l) => l.evaluate((e) => e.getBoundingClientRect().top);

      // a row's Delete forever asks; Cancel sends nothing, and the page stays put
      const row = rows.filter({ hasText: "write the post" });
      const del = row.getByRole("button", { name: w.forever, exact: true });
      const before = await top(row);
      await del.click();
      const ask = page.locator("#modal .sm-ask");
      await ask.waitFor();
      assert.equal((await ask.locator(".ehead b").textContent()).trim(), w.askOne);
      assert.deepEqual(await ask.locator(".sm-ask-list li").allTextContents(), ["write the post"]);
      const border = await page.evaluate(() => [...document.querySelectorAll("#view-sessions, #view-sessions *, #modal .sm-ask, #modal .sm-ask *")]
        .filter((e) => parseFloat(getComputedStyle(e).borderLeftWidth) > 1 && getComputedStyle(e).borderLeftColor !== getComputedStyle(e).borderRightColor).map((e) => e.className));
      assert.deepEqual(border, [], "no left-border accent");
      await ask.getByRole("button", { name: w.cancel, exact: true }).click();
      await ask.waitFor({ state: "detached" });
      assert.equal(calls.length, 0, "Cancel erases nothing");
      assert.equal(await top(row), before, "the click moved the page");

      // confirmed: its key is purged, and it leaves the list
      await del.click();
      await ask.waitFor();
      await ask.getByRole("button", { name: w.forever, exact: true }).click();
      await ask.waitFor({ state: "detached" });
      assert.deepEqual(calls[0], { path: "purge", body: { keys: ["claude/20261001-b"] } });
      await said(w.one);
      await page.waitForFunction(() => document.querySelectorAll("#view-sessions .sm-trash .row").length === 2);

      // Empty trash asks, naming them, and posts all
      const empty = view.locator(".sm-trash-bar .sm-empty");
      assert.equal((await empty.textContent()).trim(), w.empty);
      await empty.click();
      await ask.waitFor();
      assert.equal((await ask.locator(".ehead b").textContent()).trim(), w.askAll);
      assert.deepEqual(await ask.locator(".sm-ask-list li").allTextContents(), ["fix the login form", "c"]);
      await ask.getByRole("button", { name: w.cancel, exact: true }).click();
      await ask.waitFor({ state: "detached" });
      assert.equal(calls.length, 1, "Cancel empties nothing");
      await empty.click();
      await ask.waitFor();
      await ask.getByRole("button", { name: w.empty, exact: true }).click();
      await ask.waitFor({ state: "detached" });
      assert.deepEqual(calls[1], { path: "purge", body: { all: true } });
      await said(w.all);
      await view.locator(".empty-state", { hasText: w.trashEmpty }).waitFor();
      assert.equal(await view.locator(".sm-empty").count(), 0, "nothing to empty");

      const missing = await page.evaluate(() => [
        "Empty trash", "Delete forever", "Empty magpie's trash?", "Delete this session forever?",
        "Every session in magpie's trash is erased for good: it can't be restored.", "Its files are erased for good: it can't be restored.",
        "Erased for good", "{n} sessions erased for good",
        "Deleted sessions are kept in {dir} until you erase them here; magpie never erases them by itself.",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });
  }
}
