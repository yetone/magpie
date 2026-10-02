// Run with Node's test runner and Playwright on the module path; see README.md.
// Every session of project folders deleted at once (#527: 有些codex会话多，
// 有些特别老的项目，希望能直接删除整个项目文件夹下所有会话; then mintonight: 在项目
// 文件夹那一行的字首加一个复选框…多选同时删除多个项目…复用已选 x 项那一行右侧的
// 删除按钮，这个数量 x 的计数逻辑还是按会话数量来). Each folder's row on the
// Sessions page starts with a box, as each session's does: ticked, it picks
// every session of the folder shown, folded or not, the bar counting
// sessions, not folders; a filter picks the ones it shows. Several folders
// are picked together, and the bar's Delete asks in magpie's own dialog
// (never confirm()), naming the folder and its path when the pick is one
// whole folder, and how many folders when it is several, then posts
// sessions/delete with their ids, so they go to magpie's trash as one
// deleted alone does. Cancel posts nothing; a session still being written
// to is left and said so; other folders stay. An agent magpie can't delete
// from has no boxes. Clicks leave the page where it is. In English and
// Chinese, Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const sess = (id, cwd, title, min) => ({
  agent: "codex", id, cwd, title, start: at(min + 5), last: at(min), models: [],
  resume: `cd ${cwd} && codex resume ${id}`, path: `~/.codex/sessions/2026/10/01/rollout-${id}.jsonl`, size: 4096, messages: 6, files: 1, deletable: true,
});

function fixture() {
  const app = [sess("a-run", "/work/app", "still running", 0.2), sess("a-login", "/work/app", "fix the login form", 30)];
  for (let i = 0; i < 12; i++) app.push(sess(`a-${i}`, "/work/app", `older task ${i}`, 60 + i * 10));
  return {
    codex: [...app, sess("b-1", "/work/old-blog", "write the post", 600), sess("b-2", "/work/old-blog", "", 700), sess("n-1", "", "no folder chat", 800)],
    opencode: [{ ...sess("o-1", "/work/app", "an opencode chat", 5), agent: "opencode", deletable: false, resume: "opencode -s o-1" }],
    trash: [],
  };
}

function serve(lang, calls) {
  const store = fixture();
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/sessions/manage") {
      const agent = url.searchParams.get("agent") === "opencode" ? "opencode" : "codex";
      return json({
        agents: [
          { agent: "codex", count: store.codex.length, deletable: true, name: "Codex", icon: "codex" },
          { agent: "opencode", count: store.opencode.length, deletable: false, name: "OpenCode", icon: "opencode" },
        ],
        agent, sessions: store[agent], terminal: false, trash: store.trash, trashDir: "~/Library/Application Support/magpie/trash/sessions",
      });
    }
    if (url.pathname === "/api/sessions/delete") {
      const body = req.postDataJSON();
      calls.push({ path: "delete", body });
      const out = { deleted: [], refused: [] };
      for (const id of body.ids) {
        if (id === "a-run") { out.refused.push({ id, error: "it was written to in the last minute; it may still be running", active: true }); continue; }
        const s = store.codex.find((x) => x.id === id);
        store.codex = store.codex.filter((x) => x.id !== id);
        store.trash.push({ key: `codex/20261001-${id}`, agent: "codex", id, title: s.title, cwd: s.cwd, last: s.last, deleted: at(0), size: s.size,
          items: [{ from: s.path, name: "0-" + id + ".jsonl" }], name: "Codex", icon: "codex" });
        out.deleted.push(id);
      }
      return json(out);
    }
    if (url.pathname.startsWith("/api/")) {
      if (req.method() === "POST") calls.push({ path: url.pathname });
      return json({});
    }
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: {
    nav: "Sessions", del: "Delete", cancel: "Cancel", label: "Select every session in old-blog", picked: (n) => `${n} selected`,
    askApp: "Delete all 14 sessions in app?", askBlog: "Delete all 2 sessions in old-blog?", askTwo: "Delete all 3 sessions in 2 folders?", askSome: "Delete 2 sessions?",
    moved3: "3 sessions moved to magpie's trash",
    active: "still running is still being written to; close it in Codex and try again in a minute",
  },
  zh: {
    nav: "会话", del: "删除", cancel: "取消", label: "选中 old-blog 下的全部会话", picked: (n) => `已选 ${n} 个`,
    askApp: "删除 app 下的全部 14 个会话？", askBlog: "删除 old-blog 下的全部 2 个会话？", askTwo: "删除 2 个文件夹下的全部 3 个会话？", askSome: "删除这 2 个会话？",
    moved3: "3 个会话已移到 magpie 的回收站",
    active: "「still running」仍在写入；请在 Codex 中关闭它，一分钟后再试",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: folders are picked with their box and deleted together, after magpie's own dialog`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 560 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-sessions-folder-delete.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
      const calls = [];
      await page.route("**/*", serve(lang, calls));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator("#nav").getByRole("button", { name: w.nav, exact: true }).click();
      const view = page.locator("#view-sessions");
      const row = (id) => view.locator(`.row.sm-sess[data-id="${id}"]`);
      await row("a-run").waitFor();
      const folder = (name) => view.locator(".row.sm-folder").filter({ has: page.locator(".fold .name", { hasText: new RegExp("^" + name + "$") }) });
      const box = (name) => folder(name).locator("input.sm-folder-check");
      const said = (text) => page.waitForFunction((x) => document.querySelector("#status")?.textContent === x, text)
        .catch(async () => assert.equal(await page.locator("#status").textContent(), text));
      const top = (l) => l.evaluate((e) => e.getBoundingClientRect().top);
      const count = async () => (await view.locator(".sm-bar .sm-count").textContent()).trim();
      const nofolder = lang === "zh" ? "无文件夹" : "No folder";
      // the reader's wheel brings it into sight (a scroll by code is put back)
      const reach = async (l) => {
        const b0 = await view.boundingBox();
        await page.mouse.move(b0.x + b0.width / 2, b0.y + b0.height / 2);
        for (let k = 0; k < 80; k++) {
          const b = await l.boundingBox();
          if (b.y >= b0.y + 40 && b.y + b.height <= b0.y + b0.height - 40) break;
          await page.mouse.wheel(0, b.y < b0.y + 40 ? -40 : 40);
          await page.waitForTimeout(20);
        }
        await page.waitForTimeout(300);
      };
      // a folder's box ticked: the page held still, the folder left folded
      // or open as it was
      const tick = async (name) => {
        const r = folder(name), c = box(name);
        await reach(c);
        const before = await top(r), open = await r.locator(".fold").getAttribute("aria-expanded");
        await c.click();
        await page.waitForTimeout(50);
        assert.equal(await top(folder(name)), before, "the box moved the page");
        assert.equal(await folder(name).locator(".fold").getAttribute("aria-expanded"), open, "the box folded or unfolded the folder");
      };
      const ask = page.locator("#modal .sm-ask");
      const askDel = async () => {
        const d = view.locator(".sm-bar .sm-delete");
        await reach(d);
        await d.click();
        await ask.waitFor();
      };

      // every folder starts with its box, before its name, inside its row
      const boxes = view.locator(".row.sm-folder input.sm-folder-check");
      assert.equal(await boxes.count(), 3);
      for (const b of await boxes.all()) {
        const first = await b.evaluate((e) => e.parentElement.firstElementChild === e && e.getBoundingClientRect().right <= e.parentElement.querySelector(".fold").getBoundingClientRect().left);
        assert(first, "the box comes first in the row");
      }
      assert.equal(await box("old-blog").getAttribute("aria-label"), w.label);
      assert.equal(await view.locator(".sm-folder-del").count(), 0, "no Delete all beside the box");

      // a folded folder ticked picks its two, the bar counting sessions;
      // its own Delete asks naming the folder; Cancel posts nothing
      assert.equal(await folder("old-blog").locator(".fold").getAttribute("aria-expanded"), "false");
      await tick("old-blog");
      assert.equal(await count(), w.picked(2));
      assert(await box("old-blog").isChecked());
      await askDel();
      assert.equal((await ask.locator(".ehead b").textContent()).trim(), w.askBlog);
      assert.equal((await ask.locator(".sm-ask-path").textContent()).trim(), "/work/old-blog");
      assert.deepEqual(await ask.locator(".sm-ask-list li").allTextContents(), ["write the post", "b-2"]);
      assert((await ask.locator(".lib-confirm").textContent()).includes("~/Library/Application Support/magpie/trash/sessions"), "they go to magpie's trash");
      await ask.getByRole("button", { name: w.cancel, exact: true }).click();
      await ask.waitFor({ state: "detached" });
      assert.deepEqual(calls, [], "Cancel deletes nothing");
      assert.equal(await count(), w.picked(2), "the pick is kept");

      // a second folder: three sessions in two folders; one of them left
      // out and one of app's in, it is two picked sessions, not whole folders
      await tick(nofolder);
      assert.equal(await count(), w.picked(3));
      const login = row("a-login").locator("input.sm-check");
      await reach(login);
      await login.check();
      assert.equal(await count(), w.picked(4));
      assert.equal(await box("app").evaluate((e) => e.indeterminate), true, "app's box is part-ticked");
      await login.uncheck();
      assert.equal(await box("app").evaluate((e) => e.indeterminate), false);
      await reach(row("a-run"));
      await row("a-run").locator("input.sm-check").check();
      await tick("old-blog");
      await askDel();
      assert.equal((await ask.locator(".ehead b").textContent()).trim(), w.askSome);
      await ask.getByRole("button", { name: w.cancel, exact: true }).click();
      await ask.waitFor({ state: "detached" });
      await reach(row("a-run"));
      await row("a-run").locator("input.sm-check").uncheck();
      await tick("old-blog");
      await page.waitForTimeout(400);
      if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-sessions-folder-picked.png`) });
      await askDel();
      assert.equal((await ask.locator(".ehead b").textContent()).trim(), w.askTwo);
      await ask.getByRole("button", { name: w.del, exact: true }).click();
      await ask.waitFor({ state: "detached" });
      assert.equal(calls.length, 1);
      assert.deepEqual(calls[0].body.ids.slice().sort(), ["b-1", "b-2", "n-1"]);
      await folder("old-blog").waitFor({ state: "detached" });
      await said(w.moved3);
      assert.equal(await folder(nofolder).count(), 0);
      assert.equal(await folder("app").count(), 1);
      assert.equal(await view.locator(".sm-trash-btn em").textContent(), "3");

      // under a filter the box picks the sessions it shows
      await view.locator(".sm-filter").fill("older task 1");
      await page.waitForFunction(() => document.querySelectorAll("#view-sessions .row.sm-sess").length === 3);
      await tick("app");
      assert.equal(await count(), w.picked(3));
      await tick("app");
      assert.equal(await count(), lang === "zh" ? "3 个会话" : "3 sessions");
      await view.locator(".sm-filter").fill("");
      await view.locator(".sm-filter").dispatchEvent("input");
      await row("a-run").waitFor();

      // app whole: every one posted; the one still running is left, and said so
      await tick("app");
      assert.equal(await count(), w.picked(14));
      await askDel();
      assert.equal((await ask.locator(".ehead b").textContent()).trim(), w.askApp);
      await ask.getByRole("button", { name: w.del, exact: true }).click();
      await ask.waitFor({ state: "detached" });
      assert.deepEqual(calls[1].body.ids, ["a-run", "a-login", ...Array.from({ length: 12 }, (_, i) => `a-${i}`)]);
      await said(w.active);
      await view.locator(".row.sm-sess").first().waitFor();
      assert.deepEqual(await view.locator(".row.sm-sess").evaluateAll((rs) => rs.map((r) => r.dataset.id)), ["a-run"]);

      // no left-border accent in the page or the dialog
      const border = await page.evaluate(() => [...document.querySelectorAll("#view-sessions, #view-sessions *")]
        .filter((e) => parseFloat(getComputedStyle(e).borderLeftWidth) > 1 && getComputedStyle(e).borderLeftColor !== getComputedStyle(e).borderRightColor).map((e) => e.className));
      assert.deepEqual(border, [], "no left-border accent");

      // an agent magpie can't delete from has none
      await view.locator(".sm-agents .opt", { hasText: "OpenCode" }).click();
      await view.locator('.row.sm-sess[data-id="o-1"]').waitFor();
      assert.equal(await view.locator(".sm-folder-check").count(), 0);

      const missing = await page.evaluate(() => ["Select every session in {folder}", "Delete all {n} sessions in {k} folders?", "Delete all {n} sessions in {folder}?"].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });
  }
}
