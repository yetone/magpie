// Run with Node's test runner and Playwright on the module path; see README.md.
// #907 (Moody-Sin): in a group's Add a model picker, a model in the group
// with its reasoning fixed (a/two:max) was neither ticked nor taken out by
// a click: it was added a second time. PAMI on Discord: with many groups,
// one was hard to find.
// - The picker ticks a model in the group as itself or with an effort, and
//   a click takes that member out (its effort with it), not adds it twice;
//   a model in other groups says them, the whole note on hover.
// - Over the groups, a filter keeps those whose name, id or models match
//   every word typed; the keyboard stays in it as the list is drawn, none
//   matching says so, Escape clears it. Not one <select>, no left border.
// No click or keystroke moves the page. In English and Chinese, Chromium
// and WebKit.
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
];

const words = {
  en: { edit: "Edit", save: "Save", add: "Add another model", in: "in Other, Fast lane", filter: "Filter groups and models", none: "No group or model matches “zzz”" },
  zh: { edit: "编辑", save: "保存", add: "再添加一个模型", in: "已在 Other, Fast lane", filter: "筛选路由组和模型", none: "没有路由组或模型匹配“zzz”" },
};

function serve(lang, posts) {
  const groups = () => ({
    models, pools: [],
    groups: [
      { id: "g", name: "G", members: ["a/one", "a/two:max"], off: [], routing: "order", ready: true, memberInfo: [{ id: "a/one", ready: true }, { id: "a/two:max", ready: true }] },
      { id: "other", name: "Other", members: ["a/two"], off: [], routing: "order", ready: true, memberInfo: [{ id: "a/two", ready: true }] },
      { id: "fast", name: "Fast lane", members: ["a/two:low", "b/three"], off: [], routing: "order", ready: true, memberInfo: [{ id: "a/two:low", ready: true }, { id: "b/three", ready: true }] },
      { id: "auto-one", name: "Found one", members: ["a/one"], auto: true, ready: true, memberInfo: [{ id: "a/one", ready: true }] },
    ],
  });
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
    test(`${engine} ${lang}: a member with its effort ticked and taken out, the groups filtered`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 1400 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=routing");
      const card = (id) => page.locator(`.rt-group[data-id="${id}"]`);
      await card("g").waitFor();
      await page.locator(".rt-gsec").evaluate((x) => x.scrollIntoView({ block: "center" })); // as the reader would
      await page.waitForTimeout(200);
      const at = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => [e.id || e.className, e.scrollTop]).join(";"));
      const before = await at();
      const listed = () => page.locator(".rt-groups .rt-group").evaluateAll((rs) => rs.map((r) => r.dataset.id));

      // the filter: by name, by a model's name or id, every word
      const q = page.locator(".rt-gsec .row-head input.rt-gfilter");
      assert.equal(await q.getAttribute("placeholder"), w.filter);
      assert.equal(await q.getAttribute("aria-label"), w.filter);
      await q.click();
      await page.keyboard.type("three");
      assert.deepEqual(await listed(), ["fast"], "by a model's name");
      assert.equal(await page.evaluate(() => document.activeElement?.classList.contains("rt-gfilter")), true, "the keyboard left the filter");
      await q.fill("a/two other");
      assert.deepEqual(await listed(), ["other"], "every word, a model's id");
      await q.fill("found");
      assert.deepEqual(await listed(), ["auto-one"], "by name");
      await q.fill("zzz");
      assert.deepEqual(await listed(), []);
      assert.equal((await page.locator(".rt-groups .rt-gnone").textContent()).trim(), w.none);
      await q.focus();
      await page.keyboard.press("Escape");
      assert.equal(await q.inputValue(), "");
      assert.deepEqual(await listed(), ["g", "other", "fast", "auto-one"]);
      const border = await q.evaluate((x) => { const s = getComputedStyle(x); return [s.borderLeftWidth, s.borderLeftColor] + "|" + [s.borderRightWidth, s.borderRightColor]; });
      assert.equal(border.split("|")[0], border.split("|")[1], "the filter has a left border of its own");
      assert.equal(await at(), before, "the filter moved the page");

      // the picker: a/two is in G as a/two:max
      await card("g").locator("button", { hasText: w.edit }).click();
      const ed = page.locator(".rt-gedit");
      await ed.locator("button.rt-gadd", { hasText: w.add }).click();
      const pop = page.locator("#pop");
      const opt = (name) => pop.locator("li", { has: page.locator(".v", { hasText: new RegExp(`^${name}$`) }) }).first();
      await opt("two").waitFor();
      const cur = (name) => opt(name).evaluate((li) => li.classList.contains("cur"));
      assert.equal(await cur("two"), true, "a member with its effort is not ticked");
      assert.equal(await cur("one"), true);
      assert.equal(await cur("three"), false);
      const note = opt("two").locator(".n");
      assert((await note.textContent()).includes(w.in), "the picker doesn't say the other groups two is in");
      assert.equal(await note.getAttribute("title"), await note.textContent(), "the note has no tooltip");
      const rows = () => ed.locator(".fbl").first().locator(".fbrow").count();
      assert.equal(await rows(), 2);
      await opt("two").click();
      assert.equal(await rows(), 1, "clicked, it was not taken out");
      assert.equal(await cur("two"), false);
      await opt("two").click();
      assert.equal(await rows(), 2);
      assert.equal(await cur("two"), true);
      await page.keyboard.press("Escape");
      await pop.waitFor({ state: "hidden" });

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
      assert.deepEqual(sent.body.members, ["a/one", "a/two"], "a member added twice, or its effort kept");

      assert.equal(await page.locator("select").count(), 0, "a native select");
      const missing = await page.evaluate(() => ["Filter groups and models", "No group or model matches “{q}”"]
        .filter((k) => !I18N.zh[k] || !I18N.ja[k] || !I18N.de[k]));
      assert.deepEqual(missing, [], "every string has its Chinese, Japanese and German");
      assert.deepEqual(errors, []);
    });
  }
}
