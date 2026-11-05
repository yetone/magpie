// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group with two models and no rule says what a rule is for,
// with the split asked for (mr_coffee0083 on Discord: "一个专门思考的模型
// 几个专门写代码的模型", and nothing in the editor to go by): a line under
// Rules and an Add an example rule that adds a rule sending planning and
// hard problems to the last model, its words in the reader's language in
// its box, Jev picked to tell them. Once a rule is there, neither shows; a
// group of one model, or one routed by hand, never shows them. In every
// language, Chromium and WebKit, at a wide and a phone's width, nothing
// past the editor's side. Run with ASSETS set to the assets before this
// change to see it fail.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.ASSETS || path.join(__dirname, "../assets"));
const models = [
  { id: "a/opus", name: "opus", providerName: "A", icon: "generic", context: 1000000, ready: true },
  { id: "b/flash", name: "flash", providerName: "B", icon: "generic", context: 1000000, ready: true },
];
const member = (id) => ({ id, ready: true, context: 1000000, images: true });
const rule = (use, more) => ({ use, pause: false, tokens: 0, images: false, effort: "", agents: [], intent: "", compact: false, folders: [], exceptFolders: [], time: null, ...more });
let shape = {};
const groups = () => ({
  models,
  deciders: [{ id: "typesafe/jev-latest", name: "Jev", providerName: "TypeSafe", icon: "generic" }],
  groups: [{ id: "main", name: "Main", members: ["b/flash", "a/opus"], routing: "", ready: true, classifier: "",
    rules: [], memberInfo: [member("b/flash"), member("a/opus")], ...shape }],
  pools: [],
});

const words = {
  en: { edit: "Edit", save: "Save", hint: "No rule yet", intent: "planning, design, or a hard problem to think through" },
  zh: { edit: "编辑", save: "保存", hint: "还没有规则", intent: "规划、设计，或需要深入思考的难题" },
  "zh-TW": { edit: "編輯", save: "儲存", hint: "還沒有規則", intent: "規劃、設計，或需要深入思考的難題" },
  ja: { edit: "編集", save: "保存", hint: "まだルールがありません", intent: "計画、設計、またはじっくり考える必要がある難問" },
  de: { edit: "Bearbeiten", save: "Speichern", hint: "Noch keine Regel", intent: "Planung, Entwurf oder ein schwieriges Problem zum Durchdenken" },
};

function serve(lang, posts) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
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
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404 }); }
  };
}

// wheel to a control, as the reader would (the page keeps still for any
// other scroll), and return where it is
async function reach(page, loc) {
  const height = page.viewportSize().height;
  await page.mouse.move(Math.min(550, (page.viewportSize().width / 2) | 0), 300);
  for (let i = 0; i < 30; i++) {
    const y = (await loc.boundingBox()).y;
    if (y > 80 && y < height - 160) break;
    await page.mouse.wheel(0, y < 80 ? -300 : 300);
    await page.waitForTimeout(150);
  }
  return loc.boundingBox();
}

// reached, then clicked with the mouse
async function press(page, loc) {
  const b = await reach(page, loc);
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
}

async function openEditor(page, w) {
  const group = page.locator(".rt-group").first();
  await group.waitFor();
  await press(page, group.locator("button", { hasText: w.edit }));
  const ed = page.locator(".rt-gedit");
  await ed.waitFor();
  return ed;
}

async function saved(page, ed, w, posts) {
  const n = posts.length;
  await press(page, ed.locator("button.primary", { hasText: w.save }));
  await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
  const save = posts.slice(n).find((p) => p.path === "/api/groups/save");
  assert(save, `saved: ${JSON.stringify(posts)}`);
  return save.body;
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    for (const width of [1100, 390]) {
      test(`${engine} ${lang} ${width}: a group with no rule offers an example rule`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 1400 } })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        shape = {};
        await page.goto("http://magpie.test/?view=routing");

        let ed = await openEditor(page, w);
        const hint = ed.locator(".hint", { hasText: w.hint });
        const tryIt = ed.locator("button.rt-try");
        await hint.waitFor();
        assert(await tryIt.isVisible(), "the example's button shows");
        assert.match(await hint.textContent(), lang === "en" ? /In order/ : /./);
        // nothing past the editor's side, nor a sideways scroll
        const past = await page.evaluate(() => {
          const e = document.querySelector(".rt-gedit").getBoundingClientRect();
          return [...document.querySelectorAll(".rt-rules, .rt-rules *, .rt-gedit .hint")].filter((x) => x.offsetParent && x.getBoundingClientRect().right > e.right + 1).map((x) => x.className || x.tagName);
        });
        assert.deepEqual(past, []);
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), "no sideways scroll");

        await press(page, tryIt);
        const rows = ed.locator(".rt-rule");
        await rows.first().waitFor();
        assert.equal(await rows.count(), 1);
        assert.equal(await rows.first().locator(".rt-cond.in textarea").inputValue(), w.intent);
        assert.match(await rows.first().locator(".rt-model").textContent(), /opus/, "to the last member");
        assert(!(await tryIt.isVisible()) && !(await hint.isVisible()), "gone once a rule is there");
        const body = await saved(page, ed, w, posts);
        assert.deepEqual(body.rules, [rule("a/opus", { useEffort: "", intent: w.intent })]);
        assert.equal(body.classifier, "typesafe/jev-latest");
        assert.deepEqual(errors, []);

        // one model, or routed by hand: no example
        for (const s of [{ members: ["a/opus"], memberInfo: [member("a/opus")] }, { routing: "manual" }]) {
          shape = s;
          await page.reload();
          ed = await openEditor(page, w);
          await ed.locator(".rt-rules").waitFor({ state: "attached" });
          assert(!(await tryIt.isVisible()), `no example: ${JSON.stringify(s)}`);
          assert.equal(await hint.count(), 0);
        }
      });
    }
  }
}
