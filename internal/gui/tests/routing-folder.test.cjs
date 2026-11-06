// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group's rule may be for the folder the agent works in (Cario0x
// on X: routing by the project being worked on). The group's rule reads its
// folders; the trace tells the rule matched by folder and the folder the
// agent said; in the editor the folders are a condition of their own, typed
// comma separated, a folder that isn't a whole path refused before saving,
// a rule with only folders not refused as having no condition, and saved
// as folders:[…]. In every language, and at a phone's width the condition
// stays inside the editor.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/opus", name: "opus", providerName: "A", icon: "generic", context: 1000000, ready: true },
  { id: "b/kimi", name: "kimi", providerName: "B", icon: "generic", context: 256000, ready: true },
];
const groups = () => ({
  models,
  groups: [{ id: "main", name: "Main", members: ["a/opus", "b/kimi"], routing: "order", ready: true,
    memberInfo: [{ id: "a/opus", ready: true, context: 1000000 }, { id: "b/kimi", ready: true, context: 256000 }], rules: [{ use: "b/kimi", folders: ["~/work/acme", "/srv/oss"] }] }],
  pools: [],
});
const now = Date.now();
const route = {
  id: 7, seq: 7, time: new Date(now - 2000).toISOString(), agent: "claude", model: "main", provider: "b", done: true, status: 200, ms: 900, tokens: 9000,
  group: { id: "main", name: "Main", members: ["a/opus", "b/kimi"] },
  order: [{ id: "b", provider: "b", name: "B", kind: "key", model: "kimi", known: true }, { id: "a", provider: "a", name: "A", kind: "key", model: "opus", known: true }],
  tries: [{ id: "b", model: "kimi", start: new Date(now - 2000).toISOString(), done: true, ms: 800, status: 200 }],
  rule: { n: 1, use: "b/kimi", when: ["folder ~/work/acme|/srv/oss"], turn: 2, tokens: 9000, folder: "/Users/me/work/acme/web" },
};

const words = {
  en: { cond: "in ~/work/acme / /srv/oss", dir: "The agent works in /Users/me/work/acme/web.", label: "in folder", bad: "Rule 1: a folder is a whole path, like ~/work or /Users/me/work", edit: "Edit", save: "Save" },
  zh: { cond: "在 ~/work/acme / /srv/oss 中", dir: "Agent 的工作目录是 /Users/me/work/acme/web。", label: "项目文件夹", bad: "规则 1：文件夹需是完整路径，如 ~/work 或 /Users/me/work", edit: "编辑", save: "保存" },
  "zh-TW": { cond: "在 ~/work/acme / /srv/oss 中", dir: "Agent 的工作目錄是 /Users/me/work/acme/web。", label: "專案資料夾", bad: "規則 1：資料夾需是完整路徑，如 ~/work 或 /Users/me/work", edit: "編輯", save: "儲存" },
  ja: { cond: "~/work/acme / /srv/oss 内", dir: "エージェントの作業フォルダは /Users/me/work/acme/web です。", label: "フォルダ", bad: "ルール 1：フォルダは ~/work や /Users/me/work のような完全なパスで指定してください", edit: "編集", save: "保存" },
  de: { cond: "in ~/work/acme / /srv/oss", dir: "Der Agent arbeitet in /Users/me/work/acme/web.", label: "im Ordner", bad: "Regel 1: Ein Ordner ist ein ganzer Pfad, etwa ~/work oder /Users/me/work", edit: "Bearbeiten", save: "Speichern" },
};

function serve(lang, posts) {
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

async function open(t, engine, lang, width) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 1400 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], posts = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, posts));
  await page.goto("http://magpie.test/?view=routing");
  const group = page.locator(".rt-group").first();
  await group.waitFor({ state: "attached" });
  return { page, group, errors, posts };
}

// the view wheeled down to a button, as the reader would (the page keeps
// still for any other scroll), and the button clicked where it is
async function press(page, b) {
  await page.mouse.move(Math.min(550, page.viewportSize().width / 2), 600);
  for (let i = 0; i < 10 && (await b.boundingBox()).y > 1400 - 160; i++) {
    await page.mouse.wheel(0, 300);
    await page.waitForTimeout(150);
  }
  // the editor grows open for a moment: the button is clicked once it
  // holds still, not where it was on its way down
  let bb = await b.boundingBox();
  for (let i = 0; i < 40; i++) {
    await page.waitForTimeout(50);
    const now = await b.boundingBox();
    if (now.x === bb.x && now.y === bb.y) break;
    bb = now;
  }
  await page.mouse.click(bb.x + bb.width / 2, bb.y + bb.height / 2);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    test(`${engine} ${lang}: a rule by folder`, async (t) => {
      const { page, group, errors, posts } = await open(t, engine, lang, 1100);

      // the trace: the rule matched by its folders, and the folder the agent said
      await page.waitForFunction((s) => document.documentElement.innerText.includes(s), w.dir);
      const said = await page.evaluate(() => document.documentElement.innerText);
      assert(said.includes(w.cond), `the rule's folders are told: ${said.slice(0, 2000)}`);
      await page.evaluate(() => window.show("groups"));
      await group.waitFor();

      // the group's rule, in words
      const title = await group.evaluate((g) => [...g.querySelectorAll("[title]")].map((e) => e.title).join("\n"));
      assert(title.includes(`1. ${w.cond} →`), `the rule reads by its folders: ${title}`);

      // the editor: the folders typed in their own condition
      await group.locator("button", { hasText: w.edit }).click();
      const ed = page.locator(".rt-gedit");
      const fd = ed.locator(".rt-rule").first().locator(".rt-cond.fd");
      await fd.waitFor();
      assert.equal(await fd.getAttribute("class"), "rt-cond fd on");
      assert.equal(await fd.locator("span").first().textContent(), w.label);
      const box = fd.locator("input");
      assert.equal(await box.inputValue(), "~/work/acme, /srv/oss");

      // not a whole path: refused, nothing saved
      await box.fill("work/acme");
      const saveBtn = ed.locator("button.primary", { hasText: w.save });
      await press(page, saveBtn);
      await page.waitForFunction((s) => document.documentElement.innerText.includes(s), w.bad);
      assert.equal(posts.filter((p) => p.path === "/api/groups/save").length, 0);

      // emptied, the rule has no condition and the folder's is off
      await box.fill("");
      assert.equal(await fd.getAttribute("class"), "rt-cond fd");

      // whole paths, one with a space: saved as typed, a rule with folders alone
      await box.fill("~/work/acme , ~/My Project/,");
      assert.equal(await fd.getAttribute("class"), "rt-cond fd on");
      await press(page, saveBtn);
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      const save = posts.find((p) => p.path === "/api/groups/save");
      assert(save, `saved: ${JSON.stringify(posts)}`);
      assert.deepEqual(save.body.rules.map((r) => [r.use, r.folders]), [["b/kimi", ["~/work/acme", "~/My Project/"]]]);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a rule by folder at a phone's width`, async (t) => {
      const { page, group, errors } = await open(t, engine, lang, 390);
      await page.evaluate(() => window.show("groups"));
      await press(page, group.locator("button", { hasText: w.edit })); // the group is below the fold here
      const fd = page.locator(".rt-gedit .rt-rule").first().locator(".rt-cond.fd");
      await fd.waitFor();
      const [b, row] = await Promise.all([fd.boundingBox(), page.locator(".rt-gedit .rt-rule").first().boundingBox()]);
      assert(b.x >= row.x - 1 && b.x + b.width <= row.x + row.width + 1, `the folders' condition stays in its rule: ${JSON.stringify([b, row])}`);
      const wide = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      assert(wide <= 0, `no sideways scroll: ${wide}px`);
      assert.deepEqual(errors, []);
    });
  }
}
