// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group's id keeps the dots of a model's name (dbydd, #968): a
// new group named "GPT 6.1 Sol" is group/gpt-6.1-sol, not gpt-6-1-sol, as
// the editor says and as Add sends it, so a harness that prices or tunes
// by the model's name reads it. groupSlug folds a run of dots and trims
// them at the ends, as provider.GroupSlug. In English and Chinese,
// Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/gpt-6.1-sol", name: "gpt-6.1-sol", providerName: "A", icon: "generic" },
  { id: "b/gpt-6.1-sol", name: "gpt-6.1-sol", providerName: "B", icon: "generic" },
];
const found = { id: "auto-gpt-6-1-sol", name: "GPT-6.1 Sol", members: ["a/gpt-6.1-sol", "b/gpt-6.1-sol"], auto: true, ready: true,
  memberInfo: ["a/gpt-6.1-sol", "b/gpt-6.1-sol"].map((id) => ({ id, ready: true })) };

function serve(lang, saved) {
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
      return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups/save") {
      const body = r.request().postDataJSON();
      saved.push(body);
      return json({ models, pools: [], deciders: [], found: true, groups: [found, { id: body.id, name: body.name, members: body.members, ready: true }] });
    }
    if (url.pathname === "/api/groups") return json({ models, pools: [], deciders: [], found: true, groups: [found] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a group's id keeps a model's dots`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 700 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], saved = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, saved));
      await page.goto("http://magpie.test/?view=groups");
      await page.locator(".rt-group", { hasText: "GPT-6.1 Sol" }).waitFor({ state: "attached" });

      assert.deepEqual(await page.evaluate(() => ["GPT 6.1 Sol", "gpt-6.1-sol", "a..b", ".x.", "-.a.-", "claude/opus 5", "..."].map(groupSlug)),
        ["gpt-6.1-sol", "gpt-6.1-sol", "a.b", "x", "a", "claude-opus-5", ""]);

      await page.evaluate(() => { window.newGroupWith("a/gpt-6.1-sol", "GPT 6.1 Sol"); });
      const ed = page.locator(".rt-gsec .rt-gedit");
      await ed.waitFor();
      await page.waitForFunction(() => document.querySelector(".rt-gedit input")?.value === "GPT 6.1 Sol");
      assert.match(await ed.locator(".hint").first().textContent(), /group\/gpt-6\.1-sol(?![\w.-])/);
      // Add is below the fold: the wheel brings it up, as a reader scrolls
      const add = ed.locator("button.primary");
      await page.mouse.move(500, 300);
      for (let i = 0; i < 20; i++) {
        const b = await add.boundingBox();
        if (b.y >= 60 && b.y + b.height < 640) break;
        await page.mouse.wheel(0, b.y < 60 ? -300 : 300);
        await page.waitForTimeout(100);
      }
      await add.click();
      for (let i = 0; i < 40 && !saved.length; i++) await page.waitForTimeout(50);
      assert.equal(saved.length, 1, "Add sent the group");
      assert.equal(saved[0].id, "gpt-6.1-sol");
      assert.deepEqual(errors, []);
    });
  }
}
