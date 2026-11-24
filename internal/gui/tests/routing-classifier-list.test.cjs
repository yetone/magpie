// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group asks its classifiers in order (qinzhidaoa on X: the one
// it had, MiMo on OpenCode Go, answered 502 ECONNRESET, and every turn
// lost its intent). The editor draws one chip per classifier in order, a
// + adds one asked if the one before fails (from the app's menu, never a
// native select), a chip's × takes it out while another is left, a chip
// picking one already listed trades their places, and Alt+←/→ moves one.
// Saved as classifier (the first) and classifierMore (the rest). The
// trace says which classifier couldn't answer and which then did.
// Chromium and WebKit, every language, at a wide and a phone's width.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.ASSETS || path.join(__dirname, "../assets"));
const models = [
  { id: "a/opus", name: "opus", providerName: "A", icon: "generic", context: 1000000, ready: true },
  { id: "b/flash", name: "flash", providerName: "B", icon: "generic", context: 1000000, ready: true },
  { id: "c/mimo", name: "mimo", providerName: "OpenCode Go", icon: "generic", context: 256000, ready: true },
  { id: "d/haiku", name: "haiku", providerName: "D", icon: "generic", context: 200000, ready: true },
  { id: "e/mini", name: "mini", providerName: "E", icon: "generic", context: 200000, ready: true },
];
const member = (id) => ({ id, ready: true, context: 1000000 });
const groups = () => ({
  models, deciders: [],
  groups: [{ id: "main", name: "Main", members: ["a/opus", "b/flash"], routing: "order", ready: true,
    classifier: "c/mimo", classifierMore: ["d/haiku"],
    rules: [{ use: "a/opus", intent: "complex" }], memberInfo: ["a/opus", "b/flash"].map(member) }],
  pools: [],
});
const now = Date.now();
const err = "c/mimo: OpenCode Go: read ECONNRESET (HTTP 502)";
const route = {
  id: 7, seq: 7, time: new Date(now - 2000).toISOString(), agent: "claude", model: "main", provider: "a", done: true, status: 200, ms: 900, tokens: 9000,
  group: { id: "main", name: "Main", members: ["a/opus", "b/flash"] },
  order: [{ id: "a", provider: "a", name: "A", kind: "key", model: "opus", known: true }],
  tries: [{ id: "a", model: "opus", start: new Date(now - 2000).toISOString(), done: true, ms: 800, status: 200 }],
  rule: { n: 1, use: "a/opus", turn: 2, tokens: 9000,
    classified: { by: "d/haiku", tried: [{ by: "c/mimo", error: err }], intents: ["complex"], intent: "complex", said: "1", ms: 300 } },
};

const words = {
  en: { edit: "Edit", save: "Save", add: "Add a model asked if this one fails", rm: "Remove mimo · OpenCode Go from the classifiers",
    tried: `c/mimo couldn't answer — ${err} — so d/haiku was asked.`, said: "d/haiku was asked which of “complex” turn 2 is, and said “complex” (" },
  zh: { edit: "编辑", save: "保存", add: "添加一个备用模型：前一个失败时询问它", rm: "从意图判断模型中移除 mimo · OpenCode Go",
    tried: `c/mimo 没能回答（${err}），于是改问 d/haiku。`, said: "d/haiku 判断第 2 轮属于 “complex” 中的“complex”（" },
  "zh-TW": { edit: "編輯", save: "儲存", add: "新增一個備用模型：前一個失敗時詢問它", rm: "從意圖判斷模型中移除 mimo · OpenCode Go",
    tried: `c/mimo 沒能回答（${err}），於是改問 d/haiku。`, said: "d/haiku 判斷第 2 輪屬於 “complex” 中的「complex」（" },
  ja: { edit: "編集", save: "保存", add: "失敗したときに尋ねるモデルを追加", rm: "意図判定モデルから mimo · OpenCode Go を外す",
    tried: `c/mimo は答えられなかったため（${err}）、d/haiku に尋ねました。`, said: "d/haiku にターン 2 が “complex” のどれかを尋ねたところ、「complex」と答えました（" },
  de: { edit: "Bearbeiten", save: "Speichern", add: "Ein Modell hinzufügen, das gefragt wird, wenn dieses ausfällt", rm: "mimo · OpenCode Go aus den Klassifikatoren entfernen",
    tried: `c/mimo konnte nicht antworten (${err}), also wurde d/haiku gefragt.`, said: "d/haiku wurde gefragt, welche von “complex” Runde 2 ist, und sagte „complex“ (" },
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
const chips = (cls) => cls.locator(".rt-cls").evaluateAll((bs) => bs.map((b) => b.textContent.trim().split(" · ")[0]));
const saves = (posts) => posts.filter((p) => p.path === "/api/groups/save");

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    for (const width of [1100, 390]) {
      test(`${engine} ${lang} ${width}: a group asks its classifiers in order`, async (t) => {
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

        // the trace: the first couldn't answer, the second did
        await page.waitForFunction((s) => document.documentElement.innerText.includes(s), w.tried).catch(() => {});
        const said = await page.evaluate(() => document.documentElement.innerText);
        assert(said.includes(w.tried), `which couldn't answer: ${said.slice(0, 3000)}`);
        assert(said.includes(w.said), `which answered: ${said.slice(0, 3000)}`);
        assert(said.indexOf(w.tried) < said.indexOf(w.said), "in the order they were asked");
        await page.evaluate(() => window.show("groups"));
        await group.waitFor();

        await press(page, group.locator("button", { hasText: w.edit }));
        const ed = page.locator(".rt-gedit");
        const cls = ed.locator(".rt-classifier");
        await cls.waitFor();
        assert.deepEqual(await chips(cls), ["mimo", "haiku"]);
        assert.equal(await page.locator("select").count(), 0, "the app menu, not a native select");

        // the + adds one from those not listed
        const add = cls.locator(".rt-adduse");
        assert.equal(await add.getAttribute("title"), w.add);
        await press(page, add);
        await pick(page, "mini");
        assert.deepEqual(await chips(cls), ["mimo", "haiku", "mini"]);

        // the × takes one out
        await cls.locator(".rt-c", { has: page.locator(".rt-cls", { hasText: "mimo" }) }).hover();
        await press(page, cls.locator(`.rt-cx[title="${w.rm}"]`));
        assert.deepEqual(await chips(cls), ["haiku", "mini"]);

        // Alt+→ moves one later, keeping the focus on it
        await cls.locator(".rt-cls").first().focus();
        await page.keyboard.press("Alt+ArrowRight");
        assert.deepEqual(await chips(cls), ["mini", "haiku"]);
        assert.equal(await page.evaluate(() => document.activeElement?.textContent.trim().split(" · ")[0]), "haiku", "the focus moved with it");
        await page.keyboard.press("Alt+ArrowLeft");
        assert.deepEqual(await chips(cls), ["haiku", "mini"]);

        // a chip picking one already listed trades their places
        await press(page, cls.locator(".rt-cls").first());
        await pick(page, "mini");
        assert.deepEqual(await chips(cls), ["mini", "haiku"]);

        // nothing past the editor's side, nor a sideways scroll
        const over = await page.evaluate(() => {
          const e = document.querySelector(".rt-gedit").getBoundingClientRect();
          return [...document.querySelectorAll(".rt-classifier, .rt-classifier *")].filter((x) => x.offsetParent && x.getBoundingClientRect().right > e.right + 1).map((x) => x.className || x.tagName);
        });
        assert.deepEqual(over, []);
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), "no sideways scroll");

        await press(page, ed.locator("button.primary", { hasText: w.save }));
        await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
        const saved = saves(posts).at(-1).body;
        assert.deepEqual([saved.classifier, saved.classifierMore], ["e/mini", ["d/haiku"]]);
        assert.deepEqual(errors, []);
      });
    }
  }
}
