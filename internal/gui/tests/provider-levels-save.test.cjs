// A model's reasoning levels, name and images, picked in the provider
// editor's Names & levels, are staged and made with the editor's Save, as
// the rest of the editor is; Cancel drops them. They used to be posted, and
// the agents' files rewritten, at each tick, so picking levels lagged (ARNO
// on Discord).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const levels = ["none", "low", "medium", "high"];

function serve(lang, posts) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const models = Array.from({ length: 3 }, (_, i) => ({ id: `model-${i + 1}`, name: `Model ${i + 1}`, on: true, efforts: levels, images: false }));
  models[2] = { ...models[2], kept: ["low", "high"] };
  const provider = { id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example/v1", responses: "", anthropic: "", models, agents: [], key: { set: true, masked: "sk-…1234" }, ready: true };
  const providers = { providers: [provider], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/provider/")) {
      posts.push({ path: url.pathname, body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a model's levels are made with the provider's Save, not at each tick`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 720 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=providers");
      const zh = lang === "zh";
      const L = { names: zh ? "名称与推理档位" : "Names & levels", images: zh ? "支持图片输入" : "Accepts images", low: zh ? "低" : "low", high: zh ? "高" : "high", unsaved: zh ? "未保存" : "unsaved", save: zh ? "保存" : "Save", cancel: zh ? "取消" : "Cancel", reset: zh ? "恢复默认" : "Restore default" };
      const open = async () => {
        await page.locator(".row.provider").click();
        // it stays open from one opening of the editor to the next
        if (!(await page.locator(".mnames:not([hidden])").count())) await page.getByRole("button", { name: L.names, exact: true }).click();
      };
      const row = (id) => page.locator(".mname", { has: page.locator("code", { hasText: id }) });
      await open();
      const list = await page.locator(".mnames:not([hidden])").elementHandle();
      const y = await page.evaluate(() => scrollY);
      // ticks: nothing sent, nothing redrawn, the boxes as ticked
      await row("model-1").getByRole("checkbox", { name: L.low, exact: true }).uncheck();
      await row("model-1").getByRole("checkbox", { name: L.high, exact: true }).uncheck();
      await row("model-1").getByRole("checkbox", { name: L.images, exact: true }).check();
      await row("model-2").locator(".mwho input").first().fill("Two");
      await row("model-2").locator(".mwho input").first().press("Enter");
      await row("model-3").getByRole("button", { name: L.reset }).click();
      await page.waitForTimeout(200);
      assert.deepEqual(posts, [], "nothing is sent before the Save");
      assert(await list.evaluate((e) => e.isConnected), "the list is not redrawn");
      assert.equal(await page.evaluate(() => scrollY), y, "a tick never scrolls the page");
      assert.equal(await row("model-1").getByRole("checkbox", { name: L.low, exact: true }).isChecked(), false);
      assert.equal(await row("model-1").getByRole("checkbox", { name: L.high, exact: true }).isChecked(), false);
      for (const id of ["model-1", "model-2", "model-3"]) assert(await row(id).getByText(L.unsaved, { exact: true }).isVisible(), id + " says it is unsaved");
      assert.equal(await row("model-3").getByRole("checkbox", { name: zh ? "中" : "medium", exact: true }).isChecked(), true, "Restore default ticks every level");
      // Cancel drops them; opened again, the editor is as saved
      await page.getByRole("button", { name: L.cancel, exact: true }).click();
      await open();
      assert.equal(await row("model-1").getByRole("checkbox", { name: L.low, exact: true }).isChecked(), true);
      assert.equal(await row("model-1").getByText(L.unsaved, { exact: true }).isVisible(), false);
      assert.deepEqual(posts, []);
      // picked again and saved: one Save carries them all
      await row("model-1").getByRole("checkbox", { name: L.low, exact: true }).uncheck();
      await row("model-1").getByRole("checkbox", { name: L.low, exact: true }).check(); // back as it was: nothing for it
      await row("model-1").getByRole("checkbox", { name: L.high, exact: true }).uncheck();
      await row("model-3").getByRole("checkbox", { name: zh ? "中" : "medium", exact: true }).check();
      await page.getByRole("button", { name: L.save, exact: true }).click();
      await page.waitForFunction(() => !document.querySelector(".mnames"));
      assert.deepEqual(posts.map((p) => p.path), ["/api/provider/save"]);
      assert.deepEqual(posts[0].body.modelPrefs, { "model-1": { efforts: ["none", "low", "medium"] }, "model-3": { efforts: ["low", "medium", "high"] } });
      assert.deepEqual(errors, []);
    });
  }
}
