// Run with Node's test runner and Playwright on the module path; see README.md.
// lc on Discord: a routing group can be duplicated. Its row's menu (right-
// click, or a click on its logos) has Duplicate under Move up and Move
// down, which posts groups/copy with the group's id and "<name> copy" in
// the reader's language; the list is drawn as the answer has it, the copy
// after the group it copies, and the status says it was duplicated. No
// click moves the page or opens the editor. At 420px, in every language,
// Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/m", name: "m", providerName: "A", icon: "generic" },
  { id: "a/x", name: "x", providerName: "A", icon: "generic" },
];
const info = (ids) => ids.map((id) => ({ id, ready: true }));
const group = (id, name, members, more = {}) => ({ id, name, members, ready: true, memberInfo: info(members), ...more });

const words = {
  en: { dup: "Duplicate", copy: "One copy", done: "Duplicated One" },
  zh: { dup: "复制", copy: "One 副本", done: "已复制 One" },
  "zh-TW": { dup: "複製", copy: "One 副本", done: "已複製 One" },
  ja: { dup: "複製", copy: "One のコピー", done: "One を複製しました" },
  de: { dup: "Duplizieren", copy: "One Kopie", done: "One dupliziert" },
};
const KEYS = ["Duplicate", "{name} copy", "Duplicated {name}", "A copy of {name} with its models, routing and rules, listed after it"];

function serve(lang, posts) {
  let list = [
    group("one", "One", ["a/x", "a/m"], { routing: "order", rules: [{ use: "a/m", agents: ["codex"] }] }),
    group("two", "Two", ["a/m"]),
  ];
  const groups = () => ({ models, pools: [], deciders: [], found: true, groups: list });
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname === "/api/groups/copy") {
      const body = JSON.parse(r.request().postData() || "{}");
      posts.push(body);
      const src = list.find((g) => g.id === body.id);
      const copy = { ...structuredClone(src), id: "one-copy", name: body.name };
      list = [list[0], copy, ...list.slice(1)];
      return json(groups());
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const listed = (page) => page.locator(".rt-groups > .rt-group").evaluateAll((rs) => rs.map((r) => r.dataset.id));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    test(`${engine} ${lang}: a routing group is duplicated from its menu`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 420, height: 1400 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-group-duplicate.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      // WebKit reports a ResizeObserver round left to the next frame as a page
      // error; Chromium doesn't. As the page first lays out, the view's
      // scrollbar comes and goes in one frame (#rt 362 -> 352 -> 362), so the
      // width marks and the stage are told twice, past the loop's depth. The
      // round is delivered on the next frame and nothing is painted wrong.
      page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=groups");
      await page.locator(".rt-groups > .rt-group").nth(1).waitFor();
      // tall enough that the rows are in view unscrolled (1400: German
      // with the found groups' "leave out" line needs more than 1200): a scroll closes
      // the menu, and the page settles its own scroll as it loads
      await page.waitForTimeout(500);
      assert.deepEqual(await listed(page), ["one", "two"]);
      const at = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => [e.id || e.className, e.scrollTop]).join(";") + "|" + scrollX);
      const before = await at();

      await page.locator(".rt-group[data-id=one] .main").click({ button: "right" });
      const menu = page.locator(".row-menu");
      await menu.waitFor();
      const item = menu.getByRole("menuitem", { name: w.dup });
      assert.equal(await item.isDisabled(), false);
      // the menu is whole on a narrow window
      const box = await menu.boundingBox();
      assert(box.x >= 0 && box.x + box.width <= 420, `menu off the window: ${JSON.stringify(box)}`);
      await item.click();
      await page.waitForFunction((m) => document.querySelector("#status").textContent.includes(m), w.done);
      assert.deepEqual(posts, [{ id: "one", name: w.copy }]);
      await page.waitForFunction(() => [...document.querySelectorAll(".rt-groups > .rt-group")].map((r) => r.dataset.id).join() === "one,one-copy,two");
      assert.equal(await page.locator(".rt-group[data-id=one-copy] .main").textContent().then((s) => s.includes(w.copy)), true);
      assert.equal(await page.locator(".rt-gedit").count(), 0, "duplicating opened the editor");
      assert.equal(await page.locator(".row-menu").count(), 0, "the menu stayed open");

      // the handle's click opens the same menu
      await page.mouse.move(1, 1);
      await page.locator(".rt-group[data-id=one-copy] .rt-ghandle").click();
      await menu.waitFor();
      assert.equal(await menu.getByRole("menuitem", { name: w.dup }).count(), 1);
      await page.keyboard.press("Escape");

      assert.equal(await at(), before, "a click moved the page");
      if (lang !== "en") {
        const missing = await page.evaluate(([l, ks]) => ks.filter((k) => !I18N[l][k]), [lang, KEYS]);
        assert.deepEqual(missing, [], `every string has its ${lang}`);
      }
      assert.deepEqual(errors, []);
    });
  }
}
