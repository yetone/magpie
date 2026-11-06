// Run with Node's test runner and Playwright on the module path; see README.md.
// issennknight on Discord: Import of a shared routing group whose id is a
// group of the user's own replaces that group instead of adding another.
// The box says so before it is pressed (the button reads Replace <name>,
// a line says what stays), a group magpie found or an id nobody has is
// still added, and only a replacing import posts replace. At 420px, in
// every language, Chromium and WebKit.
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
  en: { imp: "Import", rep: "Replace One", hint: "One has the same id", done: "Replaced One" },
  zh: { imp: "导入", rep: "替换 One", hint: "One 的 id 相同", done: "已替换 One" },
  "zh-TW": { imp: "匯入", rep: "取代 One", hint: "One 的 id 相同", done: "已取代 One" },
  ja: { imp: "インポート", rep: "One を置き換える", hint: "One と ID が同じ", done: "One を置き換えました" },
  de: { imp: "Importieren", rep: "One ersetzen", hint: "One hat dieselbe ID", done: "One ersetzt" },
};
const shared = (id) => JSON.stringify({ magpie: "routing-group", version: 1, group: { id, name: "One", members: ["a/x"] } });
function serve(lang, posts) {
  let list = [
    group("one", "One", ["a/x", "a/m"], { routing: "order" }),
    group("two", "Two", ["a/m"]),
    group("found", "Found", ["a/m"], { auto: true }),
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
    if (url.pathname === "/api/copy") {
      posts.push(["copy", JSON.parse(r.request().postData() || "{}").text]);
      return json({});
    }
    if (url.pathname === "/api/groups/import") {
      const { text } = JSON.parse(r.request().postData() || "{}");
      posts.push(["import", text, JSON.parse(r.request().postData()).replace]);
      if (!text.includes("routing-group")) return r.fulfill({ status: 400, json: { error: "this is not a routing group shared from magpie", code: "share", share: "not_shared", format: "0", models: "" } });
      const replace = JSON.parse(r.request().postData()).replace;
      if (replace) return json({ ...groups(), imported: { id: "one", name: "One", replaced: true } });
      list = [...list, group("new", "New", ["a/x"])];
      return json({ ...groups(), imported: { id: "new", name: "New" } });
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
    test(`${engine} ${lang}: a pasted group whose id is a group of the user's replaces it`, async () => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      try {
        const page = await (await browser.newContext({ viewport: { width: 420, height: 1800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=groups");
        await page.locator(".rt-groups > .rt-group").nth(1).waitFor();
        await page.waitForTimeout(500);
        await page.locator(".rt-gimportbtn").click();
        const text = page.locator(".rt-gimport-text"), go = page.locator(".rt-gimport-go"), over = page.locator(".rt-gimport-over");
        await text.waitFor();
        assert.equal((await go.textContent()).trim(), w.imp);
        assert(await over.isHidden(), "a hint with nothing pasted");

        // an id no group of the user's has, or one magpie found: Import
        for (const id of ["nope", "found"]) {
          await text.fill(shared(id));
          assert.equal((await go.textContent()).trim(), w.imp, id);
          assert(await over.isHidden(), id);
        }
        // the id of the user's own group: says which it replaces, before
        await text.fill("```\n" + shared("One") + "\n```");
        assert.equal((await go.textContent()).trim(), w.rep);
        assert((await over.textContent()).includes(w.hint), await over.textContent());
        const ob = await over.boundingBox(), gb = await go.boundingBox();
        assert(ob.x >= 0 && ob.x + ob.width <= 420 && gb.x + gb.width <= 420, "off the window");
        await page.locator(".rt-gfilter").fill("o");
        await page.locator(".rt-gfilter").fill("");
        assert.equal((await go.textContent()).trim(), w.rep, "the redraw lost the label");
        await go.click();
        await page.locator(".rt-gimport-done").waitFor();
        assert.equal((await page.locator(".rt-gimport-done").textContent()).trim(), w.done);
        assert.equal(posts.length, 1);
        assert.equal(posts[0][2], true, "replace not asked");
        assert.deepEqual(await listed(page), ["one", "two", "found"]);

        // an id nobody has: added as before, no replace asked
        await page.locator(".rt-gimport").getByRole("button").last().click();
        await page.locator(".rt-gimportbtn").click();
        await text.fill(shared("nope"));
        await go.click();
        await page.locator(".rt-gimport-done").waitFor();
        assert.equal(posts[1][2], undefined, "replace asked for an id nobody has");
        assert.deepEqual(await listed(page), ["one", "two", "found", "new"]);
        if (lang !== "en") {
          const keys = ["Replace {name}", "{name} has the same id: its models, routing and rules are replaced by this group's; its accounts and folder rules stay", "Replaced {name}"];
          const missing = await page.evaluate(([l, ks]) => ks.filter((k) => !I18N[l][k]), [lang, keys]);
          assert.deepEqual(missing, [], `every string has its ${lang}`);
        }
        assert.deepEqual(errors, []);
      } finally {
        await browser.close();
      }
    });
  }
}
