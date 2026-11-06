// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group's rule can be found and taken out (ppBruce on Discord,
// v0.1.1290: "once a rule is added there is no delete button, and a rule
// with empty conditions can't be saved"). The rule's ⋯, which holds Remove
// rule, is drawn whether or not the pointer is over the rule, and works by
// mouse and by keyboard. A rule left with no condition isn't saved: Save
// says so under that rule and in the status line, with the way out (its +,
// or Remove rule under its ⋯), and both go once the rule has a condition
// again. Chromium and WebKit, every language, at a wide and a phone's width.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.ASSETS || path.join(__dirname, "../assets"));
const models = [
  { id: "a/opus", name: "opus", providerName: "A", icon: "generic", context: 1000000, ready: true },
  { id: "b/flash", name: "Deepseek-V4.1-Flash", providerName: "WorkBuddy AI", icon: "generic", context: 1000000, ready: true },
];
const member = (id) => ({ id, ready: true, context: 1000000, images: true });
const rule = (use, more) => ({ use, pause: false, tokens: 0, images: false, effort: "", agents: [], intent: "", compact: false, folders: [], exceptFolders: [], time: null, ...more });
// the reporter's group: one rule, "has an image" → the flash model
const groups = () => ({
  models, deciders: [],
  groups: [{ id: "main", name: "Main", members: ["a/opus", "b/flash"], routing: "order", ready: true,
    rules: [rule("b/flash", { images: true })], memberInfo: [member("a/opus"), member("b/flash")] }],
  pools: [],
});

const words = {
  en: { edit: "Edit", save: "Save", image: "has an image", remove: "Remove rule",
    status: "Rule 1 has no condition. Add one with its +, or remove the rule from its ⋯ menu",
    under: "This rule has no condition. Add one with its +, or remove the rule from its ⋯ menu" },
  zh: { edit: "编辑", save: "保存", image: "带图片", remove: "删除规则",
    status: "第 1 条规则还没有条件：用它的 + 添加一个，或在它的 ⋯ 菜单里删除这条规则",
    under: "这条规则还没有条件：用它的 + 添加一个，或在它的 ⋯ 菜单里删除这条规则" },
  "zh-TW": { edit: "編輯", save: "儲存", image: "帶圖片", remove: "刪除規則",
    status: "第 1 條規則還沒有條件：用它的 + 新增一個，或在它的 ⋯ 選單裡刪除這條規則",
    under: "這條規則還沒有條件：用它的 + 新增一個，或在它的 ⋯ 選單裡刪除這條規則" },
  ja: { edit: "編集", save: "保存", image: "画像あり", remove: "ルールを削除",
    status: "ルール 1 に条件がありません。+ で追加するか、⋯ メニューからこのルールを削除してください",
    under: "このルールには条件がありません。+ で追加するか、⋯ メニューからこのルールを削除してください" },
  de: { edit: "Bearbeiten", save: "Speichern", image: "enthält ein Bild", remove: "Regel entfernen",
    status: "Regel 1 hat keine Bedingung. Füge mit ihrem + eine hinzu oder entferne die Regel über ihr ⋯-Menü",
    under: "Diese Regel hat keine Bedingung. Füge mit ihrem + eine hinzu oder entferne die Regel über ihr ⋯-Menü" },
};

function serve(lang, posts) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
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

async function press(page, loc) {
  const b = await reach(page, loc);
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
}

async function openEditor(page, w) {
  const group = page.locator(".rt-group").first();
  await group.waitFor();
  await press(page, group.locator("button", { hasText: w.edit }));
  const ed = page.locator(".rt-gedit");
  await ed.locator(".rt-rule").first().waitFor();
  return ed;
}

const saves = (posts) => posts.filter((p) => p.path === "/api/groups/save");

// the ⋯ as the reader sees it with the pointer elsewhere: drawn, inside
// the editor, and the topmost thing where it is
async function seen(page, more) {
  await reach(page, more);
  await page.mouse.move(1, 1);
  await page.waitForTimeout(200); // past any fade
  return more.evaluate((b) => {
    const r = b.getBoundingClientRect(), e = b.closest(".rt-gedit").getBoundingClientRect();
    const top = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
    return { opacity: getComputedStyle(b).opacity, visibility: getComputedStyle(b).visibility, inside: r.width > 0 && r.left >= e.left && r.right <= e.right + 1, top: !!top && b.contains(top) };
  });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    for (const width of [1100, 390]) {
      test(`${engine} ${lang} ${width}: a rule's ⋯ is there to remove it, and a rule with no condition says how`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 1400 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=groups");

        // the ⋯ is drawn with the pointer off the rule, and Remove rule
        // under it takes the rule out
        let ed = await openEditor(page, w);
        let row = ed.locator(".rt-rule").first();
        assert.deepEqual(await seen(page, row.locator(".rt-rmore")), { opacity: "1", visibility: "visible", inside: true, top: true }, "the rule's ⋯ is there without hovering");
        await press(page, row.locator(".rt-rmore"));
        await page.locator(".row-menu .rm-item", { hasText: w.remove }).click();
        assert.equal(await ed.locator(".rt-rule").count(), 0);
        await press(page, ed.locator("button.primary", { hasText: w.save }));
        await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
        assert.deepEqual(saves(posts).at(-1).body.rules, []);

        // by keyboard: Tab reaches the ⋯ (Option-Tab in WebKit, which as
        // Safari tabs to fields alone), Enter opens it, Remove rule is the
        // last item
        const TAB = engine === "webkit" ? "Alt+Tab" : "Tab";
        ed = await openEditor(page, w);
        row = ed.locator(".rt-rule").first();
        await row.locator(".rt-model").focus({ preventScroll: true });
        for (let i = 0; i < 6 && !(await row.locator(".rt-rmore").evaluate((b) => b === document.activeElement)); i++) await page.keyboard.press(TAB);
        assert(await row.locator(".rt-rmore").evaluate((b) => b === document.activeElement), "Tab reaches the rule's ⋯");
        await page.keyboard.press("Enter");
        await page.locator(".row-menu").waitFor();
        await page.keyboard.press("ArrowUp");
        assert.equal((await page.evaluate(() => document.activeElement.textContent)).trim(), w.remove);
        await page.keyboard.press("Enter");
        assert.equal(await ed.locator(".rt-rule").count(), 0);
        await press(page, ed.locator("button.primary", { hasText: w.save }));
        await page.waitForFunction(() => !document.querySelector(".rt-gedit"));

        // a rule left with no condition (its "has an image" turned off):
        // Save says so under it and in the status line, with the way out,
        // and saves nothing
        ed = await openEditor(page, w);
        row = ed.locator(".rt-rule").first();
        const n = saves(posts).length;
        await press(page, row.locator('.rt-c[data-k="images"] .rt-cond'));
        assert(await row.locator(".rt-rneed").isHidden(), "nothing said before Save");
        await press(page, ed.locator("button.primary", { hasText: w.save }));
        await page.waitForFunction((s) => document.querySelector("#status")?.textContent === s, w.status);
        row = ed.locator(".rt-rule").first();
        assert.equal(await row.locator(".rt-rneed").textContent(), w.under);
        assert(await row.locator(".rt-rneed").isVisible());
        assert(await row.locator(".rt-addc").evaluate((b) => b === document.activeElement), "the rule's + has the keyboard");
        assert.equal(saves(posts).length, n, "nothing saved");
        // nothing past the editor's side, nor a sideways scroll
        const over = await page.evaluate(() => {
          const e = document.querySelector(".rt-gedit").getBoundingClientRect();
          return [...document.querySelectorAll(".rt-rule, .rt-rule *")].filter((x) => x.offsetParent && x.getBoundingClientRect().right > e.right + 1).map((x) => x.className || x.tagName);
        });
        assert.deepEqual(over, []);
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), "no sideways scroll");
        // its condition on again, the line goes and Save saves it
        await press(page, row.locator('.rt-c[data-k="images"] .rt-cond'));
        assert(await row.locator(".rt-rneed").isHidden(), "said no more once it has a condition");
        await press(page, ed.locator("button.primary", { hasText: w.save }));
        await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
        assert.deepEqual(saves(posts).at(-1).body.rules, [rule("b/flash", { images: true })]);
        assert.deepEqual(errors, []);
      });
    }
  }
}
