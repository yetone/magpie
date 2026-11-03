// Run with Node's test runner and Playwright on the module path; see README.md.
// jzw690 on X: a group's Add a model picker didn't list the providers'
// image models, so a group of them, to pick in Settings › Images, couldn't
// be made.
// - The picker offers each image model in /api/groups' drawers, its note
//   saying it is one; a click adds it, and its row says so and offers no
//   reasoning, as an image model takes none.
// - Saved, the group holds it as written.
// In English and Chinese, Chromium and WebKit. Not one <select>.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [{ id: "a/one", name: "one", providerName: "A", icon: "generic" }];
const drawers = [{ id: "a/gpt-image-2", name: "gpt-image-2", providerName: "A", icon: "generic" }];

const words = {
  en: { edit: "Edit", save: "Save", add: "Add another model", image: "Image model" },
  zh: { edit: "编辑", save: "保存", add: "再添加一个模型", image: "生图模型" },
};

function serve(lang, posts) {
  const groups = () => ({
    models, drawers, pools: [],
    groups: [{ id: "g", name: "G", members: ["a/one"], off: [], routing: "order", ready: true, memberInfo: [{ id: "a/one", ready: true }] }],
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
    test(`${engine} ${lang}: a group's picker offers the image models`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 1400 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=routing");
      const card = page.locator('.rt-group[data-id="g"]');
      await card.waitFor();
      await card.locator("button", { hasText: w.edit }).click();
      const ed = page.locator(".rt-gedit");
      await ed.locator("button.rt-gadd", { hasText: w.add }).click();
      const pop = page.locator("#pop");
      const opt = (name) => pop.locator("li", { has: page.locator(".v", { hasText: new RegExp(`^${name}$`) }) }).first();
      await opt("gpt-image-2").waitFor();
      assert((await opt("gpt-image-2").locator(".n").textContent()).includes(`A · ${w.image}`), "the picker doesn't say it is an image model");
      assert(!(await opt("one").locator(".n").textContent()).includes(w.image), "a chat model said to be an image model");
      const rows = ed.locator(".fbl").first().locator(".fbrow");
      assert.equal(await rows.count(), 1);
      await opt("gpt-image-2").click();
      assert.equal(await rows.count(), 2, "clicked, the image model was not added");
      await page.keyboard.press("Escape");
      await pop.waitFor({ state: "hidden" });
      const row = rows.nth(1);
      assert((await row.locator("small").first().textContent()).includes(w.image), "its row doesn't say it is an image model");
      assert.equal(await row.locator(".rt-fixed").count(), 0, "an image model offered a reasoning level");
      assert.equal(await rows.nth(0).locator(".rt-fixed").count(), 1, "the chat model lost its reasoning button");

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
      assert.deepEqual(sent.body.members, ["a/one", "a/gpt-image-2"]);
      assert.equal(await page.locator("select").count(), 0, "a native select");
      assert.deepEqual(errors, []);
    });
  }
}
