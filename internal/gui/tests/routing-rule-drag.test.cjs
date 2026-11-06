// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group's rules are put in order by dragging (monkeyin_92 on X:
// "I'd like to drag rules up and down"), as the group's models are. In the
// editor a rule's number is its handle, with a grip beside it drawn only
// while the rule is under the pointer; dragging it moves the rule, Alt+↑/↓
// moves it from the keyboard, which stays on its handle, and the rule's ⋯
// keeps Move up and Move down. Saved, the rules go in the new order. No
// click or drag moves the page. Chromium and WebKit, every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.ASSETS || path.join(__dirname, "../assets"));
const models = [
  { id: "a/one", name: "one", providerName: "A", icon: "generic", context: 1000000, ready: true },
  { id: "a/two", name: "two", providerName: "A", icon: "generic", context: 1000000, ready: true },
  { id: "b/three", name: "three", providerName: "B", icon: "generic", context: 1000000, ready: true },
];
const members = models.map((m) => m.id);
const groups = () => ({
  models, deciders: [], pools: [],
  groups: [{ id: "g", name: "G", members, routing: "order", ready: true,
    rules: [{ use: "a/one", images: true }, { use: "a/two", tokens: 5000 }, { use: "b/three", agents: ["codex"] }],
    memberInfo: members.map((id) => ({ id, ready: true, context: 1000000, images: true })) }],
});
const words = {
  en: { edit: "Edit", save: "Save", move: "Move rule 3", down: "Move down", tip: "Drag to reorder · Alt+↑/↓ to move" },
  zh: { edit: "编辑", save: "保存", move: "移动规则 3", down: "下移" },
  "zh-TW": { edit: "編輯", save: "儲存", move: "移動規則 3", down: "下移" },
  ja: { edit: "編集", save: "保存", move: "ルール 3 を移動", down: "下へ移動" },
  de: { edit: "Bearbeiten", save: "Speichern", move: "Regel 3 verschieben", down: "Nach unten" },
};

function serve(lang, posts) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
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

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    test(`${engine} ${lang}: a group's rules are dragged into order`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 1400 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=groups");
      const card = page.locator(".rt-group").first();
      await card.waitFor();
      await card.locator("button", { hasText: w.edit }).click();
      const ed = page.locator(".rt-gedit");
      await ed.locator(".rt-rule").first().waitFor();
      const order = () => ed.locator(".rt-rule .rt-then .rt-model").evaluateAll((bs) => bs.map((b) => b.textContent.split(" · ")[0].trim()));
      const rule = (name) => ed.locator(".rt-rule", { has: page.locator(".rt-then .rt-model", { hasText: name }) });
      const handle = (name) => rule(name).locator(".rt-rhandle");

      // every rule's number is its handle
      assert.deepEqual(await ed.locator(".rt-rule .rt-rhandle").allTextContents(), ["1", "2", "3"]);
      assert.equal(await handle("three").getAttribute("aria-label"), w.move);
      if (w.tip) assert.equal(await handle("three").getAttribute("title"), w.tip);
      // the grip shows on the rule under the pointer only
      await page.mouse.move(1, 1);
      await page.waitForFunction(() => [...document.querySelectorAll(".rt-rhandle")].map((h) => getComputedStyle(h, "::before").opacity).join() === "0,0,0");
      await rule("two").locator(".rt-then").hover();
      await page.waitForFunction(() => [...document.querySelectorAll(".rt-rhandle")].map((h) => getComputedStyle(h, "::before").opacity).join() === "0,1,0");

      // wheeled to the rules, as the reader would (a scroll no reader asked
      // for is put back)
      const v = await page.locator("#view-groups").boundingBox();
      await page.mouse.move(v.x + 10, 300);
      for (let n = 0; n < 40; n++) {
        const b = await ed.locator(".rt-rules").boundingBox();
        const d = Math.round(b.y + b.height / 2 - 700);
        if (Math.abs(d) < 120) break;
        await page.mouse.wheel(0, Math.sign(d) * Math.min(Math.abs(d), 300));
        await page.waitForTimeout(60);
      }
      await page.waitForTimeout(200);
      const at = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => [e.id || e.className, e.scrollTop]).join(";") + "|" + Math.round(document.querySelector(".rt-gedit").getBoundingClientRect().top));
      const before = await at();
      // drag "three" above "one"
      const hb = await handle("three").boundingBox(), top = await rule("one").boundingBox();
      await page.mouse.move(hb.x + hb.width / 2, hb.y + hb.height / 2);
      await page.mouse.down();
      for (let i = 1; i <= 8; i++) await page.mouse.move(hb.x + hb.width / 2, hb.y + hb.height / 2 - ((hb.y - top.y + 6) * i) / 8);
      await page.mouse.up();
      assert.deepEqual(await order(), ["three", "one", "two"], "the drag moved it");
      assert.equal(await page.locator(".pop:not([hidden]), .row-menu").count(), 0, "the drag's click opened something");
      assert.deepEqual(await ed.locator(".rt-rule .rt-rhandle").allTextContents(), ["1", "2", "3"], "numbered again");

      // Alt+↓ from the keyboard, which stays on the moved rule's handle
      await handle("three").focus();
      await page.keyboard.press("Alt+ArrowDown");
      assert.deepEqual(await order(), ["one", "three", "two"]);
      assert(await handle("three").evaluate((h) => h === document.activeElement), "the keyboard left the moved rule");
      await page.keyboard.press("Alt+ArrowDown");
      await page.keyboard.press("Alt+ArrowDown"); // past the last: stays
      assert.deepEqual(await order(), ["one", "two", "three"]);
      await page.keyboard.press("Alt+ArrowUp");
      assert.deepEqual(await order(), ["one", "three", "two"]);
      // the ⋯'s Move down still moves it a step
      await rule("one").locator(".rt-rmore").click();
      await page.locator(".row-menu .rm-item", { hasText: w.down }).click();
      assert.deepEqual(await order(), ["three", "one", "two"]);
      assert.equal(await at(), before, "a click or drag moved the page");

      const b = ed.locator("button.primary", { hasText: w.save });
      await page.mouse.move(550, 600);
      for (let i = 0; i < 10 && (await b.boundingBox()).y > 1400 - 160; i++) {
        await page.mouse.wheel(0, 300);
        await page.waitForTimeout(150);
      }
      const bb = await b.boundingBox();
      await page.mouse.click(bb.x + bb.width / 2, bb.y + bb.height / 2);
      await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
      const sent = posts.find((p) => p.path === "/api/groups/save");
      assert.deepEqual(sent.body.rules.map((r) => r.use), ["b/three", "a/one", "a/two"]);
      assert.deepEqual(sent.body.rules[0].agents, ["codex"], "a rule moves with its conditions");
      assert.deepEqual(errors, []);
    });
  }
}
