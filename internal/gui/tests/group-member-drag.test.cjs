// Run with Node's test runner and Playwright on the module path; see README.md.
// A group's models are put in order by dragging (PAMI on Discord: only Up,
// one step a click). In the editor a named member's number is its handle,
// with a grip beside it drawn only while the row is under the pointer;
// dragging it moves the model, Alt+↑/↓ moves it from the keyboard, which
// stays on it, and Up and Down stay beside it. A pattern's members are
// moved the same way (usrname1833 on Discord), among the named ones too.
// Saved, the order of them all goes as placed. No click
// or drag moves the page. In English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/one", name: "one", providerName: "A", icon: "generic" },
  { id: "a/two", name: "two", providerName: "A", icon: "generic" },
  { id: "b/three", name: "three", providerName: "B", icon: "generic" },
  { id: "c/free:free", name: "free:free", providerName: "C", icon: "generic" },
];
const members = ["a/one", "a/two", "b/three", "c/free:free"];
const groups = () => ({
  models, pools: [],
  groups: [{ id: "g", name: "G", members, match: ["c/*:free"], matched: ["c/free:free"], off: [], routing: "order", ready: true,
    memberInfo: members.map((id) => ({ id, ready: true })) }],
});

const words = {
  en: { edit: "Edit", save: "Save", up: "Up", down: "Down", move: "Move two" },
  zh: { edit: "编辑", save: "保存", up: "上移", down: "下移", move: "移动 two" },
};

function serve(lang, posts) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
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
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a group's models are dragged into order`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 1400 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=groups");
      const card = page.locator(".rt-group", { hasText: "G" }).first();
      await card.waitFor();
      await card.locator("button", { hasText: w.edit }).click();
      const ed = page.locator(".rt-gedit");
      await ed.locator(".fbrow").first().waitFor();
      const order = () => ed.locator(".fbl").first().locator(".fbrow .n > span:first-child").allTextContents();
      const row = (name) => ed.locator(".fbrow", { has: page.locator(".n > span:first-child", { hasText: new RegExp(`^${name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`) }) });
      const handle = (name) => row(name).locator(".rt-mhandle");

      // every member has a handle with its number, a pattern's too
      assert.deepEqual(await ed.locator(".fbrow .rt-mhandle").allTextContents(), ["1", "2", "3", "4"]);
      assert.equal(await handle("two").getAttribute("aria-label"), w.move);
      // Up on all but the first, Down on all but the last
      const btns = async (name) => row(name).locator("button.text").allTextContents();
      assert(!(await btns("one")).includes(w.up) && (await btns("one")).includes(w.down));
      assert((await btns("three")).includes(w.up) && (await btns("three")).includes(w.down));
      assert((await btns("free:free")).includes(w.up) && !(await btns("free:free")).includes(w.down));

      // the grip shows on the row under the pointer only
      await page.mouse.move(1, 1);
      const grip = (name) => handle(name).evaluate((h) => getComputedStyle(h, "::before").opacity);
      assert.equal(await grip("two"), "0");
      await row("two").locator(".n").hover();
      await page.waitForFunction(() => [...document.querySelectorAll(".rt-mhandle")].map((h) => getComputedStyle(h, "::before").opacity).join() === "0,1,0,0");

      await ed.evaluate((x) => x.scrollIntoView({ block: "center" })); // by the test, as the reader would
      await page.waitForTimeout(200);
      const at = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => [e.id || e.className, e.scrollTop]).join(";") + "|" + Math.round(document.querySelector(".rt-gedit").getBoundingClientRect().top));
      const before = await at();

      // drag "three" above "one"
      const hb = await handle("three").boundingBox(), top = await row("one").boundingBox();
      await page.mouse.move(hb.x + hb.width / 2, hb.y + hb.height / 2);
      await page.mouse.down();
      for (let i = 1; i <= 8; i++) await page.mouse.move(hb.x + hb.width / 2, hb.y + hb.height / 2 - ((hb.y - top.y + 6) * i) / 8);
      await page.mouse.up();
      assert.deepEqual(await order(), ["three", "one", "two", "free:free"], "the drag moved it");
      assert.equal(await page.locator(".pop:not([hidden])").count(), 0, "the drag's click opened something");

      // Alt+↓ from the keyboard, which stays on the handle
      await handle("one").focus();
      await page.keyboard.press("Alt+ArrowDown");
      assert.deepEqual(await order(), ["three", "two", "one", "free:free"]);
      assert.equal(await page.evaluate(() => document.activeElement?.closest(".fbrow")?.querySelector(".n > span")?.textContent), "one", "the keyboard left the moved model");
      // past a pattern's too
      await page.keyboard.press("Alt+ArrowDown");
      assert.deepEqual(await order(), ["three", "two", "free:free", "one"]);
      // Down and Up still move it a step
      await row("three").locator("button.text", { hasText: w.down }).click();
      assert.deepEqual(await order(), ["two", "three", "free:free", "one"]);
      await row("one").locator("button.text", { hasText: w.up }).click();
      assert.deepEqual(await order(), ["two", "three", "one", "free:free"]);
      // a pattern's dragged to the top
      const hf = await handle("free:free").boundingBox(), first = await row("two").boundingBox();
      await page.mouse.move(hf.x + hf.width / 2, hf.y + hf.height / 2);
      await page.mouse.down();
      for (let i = 1; i <= 8; i++) await page.mouse.move(hf.x + hf.width / 2, hf.y + hf.height / 2 - ((hf.y - first.y + 6) * i) / 8);
      await page.mouse.up();
      assert.deepEqual(await order(), ["free:free", "two", "three", "one"], "a pattern's was dragged");
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
      assert.deepEqual(sent.body.members, ["a/two", "b/three", "a/one"]);
      assert.deepEqual(sent.body.placed, ["c/free:free", "a/two", "b/three", "a/one"], "the order, a pattern's among them");

      const missing = await page.evaluate(() => ["Down", "Move {name}", "Drag to reorder · Alt+↑/↓ to move"].filter((k) => !I18N.zh[k] || !I18N.ja[k] || !I18N.de[k]));
      assert.deepEqual(missing, [], "every string has its Chinese, Japanese and German");
      assert.deepEqual(errors, []);
    });
  }
}
