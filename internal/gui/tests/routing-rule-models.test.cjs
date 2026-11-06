// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group's rule names several models (monkeyin_92 on X: "can a
// rule take more than one model? Otherwise one rule with several models
// is several rules"). The editor draws one chip per model in order, a +
// adds the next from the group's others (from the app's menu, never a
// native select), a chip's × takes it out while the rule names another,
// and a chip picks another in its place, none the rule names twice. Saved
// as use (the first) and more (the rest); a rule of one model has no
// more. A pause names one model and has no +. The trace says the rule's
// next went first when its first had none to take it. Chromium and
// WebKit, every language, at a wide and a phone's width.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.ASSETS || path.join(__dirname, "../assets"));
const models = [
  { id: "a/opus", name: "opus", providerName: "A", icon: "generic", context: 1000000, ready: true },
  { id: "b/flash", name: "flash", providerName: "B", icon: "generic", context: 1000000, ready: true },
  { id: "c/mini", name: "mini", providerName: "C", icon: "generic", context: 1000000, ready: true },
];
const member = (id) => ({ id, ready: true, context: 1000000, images: true });
const groups = () => ({
  models, deciders: [],
  groups: [{ id: "main", name: "Main", members: ["a/opus", "b/flash", "c/mini"], routing: "order", ready: true,
    rules: [{ use: "b/flash", more: ["c/mini"], images: true }], memberInfo: models.map((m) => member(m.id)) }],
  pools: [],
});
const now = Date.now();
// b/flash had nothing to take it, and the rule's next, c/mini, went first
const route = {
  id: 7, seq: 7, time: new Date(now - 2000).toISOString(), agent: "claude", model: "main", provider: "c", done: true, status: 200, ms: 900, tokens: 9000,
  group: { id: "main", name: "Main", members: ["a/opus", "b/flash", "c/mini"] },
  order: [{ id: "c", provider: "c", name: "C", kind: "key", model: "mini", known: true }, { id: "a", provider: "a", name: "A", kind: "key", model: "opus", known: true }],
  tries: [{ id: "c", model: "mini", start: new Date(now - 2000).toISOString(), done: true, ms: 800, status: 200 }],
  rule: { n: 1, use: "b/flash", more: ["c/mini"], instead: "c/mini", when: ["images"], turn: 2, tokens: 9000, images: true },
};

const words = {
  en: { edit: "Edit", save: "Save", add: "Add a model it goes to next", rm: "Remove flash from this rule",
    why: "Rule 1 matches, but b/flash has no account or key that can take this request, so c/mini, which the rule names after it, goes first." },
  zh: { edit: "编辑", save: "保存", add: "添加下一个模型", rm: "从这条规则移除 flash",
    why: "规则 1 命中，但 b/flash 没有能接这次请求的账号或 Key，于是由这条规则的下一个模型 c/mini 排第一。" },
  "zh-TW": { edit: "編輯", save: "儲存", add: "新增下一個模型", rm: "從這條規則移除 flash",
    why: "規則 1 命中，但 b/flash 沒有能接這次請求的帳號或 Key，於是由這條規則的下一個模型 c/mini 排第一。" },
  ja: { edit: "編集", save: "保存", add: "次に使うモデルを追加", rm: "このルールから flash を外す",
    why: "ルール 1 に一致しましたが、b/flash にはこのリクエストを受けられるアカウントやキーがないため、このルールの次のモデル c/mini が先頭です。" },
  de: { edit: "Bearbeiten", save: "Speichern", add: "Ein Modell für danach hinzufügen", rm: "flash aus dieser Regel entfernen",
    why: "Regel 1 trifft zu, aber b/flash hat kein Konto und keinen Schlüssel, der diese Anfrage annehmen kann, daher kommt c/mini, das die Regel danach nennt, zuerst." },
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
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404 }); }
  };
}

// wheel to a control, as the reader would (the page keeps still for any
// other scroll), and click it where it is
async function press(page, loc) {
  const height = page.viewportSize().height;
  await page.mouse.move(Math.min(550, (page.viewportSize().width / 2) | 0), 300);
  for (let i = 0; i < 30; i++) {
    const y = (await loc.boundingBox()).y;
    if (y > 80 && y < height - 160) break;
    await page.mouse.wheel(0, y < 80 ? -300 : 300);
    await page.waitForTimeout(150);
  }
  const b = await loc.boundingBox();
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
}
const pick = async (page, label) => {
  const li = page.locator("#list li", { hasText: label });
  await li.first().waitFor();
  await li.first().click();
};
const chips = (row) => row.locator(".rt-then .rt-model").evaluateAll((bs) => bs.map((b) => b.textContent.trim().split(" · ")[0]));
const saves = (posts) => posts.filter((p) => p.path === "/api/groups/save");

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    for (const width of [1100, 390]) {
      test(`${engine} ${lang} ${width}: a rule names several models`, async (t) => {
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

        // the trace: the rule's next went first
        await page.waitForFunction((s) => document.documentElement.innerText.includes(s), w.why);
        await page.evaluate(() => window.show("groups"));
        await group.waitFor();

        await press(page, group.locator("button", { hasText: w.edit }));
        const ed = page.locator(".rt-gedit");
        const row = ed.locator(".rt-rule").first();
        await row.waitFor();
        assert.deepEqual(await chips(row), ["flash", "mini"]);
        assert.equal(await page.locator("select").count(), 0, "the app menu, not a native select");

        // the × takes one out; the last has none
        await row.locator(".rt-c", { has: page.locator(".rt-model", { hasText: "flash" }) }).hover();
        await press(page, row.locator(`.rt-cx[title="${w.rm}"]`));
        assert.deepEqual(await chips(row), ["mini"]);
        assert.equal(await row.locator(".rt-then .rt-cx").count(), 0, "a rule's one model can't be taken out");

        // the + adds the next, from those it doesn't name
        const add = row.locator(".rt-adduse");
        assert.equal(await add.getAttribute("title"), w.add);
        await press(page, add);
        assert.equal(await page.locator("#list li").count(), 2);
        await pick(page, "flash");
        assert.deepEqual(await chips(row), ["mini", "flash"]);
        await press(page, row.locator(".rt-adduse"));
        await pick(page, "opus");
        assert.deepEqual(await chips(row), ["mini", "flash", "opus"]);
        assert.equal(await row.locator(".rt-adduse").count(), 0, "no + once it names every model");

        // a chip picks another in its place, none named twice
        await press(page, row.locator(".rt-model").first());
        assert.equal(await page.locator("#list li").count(), 1);
        await pick(page, "mini");
        // nothing past the editor's side, nor a sideways scroll
        const over = await page.evaluate(() => {
          const e = document.querySelector(".rt-gedit").getBoundingClientRect();
          return [...document.querySelectorAll(".rt-rule, .rt-rule *")].filter((x) => x.offsetParent && x.getBoundingClientRect().right > e.right + 1).map((x) => x.className || x.tagName);
        });
        assert.deepEqual(over, []);
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), "no sideways scroll");

        await press(page, ed.locator("button.primary", { hasText: w.save }));
        await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
        const saved = saves(posts).at(-1).body.rules[0];
        assert.deepEqual([saved.use, saved.more], ["c/mini", ["b/flash", "a/opus"]]);
        assert.deepEqual(errors, []);
      });
    }
  }
}
