// Run with Node's test runner and Playwright on the module path; see README.md.
// ysm119 on Discord, on the Sessions page's Cursor tab: some rows had a
// delete and some had none, and the right-hand column (when, how many
// sessions) stood at a different place from row to row. Every session of
// an agent magpie deletes from has a delete; one it can't delete says why
// on it. A row without Resume, Continue in or delete, and a folder without
// its ⋯, keep that room, so every row's right-hand column lines up: with a
// title or none, a folder or none, hovered or not, while Resume says
// Copied, and at narrow widths. Both engines, every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const base = (id, cwd, title, min, extra = {}) => ({
  agent: "cursor", id, cwd, title, start: at(min + 5), last: at(min), models: [], input: 0, output: 0, cost: 0,
  messages: 4, size: 2048, files: 1, path: `~/.cursor/projects/x/agent-transcripts/${id}/${id}.jsonl`, ...extra,
});
// a CLI chat (resumed, carried on, deleted), the editor's chats (read
// only, kept in its database), a loose transcript, in folders and none
const spent = { models: [{ model: "claude-4.6-sonnet", priced: true, input: 1200000, output: 34000, calls: 12 }], input: 1200000, output: 34000, cost: 4.21 };
const sessions = [
  base("cli-1", "/work/app", "Port the parser to the new API", 2, { ...spent, deletable: true, resume: "cd /work/app && cursor-agent --resume cli-1", carry: [{ agent: "claude", command: "claude --resume x" }] }),
  base("ed-1", "/work/app", "Debugging access page", 20, { read_only: true, deletable: false, undeletable: "cursor-editor" }),
  base("ed-2", "/work/editor", "", 40, { read_only: true, deletable: false, undeletable: "cursor-editor" }),
  base("ed-3", "/work/editor", "Component changes review", 60, { ...spent, cost: 123.4, read_only: true, deletable: false, undeletable: "cursor-editor" }),
  base("loose-1", "", "Fix editor history", 80, { read_only: true, deletable: true }),
  base("cli-2", "/work/other", "", 100, { deletable: true, resume: "cd /work/other && cursor-agent --resume cli-2" }),
];

function serve(lang, calls) {
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
      return json({
        agents: [{ agent: "cursor", count: sessions.length, deletable: true, name: "Cursor", icon: "cursor" }],
        agent: "cursor", sessions, terminal: true, trash: [], trashDir: "~/Library/Application Support/magpie/trash/sessions",
      });
    }
    if (url.pathname.startsWith("/api/sessions/") && url.pathname !== "/api/sessions/manage") calls.push(url.pathname);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const why = {
  en: "Cursor's editor keeps this chat in its own database, which it holds open while it runs: delete it in Cursor.",
  zh: "Cursor 编辑器把这段对话存在它自己的数据库里，运行时一直占用着：请在 Cursor 里删除。",
  "zh-TW": "Cursor 編輯器把這段對話存在它自己的資料庫裡，執行時一直佔用著：請在 Cursor 裡刪除。",
  ja: "Cursor のエディタはこのチャットを自身のデータベースに保存し、実行中は開いたままにしています。Cursor で削除してください。",
  de: "Der Cursor-Editor speichert diesen Chat in seiner eigenen Datenbank, die er während der Ausführung geöffnet hält: Lösche ihn in Cursor.",
};

// edges is the right edge of each row's right-hand column: a folder's
// when and count, a session's cost and tokens, and its delete
const edges = (page) => page.evaluate(() => {
  const right = (e) => e && e.offsetParent ? Math.round(e.getBoundingClientRect().right * 2) / 2 : null;
  const rows = [...document.querySelectorAll("#view-sessions .row.sm-sess")].map((r) => ({
    id: r.dataset.id, cost: right(r.querySelector(".cost")), num: right(r.querySelector(".num")), del: right(r.querySelector(".sm-del")),
  }));
  const folders = [...document.querySelectorAll("#view-sessions .row.sm-folder")].map((r) => ({
    name: r.querySelector(".fold .name")?.textContent, when: right(r.querySelector(".sm-when")),
  }));
  return { rows, folders };
});

function lined(list, key, where) {
  const xs = list.map((r) => r[key]).filter((x) => x !== null);
  assert.ok(xs.length >= 2, `${where}: ${key} measured on ${xs.length} rows: ${JSON.stringify(list)}`);
  assert.ok(Math.max(...xs) - Math.min(...xs) <= 0.5, `${where}: ${key} right edges differ: ${JSON.stringify(list)}`);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: every Cursor row has a delete and its right-hand column lines up`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 1100, height: 900 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-sessions-cursor-align.png`), fullPage: true });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], calls = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, calls));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator('#nav button[data-view="sessions"]').click();
      const view = page.locator("#view-sessions");
      await view.locator(".row.sm-folder").first().waitFor();
      const shut = view.locator('.row.sm-folder .fold[aria-expanded="false"]');
      for (let i = 0; i < 10 && await shut.count(); i++) await shut.first().click();
      await page.waitForFunction((n) => document.querySelectorAll("#view-sessions .row.sm-sess").length === n, sessions.length);

      // every row has its delete; the editor's say why, and do nothing
      for (const s of sessions) {
        const del = view.locator(`.row.sm-sess[data-id="${s.id}"] .sm-del`);
        assert.equal(await del.count(), 1, `${s.id} has a delete`);
        if (s.deletable) {
          assert.equal(await del.getAttribute("aria-disabled"), null, s.id);
          continue;
        }
        assert.equal(await del.getAttribute("title"), why[lang], s.id);
        assert.equal(await del.getAttribute("aria-disabled"), "true", s.id);
      }
      // a click (aria-disabled, so Playwright's own would wait) says why
      await view.locator('.row.sm-sess[data-id="ed-1"] .sm-del').click({ force: true });
      await page.waitForFunction((w) => document.querySelector("#status")?.textContent === w, why[lang]);
      assert.equal(await page.locator("dialog.action-confirm[open]").count(), 0, "no delete asked for an editor chat");
      assert.deepEqual(calls, [], "nothing sent for an editor chat");
      // the hidden rooms are out of reach
      assert.equal(await view.locator(".sm-slot").evaluateAll((xs) => xs.filter((x) => !x.inert || x.getAttribute("aria-hidden") !== "true").length), 0);

      for (const width of [1100, 660, 420]) {
        await page.setViewportSize({ width, height: 900 });
        await page.waitForFunction(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(() => r(true)))));
        const still = await edges(page);
        const where = `${width}px`;
        lined(still.folders, "when", where);
        lined(still.rows, "cost", where);
        lined(still.rows, "del", where);
        if (width === 1100) lined(still.rows, "num", where);
        // hovered, a row and a folder still line up with the rest
        for (const target of [".row.sm-sess[data-id=\"ed-2\"]", ".row.sm-folder"]) {
          await view.locator(target).first().hover();
          const hov = await edges(page);
          lined(hov.folders, "when", `${where} hovering ${target}`);
          lined(hov.rows, "cost", `${where} hovering ${target}`);
        }
        await page.mouse.move(0, 0);
      }

      // Resume saying Copied takes no more room than Resume did
      await page.setViewportSize({ width: 1100, height: 900 });
      await page.evaluate(() => { try { Object.defineProperty(navigator, "clipboard", { value: { writeText: async () => {} }, configurable: true }); } catch {} });
      await view.locator('.row.sm-sess[data-id="cli-1"] .sess-resume').click();
      await view.locator('.row.sm-sess[data-id="cli-1"] .sess-resume.done').waitFor();
      lined((await edges(page)).rows, "cost", "while Copied");
      assert.deepEqual(errors, []);
    });
  }
}
