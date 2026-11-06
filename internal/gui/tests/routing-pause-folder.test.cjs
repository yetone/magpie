// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group's pause can hold by the folder the agent works in (#1606,
// Ti800: a subscription's quota kept for the long task in one folder). The
// group's rule reads "not in <folders> → pause <model>"; the trace tells the
// model was paused and the folder the agent said, or that it said none; in
// the editor a pause shows the folder conditions (in folder, not in folder)
// and keeps them when it is picked, a folder that isn't a whole path is
// refused, a pause with no condition left is refused, and it is saved as
// pause:true with exceptFolders:[…]. In every language, and at a phone's
// width the conditions stay inside the editor.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/opus", name: "opus", providerName: "A", icon: "generic", context: 1000000, ready: true },
  { id: "b/opus", name: "opus", providerName: "B", icon: "generic", context: 1000000, ready: true },
];
const groups = () => ({
  models,
  groups: [{ id: "main", name: "Main", members: ["a/opus", "b/opus"], routing: "order", ready: true,
    memberInfo: [{ id: "a/opus", ready: true, context: 1000000 }, { id: "b/opus", ready: true, context: 1000000 }],
    rules: [{ use: "a/opus", pause: true, exceptFolders: ["~/big-task"] }] }],
  pools: [],
});
const now = Date.now();
const routeFrom = (folder) => ({
  id: 7, seq: 7, time: new Date(now - 2000).toISOString(), agent: "claude", model: "main", provider: "b", done: true, status: 200, ms: 900, tokens: 9000,
  group: { id: "main", name: "Main", members: ["b/opus"], paused: [{ member: "a/opus", rule: 1, use: "a/opus", when: ["not folder ~/big-task"], byFolder: true, ...(folder ? { folder } : {}) }] },
  order: [{ id: "b", provider: "b", name: "B", kind: "key", model: "opus", known: true }],
  tries: [{ id: "b", model: "opus", start: new Date(now - 2000).toISOString(), done: true, ms: 800, status: 200 }],
});

// the rule's ⋯ menu: turned to send-to, and back to a pause
const TURN = {
  en: ["Send to the model instead", "Pause the model instead"],
  zh: ["改为先发给该模型", "改为暂停该模型"],
  "zh-TW": ["改為先發給該模型", "改為暫停該模型"],
  ja: ["代わりにモデルへ送信", "代わりにモデルを一時停止"],
  de: ["Stattdessen an das Modell senden", "Stattdessen Modell pausieren"],
};
const words = {
  en: { cond: "not in ~/big-task", dir: "The agent works in /Users/me/scratch.", none: "The agent didn't say which folder it works in.", label: "not in folder", in: "in folder",
    aside: "a/opus is paused by rule 1 (not in ~/big-task), so it is left out until that ends. The agent works in /Users/me/scratch.",
    bad: "Rule 1: a folder is a whole path, like ~/work or /Users/me/work", bare: "Rule 1: a pause needs hours, days, agents or folders. Add one with its +, or remove the rule from its ⋯ menu", edit: "Edit", save: "Save", pause: "pause", send: "send to" },
  zh: { cond: "不在 ~/big-task 中", dir: "Agent 的工作目录是 /Users/me/scratch。", none: "Agent 没有说明它的工作目录。", label: "不在文件夹", in: "项目文件夹",
    bad: "规则 1：文件夹需是完整路径，如 ~/work 或 /Users/me/work", bare: "第 1 条规则：暂停需要设时段、星期、Agent 或文件夹。用它的 + 添加，或在它的 ⋯ 菜单里删除这条规则", edit: "编辑", save: "保存", pause: "暂停", send: "先发给" },
  "zh-TW": { cond: "不在 ~/big-task 中", dir: "Agent 的工作目錄是 /Users/me/scratch。", none: "Agent 沒有說明它的工作目錄。", label: "不在資料夾", in: "專案資料夾",
    bad: "規則 1：資料夾需是完整路徑，如 ~/work 或 /Users/me/work", bare: "第 1 條規則：暫停需要設時段、星期、Agent 或資料夾。用它的 + 新增，或在它的 ⋯ 選單裡刪除這條規則", edit: "編輯", save: "儲存", pause: "暫停", send: "先發給" },
  ja: { cond: "~/big-task 以外", dir: "エージェントの作業フォルダは /Users/me/scratch です。", none: "エージェントは作業フォルダを伝えていません。", label: "除外フォルダ", in: "フォルダ",
    bad: "ルール 1：フォルダは ~/work や /Users/me/work のような完全なパスで指定してください", bare: "ルール 1：一時停止には時間帯、曜日、エージェント、またはフォルダが必要です。+ で追加するか、⋯ メニューからこのルールを削除してください", edit: "編集", save: "保存", pause: "一時停止", send: "送信先" },
  de: { cond: "nicht in ~/big-task", dir: "Der Agent arbeitet in /Users/me/scratch.", none: "Der Agent hat nicht gesagt, in welchem Ordner er arbeitet.", label: "nicht im Ordner", in: "im Ordner",
    bad: "Regel 1: Ein Ordner ist ein ganzer Pfad, etwa ~/work oder /Users/me/work", bare: "Regel 1: eine Pause braucht Uhrzeiten, Tage, Agenten oder Ordner. Füge mit ihrem + etwas hinzu oder entferne die Regel über ihr ⋯-Menü", edit: "Bearbeiten", save: "Speichern", pause: "pausieren", send: "senden an" },
};

function serve(lang, posts, route) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  let first = true;
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      const routes = first ? [route] : [];
      first = false;
      return json({ mine: true, now: new Date().toISOString(), seq: 7, totals: { requests: 1, rerouted: 0, errors: 0 }, routes });
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

async function open(t, engine, lang, width, folder) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 1400 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], posts = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, posts, routeFrom(folder)));
  await page.goto("http://magpie.test/?view=routing");
  const group = page.locator(".rt-group").first();
  await group.waitFor({ state: "attached" });
  return { page, group, errors, posts };
}

// the view wheeled to a button, as the reader would (the page keeps
// still for any other scroll), and the button clicked where it is
async function press(page, b) {
  await page.mouse.move(Math.min(550, page.viewportSize().width / 2), 600);
  for (let i = 0; i < 20; i++) {
    const y = (await b.boundingBox()).y;
    if (y > 80 && y < 1400 - 160) break;
    await page.mouse.wheel(0, y < 80 ? -300 : 300);
    await page.waitForTimeout(150);
  }
  const bb = await b.boundingBox();
  await page.mouse.click(bb.x + bb.width / 2, bb.y + bb.height / 2);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    test(`${engine} ${lang}: a pause outside a folder`, async (t) => {
      const { page, group, errors, posts } = await open(t, engine, lang, 1100, "/Users/me/scratch");

      // the trace: paused outside the folder, and the folder the agent said
      await page.waitForFunction((s) => document.documentElement.innerText.includes(s), w.dir);
      const said = await page.evaluate(() => document.documentElement.innerText);
      assert(said.includes(w.cond), `the pause's folders are told: ${said.slice(0, 2000)}`);
      if (w.aside) assert(said.includes(w.aside), `the paused reason: ${said.slice(0, 2000)}`);
      await page.evaluate(() => window.show("groups"));
      await group.waitFor();

      // the group's rule, in words
      const title = await group.evaluate((g) => [...g.querySelectorAll("[title]")].map((e) => e.title).join("\n"));
      assert(title.includes(`1. ${w.cond} → ${w.pause}`), `the rule reads by its folders: ${title}`);

      // the editor: a pause shows the folder condition it has; in folder
      // is offered for it, the request's conditions aren't
      await group.locator("button", { hasText: w.edit }).click();
      const ed = page.locator(".rt-gedit");
      const row = ed.locator(".rt-rule").first();
      const xf = row.locator(".rt-cond.xf");
      await xf.waitFor();
      assert.equal(await xf.getAttribute("class"), "rt-cond xf on");
      assert.equal(await xf.locator("span").first().textContent(), w.label);
      assert.equal(await row.locator(".rt-cond.fd").count(), 0, "in folder isn't set, so not drawn");
      assert.equal(await row.locator(".rt-cond.tk").count(), 0, "no tokens on a pause");
      await press(page, row.locator(".rt-addc"));
      const offered = await page.locator(".proto-menu .pm-item .pm-name").allTextContents();
      assert(offered.includes(w.in), `in folder offered on a pause: ${offered}`);
      assert(!offered.includes(w.label), `not in folder is set already: ${offered}`);
      await page.keyboard.press("Escape");
      const box = xf.locator("input");
      assert.equal(await box.inputValue(), "~/big-task");

      // not a whole path: refused, nothing saved
      await box.fill("big-task");
      const saveBtn = ed.locator("button.primary", { hasText: w.save });
      await press(page, saveBtn);
      await page.waitForFunction((s) => document.documentElement.innerText.includes(s), w.bad);
      assert.equal(posts.filter((p) => p.path === "/api/groups/save").length, 0);

      // emptied, the pause has no condition left: refused
      await box.fill("");
      assert.equal(await xf.getAttribute("class"), "rt-cond xf");
      await press(page, saveBtn);
      await page.waitForFunction((s) => document.querySelector("#status")?.textContent === s, w.bare);
      assert.equal(posts.filter((p) => p.path === "/api/groups/save").length, 0);

      // turned to send-to and back to a pause, the folders typed are kept
      await box.fill("~/big-task , ~/My Task/,");
      await press(page, row.locator(".rt-rmore"));
      assert.equal(await page.locator("select").count(), 0, "the app menu, not a native select");
      await page.locator(".row-menu .rm-item", { hasText: TURN[lang][0] }).click();
      assert.equal(await ed.locator(".rt-rule").first().locator(".rt-act").count(), 0, "a send-to rule says no action");
      await press(page, ed.locator(".rt-rule").first().locator(".rt-addc"));
      assert(await page.locator(".proto-menu .pm-item").count() > 4, "a send-to rule is offered the request's conditions too");
      await page.keyboard.press("Escape");
      await press(page, ed.locator(".rt-rule").first().locator(".rt-rmore"));
      await page.locator(".row-menu .rm-item", { hasText: TURN[lang][1] }).click();
      const xf2 = ed.locator(".rt-rule").first().locator(".rt-cond.xf");
      assert.equal(await xf2.getAttribute("class"), "rt-cond xf on");
      assert.equal(await xf2.locator("input").inputValue(), "~/big-task, ~/My Task/");

      await press(page, saveBtn);
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      const save = posts.find((p) => p.path === "/api/groups/save");
      assert(save, `saved: ${JSON.stringify(posts)}`);
      assert.deepEqual(save.body.rules.map((r) => [r.use, !!r.pause, r.exceptFolders]), [["a/opus", true, ["~/big-task", "~/My Task/"]]]);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a pause outside a folder at a phone's width, the agent saying none`, async (t) => {
      const { page, group, errors } = await open(t, engine, lang, 390, "");
      await page.waitForFunction((s) => document.documentElement.innerText.includes(s), w.none);
      await page.evaluate(() => window.show("groups"));
      await group.waitFor();
      await press(page, group.locator("button", { hasText: w.edit })); // the group is below the fold here
      const row = page.locator(".rt-gedit .rt-rule").first();
      const xf = row.locator(".rt-cond.xf");
      await xf.waitFor();
      const r = await row.boundingBox();
      for (const c of [xf, row.locator(".rt-addc"), row.locator(".rt-model")]) {
        const b = await c.boundingBox();
        assert(b.x >= r.x - 1 && b.x + b.width <= r.x + r.width + 1, `the folders' condition stays in its rule: ${JSON.stringify([b, r])}`);
      }
      const wide = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      assert(wide <= 0, `no sideways scroll: ${wide}px`);
      assert.deepEqual(errors, []);
    });
  }
}
