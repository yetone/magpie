// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group's rule reads as one line (the owner, 10-14: every rule
// showed all ten conditions, empty, "用户要输入、填写这么多东西，太恐怖了"):
// the conditions it has, → and the model. A rule with only an intent shows
// that intent and nothing empty; a rule with every condition shows each,
// in the rule's order; a condition is added from the rule's + in the app's
// menu (never a native select) and taken off with its ×; the ⋯ moves a rule.
// Saved untouched, the rules are what was loaded, field for field (run with
// ASSETS set to the assets before this change to see the old editor save
// the same). In every language, at a wide and a phone's width, nothing past
// the editor's side.
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
const WEEKDAYS = ["mon", "tue", "wed", "thu", "fri"];
const rule = (use, more) => ({ use, pause: false, tokens: 0, images: false, effort: "", agents: [], intent: "", compact: false, folders: [], exceptFolders: [], time: null, ...more });
// as the group's rules are loaded, and so as they are saved untouched
const RULES = [
  rule("b/flash", { intent: "a quick question" }),
  rule("a/opus", { tokens: 50000, images: true, effort: "high", agents: ["claude"], intent: "code review", compact: true, folders: ["~/work/acme", "~/oss"], exceptFolders: ["~/big-task"], time: { from: "09:00", to: "18:00", days: WEEKDAYS } }),
  rule("a/opus", { pause: true, time: { from: "14:00", to: "18:00", days: [] } }),
];
const groups = () => ({
  models,
  deciders: [{ id: "typesafe/jev-latest", name: "Jev", providerName: "TypeSafe", icon: "generic" }],
  groups: [{ id: "main", name: "Main", members: ["a/opus", "b/flash"], routing: "order", ready: true, classifier: "typesafe/jev-latest",
    rules: structuredClone(RULES), memberInfo: [member("a/opus"), member("b/flash")] }],
  pools: [],
});
// the order a rule reads, as the editor draws its conditions
const ORDER = ["intent", "tokens", "images", "effort", "compact", "agents", "folders", "exceptFolders", "time"];
// the conditions a rule may ask besides (aipha2025 on X), which the + still offers
const BESIDES = ["subagent", "message"];

const words = {
  en: { edit: "Edit", save: "Save", image: "has an image", down: "Move down" },
  zh: { edit: "编辑", save: "保存", image: "带图片", down: "下移" },
  "zh-TW": { edit: "編輯", save: "儲存", image: "帶圖片", down: "下移" },
  ja: { edit: "編集", save: "保存", image: "画像あり", down: "下へ移動" },
  de: { edit: "Bearbeiten", save: "Speichern", image: "enthält ein Bild", down: "Nach unten" },
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
  await ed.locator(".rt-rule").first().waitFor();
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
      test(`${engine} ${lang} ${width}: a rule reads as one line, its conditions added from a menu`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 1400 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=groups");

        // saved untouched: the rules as loaded, field for field
        let ed = await openEditor(page, w);
        const body = await saved(page, ed, w, posts);
        assert.deepEqual(body.rules, RULES);
        assert.equal(body.classifier, "typesafe/jev-latest");
        if (process.env.ASSETS) return; // the old editor: only the round trip is asked of it

        ed = await openEditor(page, w);
        const rows = ed.locator(".rt-rule");
        const kinds = (row) => row.locator(".rt-c").evaluateAll((xs) => xs.map((x) => x.dataset.k));
        // an intent alone: the intent, the model, nothing empty
        const r1 = rows.nth(0);
        assert.deepEqual(await kinds(r1), ["intent"]);
        assert.equal(await r1.locator(".rt-cond.in textarea").inputValue(), "a quick question");
        const chips1 = await r1.locator(".rt-cond").evaluateAll((xs) => xs.map((x) => x.className));
        assert.deepEqual(chips1, ["rt-cond in on", "rt-cond on rt-model"], "no empty chip beside the intent");
        assert.equal(await r1.locator(".rt-act").count(), 0, "sending goes unsaid");
        // every condition of its own, each set, in the rule's order; the
        // + left for the ones a rule may ask besides
        const r2 = rows.nth(1);
        assert.deepEqual(await kinds(r2), ORDER);
        assert.equal(await r2.locator(".rt-addc").count(), 1);
        for (const c of [".tk", ".in", ".fd", ".xf", ".tm"]) assert.match(await r2.locator(".rt-cond" + c).getAttribute("class"), /\bon\b/, c);
        // a pause: its hours, and says it pauses
        const r3 = rows.nth(2);
        assert.deepEqual(await kinds(r3), ["time"]);
        assert.equal(await r3.locator(".rt-act").count(), 1);

        // nothing past the editor's side, nor a sideways scroll
        const over = await page.evaluate(() => {
          const e = document.querySelector(".rt-gedit").getBoundingClientRect();
          return [...document.querySelectorAll(".rt-rule, .rt-rule *")].filter((x) => x.offsetParent && x.getBoundingClientRect().right > e.right + 1).map((x) => x.className || x.tagName);
        });
        assert.deepEqual(over, []);
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), "no sideways scroll");
        // a field is set in its line's size, a phone's page too (where a
        // page's own boxes are 16px): the intent, tokens, folders, hours
        const sizes = await page.evaluate(() => [...document.querySelectorAll(".rt-rule .rt-cond input, .rt-rule .rt-cond textarea")]
          .filter((x) => getComputedStyle(x).fontSize !== getComputedStyle(x.closest(".rt-cond")).fontSize)
          .map((x) => `${x.closest(".rt-cond").className} ${x.tagName} ${getComputedStyle(x).fontSize}`));
        assert.deepEqual(sizes, [], "every field in its line's size");

        // a condition added from the rule's + menu: the app's, the ones
        // the rule hasn't, and the pick shows set
        await press(page, r1.locator(".rt-addc"));
        assert.equal(await page.locator("select").count(), 0, "the app menu, not a native select");
        const items = page.locator(".proto-menu .pm-item");
        assert.equal(await items.count(), ORDER.length - 1 + BESIDES.length, "all but the intent it has");
        await items.filter({ hasText: w.image }).click();
        assert.deepEqual(await kinds(r1), ["intent", "images"]);
        assert.equal(await r1.locator('.rt-c[data-k="images"] .rt-cond').getAttribute("class"), "rt-cond on");
        // and one taken off with its ×
        const c = await reach(page, r2.locator('.rt-c[data-k="agents"]'));
        await page.mouse.move(c.x + c.width / 2, c.y + c.height / 2);
        await press(page, r2.locator('.rt-c[data-k="agents"] .rt-cx'));
        assert.equal(await r2.locator('.rt-c[data-k="agents"]').count(), 0);
        // the ⋯ moves a rule
        await press(page, r1.locator(".rt-rmore"));
        await page.locator(".row-menu .rm-item", { hasText: w.down }).click();
        assert.deepEqual(await kinds(rows.nth(1)), ["intent", "images"]);

        const body2 = await saved(page, ed, w, posts);
        assert.deepEqual(body2.rules, [{ ...RULES[1], agents: [] }, { ...RULES[0], images: true }, RULES[2]]);
        assert.deepEqual(errors, []);
      });
    }
  }
}
