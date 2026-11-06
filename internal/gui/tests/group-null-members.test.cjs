// Run with Node's test runner and Playwright on the module path; see README.md.
// #1585 (dbydd): the Routing page showed only "null is not an object
// (evaluating 'g.members.map')" and no group at all, while every group still
// routed. A group made of patterns only whose models a plugin behind on
// updates no longer lists came from /api/groups with "members": null (the
// group below is that response's bytes). Now that group is drawn as one
// whose pattern matches nothing and no member is ready, every other group
// is drawn as ever, and the filter and the editor work. A group that still
// can't be drawn is one row saying so, never the whole list. The Gateway
// page's model list survives a group with null lists too. Both engines, en
// and zh, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/m1", name: "m1", provider: "a", pname: "Acme", icon: "generic" },
  { id: "b/m2", name: "m2", provider: "b", pname: "Beta", icon: "generic" },
];
const info = (id) => ({ id, ready: true, provider: id.split("/")[0], name: id.split("/")[0], model: id.split("/")[1], on: 1, of: id });
const groups = () => ({
  models, pools: [], deciders: [], found: true, templates: [],
  groups: [
    { id: "fast", name: "Fast", members: ["a/m1", "b/m2"], routing: "order", ready: true, memberInfo: ["a/m1", "b/m2"].map(info), holds: [], offers: [], shared: [], patterns: [] },
    // as GET /api/groups said it: a pattern-only group whose plugin lists none of its models now
    JSON.parse('{"id":"p","name":"Plugin","members":null,"match":["a/m*"],"ready":false,"memberInfo":[],"holds":[],"offers":[],"shared":[],"patterns":[{"pattern":"a/m*","models":0}]}'),
    // one no tidying makes drawable: its rule names no member a page can read
    { id: "odd", name: "Odd", members: ["b/m2"], ready: true, memberInfo: [info("b/m2")], holds: [], offers: [], shared: [], patterns: [], rules: [{ use: 5 }] },
    { id: "slow", name: "Slow", members: ["b/m2"], ready: true, memberInfo: [info("b/m2")], holds: [], offers: [], shared: [], patterns: [] },
  ],
});
const providers = {
  providers: [{ id: "a", name: "Acme", icon: "generic", agents: [], models: [{ id: "m1", name: "m1", on: true }] }],
  presets: [], excluded: [],
  gateway: { running: true, window: true, mine: true, url: "http://127.0.0.1:3999", models: 2, calls: [], groups: [
    { id: "group/fast", name: "Fast", icons: ["generic"], providers: ["Acme"], members: ["a/m1"] },
    { id: "group/p", name: "Plugin", icons: null, providers: null, members: null },
  ] },
};

const words = {
  en: { none: "a/m* · matches nothing now", ready: "no member ready", edit: "Edit", bad: "This group can't be shown: " },
  zh: { none: "a/m* · 目前没有匹配的模型", ready: "没有可用成员", edit: "编辑", bad: "这个路由组无法显示：" },
};

function serve(lang) {
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
    if (url.pathname.startsWith("/api/groups/")) return json(groups());
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    for (const width of [1100, 420]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}: a group with null members leaves every other group drawn`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 1400 }, reducedMotion: "reduce" })).newPage();
        t.after(() => browser.close());
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=groups");
        const row = (name) => page.locator(".rt-group", { has: page.locator(".nm b", { hasText: new RegExp("^" + name + "$") }) });
        await row("Fast").waitFor();
        assert.deepEqual(await page.locator(".rt-group .nm b").allTextContents(), ["Fast", "Plugin", "Odd", "Slow"], "every group is listed");
        assert.equal(await page.locator(".err", { hasText: "members" }).count(), 0, "no error in place of the list");

        const p = row("Plugin");
        assert.deepEqual((await p.locator(".mem.pat").allTextContents()).map((x) => x.trim()), [w.none]);
        assert((await p.textContent()).includes(w.ready), "it says no member is ready");
        const odd = row("Odd");
        assert((await odd.locator(".mem").textContent()).startsWith(w.bad), "a group that can't be drawn says so in its own row");
        assert.equal(await page.locator(".rt-gbad").count(), 1, "only that one");
        assert((await row("Slow").textContent()).includes("m2"), "the group after it is drawn whole");

        // the filter reads every group's members
        const filter = page.locator("input.rt-gfilter");
        await filter.fill("m2");
        await page.waitForFunction(() => document.querySelectorAll(".rt-group .nm b").length === 3);
        assert.deepEqual(await page.locator(".rt-group .nm b").allTextContents(), ["Fast", "Odd", "Slow"]);
        await filter.fill("");
        await page.waitForFunction(() => document.querySelectorAll(".rt-group .nm b").length === 4);

        // the editor of a good group reads every other group's members
        await row("Fast").locator("button", { hasText: w.edit }).click();
        await page.locator(".rt-gedit").waitFor();
        // and the pattern group's own editor opens, with its pattern
        await page.reload();
        await row("Plugin").locator("button", { hasText: w.edit }).click();
        await page.locator(".rt-gedit .rt-pats").waitFor();
        assert((await page.locator(".rt-gedit .rt-pats").textContent()).includes("a/m*"));

        // the page doesn't scroll sideways at this width
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), "no sideways scroll");
        const missing = await page.evaluate(() => ["zh", "zh-TW", "ja", "de"].filter((l) => !I18N[l]["This group can't be shown: {error}"]));
        assert.deepEqual(missing, [], "every language has it");
        assert.deepEqual(errors, []);
      });
    }
    test(`${engine} ${lang}: the Gateway's model list survives a group with null lists`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 420, height: 900 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=gateway");
      await page.locator("#gwModels .row.model").first().waitFor();
      const ids = await page.locator("#gwModels .row.model .name").allTextContents();
      assert(ids.includes("group/fast") && ids.includes("group/p") && ids.includes("a/m1"), ids.join(", "));
      assert.deepEqual(errors, []);
    });
  }
}
