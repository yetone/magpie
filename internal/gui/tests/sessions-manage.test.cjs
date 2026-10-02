// Run with Node's test runner and Playwright on the module path; see README.md.
// The Sessions page (TJHHHH on Discord: manage sessions as CC Switch does,
// above all delete them, in the GUI): an agent's sessions by the folder they
// ran in, each with its resume command; picked ones are deleted after
// magpie's own dialog (never confirm()), which posts sessions/delete with
// their ids; one still being written to is said so; the Trash lists what
// was deleted and Restore posts its key. An agent whose sessions magpie
// can't delete shows no delete at all. Folding, picking and opening a row
// leave the page where it is. In English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const sess = (id, cwd, title, min, extra) => ({
  agent: "claude", id, cwd, title, start: at(min + 5), last: at(min), models: [],
  resume: `cd ${cwd} && claude --resume ${id}`, path: `~/.claude/projects/x/${id}.jsonl`, size: 4096, messages: 6, files: 1, deletable: true, ...extra,
});

function fixture() {
  const app = [sess("a-run", "/work/app", "still running", 0.2), sess("a-old", "/work/app", "fix the login form", 30, { files: 3 })];
  for (let i = 0; i < 14; i++) app.push(sess(`a-${i}`, "/work/app", `older task ${i}`, 60 + i * 10));
  return {
    claude: [...app, sess("b-1", "/work/blog", "write the post", 120), sess("b-2", "/work/blog", "", 240)],
    opencode: [{ ...sess("o-1", "/work/app", "an opencode chat", 5), agent: "opencode", deletable: false, resume: "opencode -s o-1" }],
    hermes: [{ ...sess("h-1", "/work/hermes", "Hermes chat", 8), agent: "hermes", deletable: false, read_only: true, resume: "" }],
    trash: [],
  };
}

function serve(lang, calls) {
  const store = fixture();
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/sessions/manage") {
      const want = url.searchParams.get("agent");
      const agent = ["opencode", "hermes"].includes(want) ? want : "claude";
      return json({
        agents: [
          { agent: "claude", count: store.claude.length, deletable: true, name: "Claude Code", icon: "claudecode-color" },
          { agent: "opencode", count: store.opencode.length, deletable: false, name: "OpenCode", icon: "opencode" },
          { agent: "hermes", count: store.hermes.length, deletable: false, name: "Hermes", icon: "hermes" },
        ],
        agent, sessions: store[agent], terminal: true, trash: store.trash, trashDir: "~/Library/Application Support/magpie/trash/sessions",
      });
    }
    if (url.pathname === "/api/sessions/delete") {
      const body = route.request().postDataJSON();
      calls.push({ path: "delete", body });
      const out = { deleted: [], refused: [] };
      for (const id of body.ids) {
        if (id === "a-run") { out.refused.push({ id, error: "it was written to in the last minute; it may still be running", active: true }); continue; }
        const s = store.claude.find((x) => x.id === id);
        store.claude = store.claude.filter((x) => x.id !== id);
        store.trash.push({ key: `claude/20261001-${id}`, agent: "claude", id, title: s.title, cwd: s.cwd, last: s.last, deleted: at(0), size: s.size,
          items: [{ from: s.path, name: "0-" + id + ".jsonl" }], name: "Claude Code", icon: "claudecode-color" });
        out.deleted.push(id);
      }
      return json(out);
    }
    if (url.pathname === "/api/sessions/restore") {
      const body = route.request().postDataJSON();
      calls.push({ path: "restore", body });
      const x = store.trash.find((y) => y.key === body.key);
      store.trash = store.trash.filter((y) => y !== x);
      store.claude.push(sess(x.id, x.cwd, x.title, 30));
      return json({ agent: "claude", id: x.id, title: x.title });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: {
    nav: "Sessions", del: "Delete", cancel: "Cancel", trash: "Trash", restore: "Restore", resume: "Resume",
    askOne: "Delete this session?", askTwo: "Delete 2 sessions?", moved: "2 sessions moved to magpie's trash",
    active: "still running is still being written to; close it in Claude Code and try again in a minute",
    restored: "fix the login form restored", picked: "2 selected", filter: "Filter sessions",
    cant: "magpie can list OpenCode's sessions and resume them, but not delete them: they aren't kept as files of their own.",
    hermesNote: "These Hermes sessions are read only; magpie can list them, but cannot resume or delete them.",
    codexNote: "Some Codex sessions are read only and cannot be deleted.",
    idLine: "Session ID",
  },
  zh: {
    nav: "会话", del: "删除", cancel: "取消", trash: "回收站", restore: "恢复", resume: "继续",
    askOne: "删除这个会话？", askTwo: "删除这 2 个会话？", moved: "2 个会话已移到 magpie 的回收站",
    active: "「still running」仍在写入；请在 Claude Code 中关闭它，一分钟后再试",
    restored: "已恢复「fix the login form」", picked: "已选 2 个", filter: "筛选会话",
    cant: "magpie 可以列出并继续 OpenCode 的会话，但不能删除：它们没有各自独立的文件。",
    hermesNote: "这些 Hermes 会话为只读；magpie 可以列出，但不能继续或删除。",
    codexNote: "部分 Codex 会话为只读，无法删除。",
    idLine: "会话 ID",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: sessions are listed by folder, deleted to magpie's trash and restored`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 560 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-sessions-manage.png`) });
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
      const top = (l) => l.evaluate((e) => e.getBoundingClientRect().top);
      const said = (text) => page.waitForFunction((x) => document.querySelector("#status")?.textContent === x, text)
        .catch(async () => assert.equal(await page.locator("#status").textContent(), text));
      // the reader's wheel brings it into sight (a scroll by code is put back)
      const reach = async (l) => {
        const box = await view.boundingBox();
        await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
        for (let k = 0; k < 80; k++) {
          const b = await l.boundingBox();
          if (b.y >= box.y + 40 && b.y + b.height <= box.y + box.height - 40) break;
          await page.mouse.wheel(0, b.y < box.y + 40 ? -40 : 40);
          await page.waitForTimeout(20);
        }
        await page.waitForTimeout(300);
      };
      const still = async (l, what, click) => {
        await reach(l);
        const before = await top(l);
        await click();
        await page.waitForTimeout(250);
        assert.equal(await top(l), before, `${what} moved the page`);
      };

      // the tree: by folder, the latest one open, the other folded
      const folders = view.locator(".row.sm-folder");
      assert.deepEqual(await folders.locator(".fold .name").allTextContents(), ["app", "blog"]);
      assert.equal(await folders.nth(0).locator(".fold").getAttribute("aria-expanded"), "true");
      assert.equal(await folders.nth(1).locator(".fold").getAttribute("aria-expanded"), "false");
      assert.equal(await row("b-1").count(), 0, "a folded folder's sessions aren't shown");
      assert.equal(await view.locator(".sm-filter").getAttribute("placeholder"), w.filter);
      assert(await row("a-old").locator(".sess-term").count() === 1, "open in terminal where there is one");
      assert.equal((await row("a-old").locator(".sess-resume").textContent()).trim(), w.resume);

      // unfolding a folder and opening a row's details leave the page alone
      await still(folders.nth(1), "unfolding", () => folders.nth(1).locator(".fold").click());
      await row("b-1").waitFor();
      await still(row("a-9"), "opening a row", () => row("a-9").locator(".who").click());
      assert(await view.locator(".sess-detail").isVisible(), "the row opened to its details");
      assert((await view.locator(".sess-detail").textContent()).includes("claude --resume a-9"));
      // the id's line is named for it (#465: it read 整个会话, the routing option's word)
      assert((await view.locator(".sess-detail .sess-line .k").allInnerTexts()).includes(w.idLine));

      // picking: two rows, the page held still
      await still(row("a-old"), "picking", () => row("a-old").locator(".sm-check").click());
      await reach(row("b-1"));
      await row("b-1").locator(".sm-check").click();
      assert.equal((await view.locator(".sm-count").textContent()).trim(), w.picked);
      assert.equal(await view.locator(".sess-detail").count(), 1, "a pick doesn't open the row");

      // the delete asks in magpie's dialog; Cancel sends nothing
      const del = view.locator(".sm-bar .sm-delete");
      await reach(del);
      await del.click();
      const ask = page.locator("#modal .sm-ask");
      await ask.waitFor();
      assert.equal((await ask.locator(".ehead b").textContent()).trim(), w.askTwo);
      assert.deepEqual(await ask.locator(".sm-ask-list li").allTextContents(), ["fix the login form", "write the post"]);
      assert.deepEqual(await ask.locator(".sm-ask-list li").evaluateAll((ls) => ls.filter((l) => l.scrollWidth > l.clientWidth).map((l) => l.textContent)), [], "the titles are shown whole");
      assert((await ask.locator(".lib-confirm").textContent()).includes("~/Library/Application Support/magpie/trash/sessions"));
      const border = await page.evaluate(() => [...document.querySelectorAll("#view-sessions, #view-sessions *, #modal .sm-ask, #modal .sm-ask *")]
        .filter((e) => parseFloat(getComputedStyle(e).borderLeftWidth) > 1 && getComputedStyle(e).borderLeftColor !== getComputedStyle(e).borderRightColor).map((e) => e.className));
      assert.deepEqual(border, [], "no left-border accent");
      await ask.getByRole("button", { name: w.cancel, exact: true }).click();
      await ask.waitFor({ state: "detached" });
      assert.equal(calls.length, 0, "Cancel deletes nothing");

      // Delete posts the ids picked, and the list goes without them
      await del.click();
      await ask.waitFor();
      await ask.getByRole("button", { name: w.del, exact: true }).click();
      await ask.waitFor({ state: "detached" });
      assert.deepEqual(calls[0], { path: "delete", body: { agent: "claude", ids: ["a-old", "b-1"] } });
      await row("a-old").waitFor({ state: "detached" });
      assert.equal(await row("b-1").count(), 0);
      await said(w.moved);
      assert(await del.isDisabled(), "nothing is picked any more");

      // a running session is left, and said so
      await reach(row("a-run"));
      await row("a-run").locator(".sm-del").click();
      await ask.waitFor();
      assert.equal((await ask.locator(".ehead b").textContent()).trim(), w.askOne);
      await ask.getByRole("button", { name: w.del, exact: true }).click();
      await ask.waitFor({ state: "detached" });
      assert.deepEqual(calls[1], { path: "delete", body: { agent: "claude", ids: ["a-run"] } });
      await said(w.active);
      assert.equal(await row("a-run").count(), 1, "still listed");

      // the Trash has the two, and Restore posts the key
      const trashBtn = view.locator(".sm-trash-btn");
      assert((await trashBtn.textContent()).includes(w.trash));
      assert.equal(await trashBtn.locator("em").textContent(), "2");
      await trashBtn.click();
      const trashed = view.locator(".sm-trash .row");
      await trashed.first().waitFor();
      assert.equal(await trashed.count(), 2);
      const r0 = trashed.filter({ hasText: "fix the login form" });
      await r0.getByRole("button", { name: w.restore }).click();
      for (let i = 0; i < 60 && calls.length < 3; i++) await page.waitForTimeout(50);
      assert.deepEqual(calls[2], { path: "restore", body: { key: "claude/20261001-a-old" } });
      await said(w.restored);
      await page.waitForFunction(() => document.querySelectorAll("#view-sessions .sm-trash .row").length === 1);

      // back to the list: restored; an agent magpie can't delete from has no delete
      await trashBtn.click();
      await row("a-old").waitFor();
      await view.locator(".sm-agents .opt", { hasText: "OpenCode" }).click();
      await view.locator('.row.sm-sess[data-id="o-1"]').waitFor();
      assert.equal((await view.locator(".sm-note").textContent()).trim(), w.cant);
      assert.equal(await view.locator(".sm-del, .sm-check").count(), 0);
      assert(!(await view.locator(".sm-bar").isVisible()));
      const mutationCount = calls.length;
      await view.locator(".sm-agents .opt", { hasText: "Hermes" }).click();
      const hermes = view.locator('.row.sm-sess[data-id="h-1"]');
      await hermes.waitFor();
      assert.equal((await view.locator(".sm-note").textContent()).trim(), w.hermesNote);
      assert.equal(await hermes.locator(".sm-resume, .sess-resume, .sess-term, .sm-del, .sm-check").count(), 0);
      assert.equal(await view.locator(".sm-folder-del").count(), 0);
      assert(!(await view.locator(".sm-bar").isVisible()));
      assert.equal(calls.length, mutationCount, "viewing Hermes sessions sends no mutation");
      if (process.env.SCREENSHOT_DIR) {
        await fs.mkdir(process.env.SCREENSHOT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.SCREENSHOT_DIR, `${engine}-${lang}-hermes.png`), fullPage: true });
      }

      const missing = await page.evaluate(() => [
        "Sessions", "Filter sessions", "No sessions yet", "Select every session shown", "Select", "Delete", "Trash", "No folder", "deleted {when}",
        "Delete this session?", "Delete {n} sessions?", "Moved to magpie's trash", "{n} sessions moved to magpie's trash", "Restore", "{title} restored",
        "{title} is still being written to; close it in {agent} and try again in a minute",
        "{n} sessions are still being written to; close them in {agent} and try again in a minute",
        "Trash is empty", "Sessions deleted here wait in magpie's trash, to be restored.",
        "Claude Code's, Codex's, Hermes's, OpenCode's and Pi's sessions on this computer show up here, by the folder they ran in.",
        "magpie can list {agent}'s sessions and resume them, but not delete them: they aren't kept as files of their own.",
        "magpie can list {agent}'s sessions, but cannot resume or delete them.",
        "These {agent} sessions are read only; magpie can list them, but cannot resume or delete them.",
        "Some {agent} sessions are read only and cannot be deleted.",
        "Their files are moved to magpie's trash ({dir}), not erased: Trash puts them back. A session written to in the last minute is left alone, as {agent} may still be running it.",
        "Deleted sessions are kept in {dir} until you erase them here; magpie never erases them by itself.",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });
  }
}
