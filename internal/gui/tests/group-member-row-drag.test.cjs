// Run with Node's test runner and Playwright on the module path; see README.md.
// A group's models are dragged into order from the row itself, not only
// from its number (NovaStudiosss on X: the number went unfound, so Up and
// Down were all there was). A press on the row's logo or the room around
// its words picks the model up; one on its name still selects the words,
// and its controls stay controls. The row lets a finger scroll the page
// (touch-action pan-y) while the number takes a finger's drag. Saved, the
// order goes as placed. No drag moves the page. In every language, wide
// and narrow, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));
const models = [
  { id: "a/one", name: "one", providerName: "A", icon: "generic" },
  { id: "a/two", name: "two", providerName: "A", icon: "generic" },
  { id: "b/three", name: "three", providerName: "B", icon: "generic" },
];
const members = ["a/one", "a/two", "b/three"];
const groups = () => ({
  models, pools: [],
  groups: [{ id: "g", name: "G", members, off: [], routing: "order", ready: true,
    memberInfo: members.map((id) => ({ id, ready: true })) }],
});

const words = {
  en: { edit: "Edit", save: "Save" },
  zh: { edit: "编辑", save: "保存" },
  "zh-TW": { edit: "編輯", save: "儲存" },
  ja: { edit: "編集", save: "保存" },
  de: { edit: "Bearbeiten", save: "Speichern" },
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
  for (const lang of Object.keys(words)) {
    for (const width of [1100, 420]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: a group's model is dragged by its row`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 1000 }, reducedMotion: "reduce" })).newPage();
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
        const list = ed.locator(".fbl").first();
        const order = () => list.locator(".fbrow .n > span:first-child").allTextContents();
        const row = (name) => list.locator(".fbrow", { has: page.locator(".n > span:first-child", { hasText: new RegExp(`^${name}$`) }) });
        await ed.evaluate((x) => x.scrollIntoView({ block: "center" })); // by the test, as the reader would
        await page.waitForTimeout(200);
        const at = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => [e.id || e.className, e.scrollTop]).join(";") + "|" + Math.round(document.querySelector(".rt-gedit").getBoundingClientRect().top));
        const before = await at();
        // a press at (x, y) dragged to the top of the row named `over`
        const drag = async (x, y, over) => {
          const top = await row(over).boundingBox();
          await page.mouse.move(x, y);
          await page.mouse.down();
          for (let i = 1; i <= 8; i++) await page.mouse.move(x, y - ((y - top.y + 6) * i) / 8);
          await page.mouse.up();
        };

        // the row says it can be picked up; a finger on it still scrolls,
        // and the number takes a finger's drag
        const look = (sel) => row("two").locator(sel).first().evaluate((e) => { const s = getComputedStyle(e); return s.cursor + " " + s.touchAction; });
        assert.equal(await look(":scope"), "grab pan-y");
        assert.match(await look(".rt-mhandle"), /none$/);
        assert.match(await look(".n > span"), /^auto /, "the name keeps the text cursor");

        // by its logo: "three" to the top
        const ib = await row("three").locator(".rt-mhandle + *").boundingBox();
        await drag(ib.x + ib.width / 2, ib.y + ib.height / 2, "one");
        assert.deepEqual(await order(), ["three", "one", "two"], "the logo picked it up");
        assert.equal(await page.locator(".pop:not([hidden])").count(), 0, "the drag's click opened something");

        // by the room in the row, right of its words: "two" to the top
        const nb = await row("two").locator(".n > span:first-child").boundingBox(), rb = await row("two").boundingBox();
        const room = await row("two").evaluate((r, x0) => {
          // the first point right of the name that is the row's own, or its .n's
          for (let x = Math.ceil(x0) + 4; x < r.getBoundingClientRect().right; x += 2) {
            const y = r.querySelector(".n > span").getBoundingClientRect();
            const hit = document.elementFromPoint(x, y.top + y.height / 2);
            if (hit === r || hit === r.querySelector(".n") || hit?.classList.contains("grow")) return [x, y.top + y.height / 2];
          }
          return null;
        }, nb.x + nb.width);
        assert(room, `no room in the row (${JSON.stringify(rb)})`);
        await drag(room[0], room[1], "three");
        assert.deepEqual(await order(), ["two", "three", "one"], "the room in the row picked it up");

        // a press on the name selects its words, and moves nothing
        const name = await row("one").locator(".n > span:first-child").boundingBox();
        await drag(name.x + 2, name.y + name.height / 2, "two");
        assert.deepEqual(await order(), ["two", "three", "one"], "a press on the name moved the model");

        assert.equal(await at(), before, "a drag moved the page");

        const b = ed.locator("button.primary", { hasText: w.save });
        await b.scrollIntoViewIfNeeded();
        await b.click();
        await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
        const sent = posts.find((p) => p.path === "/api/groups/save");
        assert.deepEqual(sent.body.members, ["a/two", "b/three", "a/one"], "saved as placed");
        assert.deepEqual(errors, []);
      });
    }
  }
}
