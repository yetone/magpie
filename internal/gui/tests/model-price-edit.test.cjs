// Run with Node's test runner and Playwright on the module path; see README.md.
// #819 (ITea312: 图形界面找不到给某个模型单独定价，只能用全局倍率): a model's
// price, which only `magpie model price` set, is given in the provider
// editor's Names & levels, beside its name. Each box shows the model's list
// price until one is typed, a part left empty takes the list's, and
// Restore default gives it its list price again; all made with the
// provider's Save. A Remote magpie's model says its list price is what the
// other magpie counts it at (Sorghum on Discord). English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const list = { input: 1.25, output: 10, cache_read: 0.125, cache_write: 0 };

function serve(lang, posts) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const models = [
    { id: "model-1", name: "Model 1", on: true, efforts: [], images: false, list },
    { id: "model-2", name: "Model 2", on: true, efforts: [], images: false, list, price: { input: 0.5, output: 2, cache_read: 0.05, cache_write: 0 } },
    { id: "model-3", name: "Model 3", on: true, efforts: [], images: false },
    // a Remote magpie's: its list price is what that magpie counts it at
    { id: "model-4", name: "Model 4", on: true, efforts: [], images: false, list, remoteList: true },
  ];
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
    test(`${engine} ${lang}: a model's price is set in Names & levels, its list price shown`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 900 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-model-price.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=providers");
      const zh = lang === "zh";
      const L = { names: zh ? "名称与推理档位" : "Names & levels", price: zh ? "价格（$ / 百万 tokens）" : "Price, $ / 1M tokens", input: zh ? "输入" : "Input", output: zh ? "输出" : "Output",
        unsaved: zh ? "未保存" : "unsaved", save: zh ? "保存" : "Save", reset: zh ? "恢复默认" : "Restore default", nolist: zh ? "model-3 没有官方价格：请填写输入和输出价格" : "model-3 has no list price: give its input and output prices" };
      await page.locator(".row.provider").click();
      if (!(await page.locator(".mnames:not([hidden])").count())) await page.getByRole("button", { name: L.names, exact: true }).click();
      const row = (id) => page.locator(".mname", { has: page.locator("code", { hasText: id }) });
      const boxes = (id) => row(id).locator(".mprice > .mpart input");

      // the list price shows in the boxes, a price set in them
      assert.equal((await row("model-1").locator(".mprice > span").textContent()), L.price);
      assert.deepEqual(await boxes("model-1").evaluateAll((is) => is.map((i) => [i.value, i.placeholder])), [["", "1.25"], ["", "10"], ["", "0.125"], ["", "0"], ["", "2.5"]]);
      assert.deepEqual(await boxes("model-2").evaluateAll((is) => is.map((i) => i.value)), ["0.5", "2", "0.05", "0", ""]);
      assert.equal(await row("model-1").getByRole("textbox", { name: L.input, exact: true }).count(), 1);
      assert.equal(await row("model-1").locator("select").count(), 0);
      // whose list price it is: models.dev's, or the other magpie's
      assert.match(await row("model-1").locator(".mprice").getAttribute("title"), zh ? /留空即官方价格/ : /empty: its list price/);
      assert.match(await row("model-4").locator(".mprice").getAttribute("title"), zh ? /留空即另一台 magpie 计费用的价格/ : /empty: what the other magpie counts it at/);

      // a part typed, the others the list's; Restore default drops a price
      const y = await page.evaluate(() => scrollY);
      await row("model-1").getByRole("textbox", { name: L.output, exact: true }).fill("8");
      await row("model-1").getByRole("textbox", { name: L.output, exact: true }).press("Enter");
      assert(await row("model-1").getByText(L.unsaved, { exact: true }).isVisible());
      assert.deepEqual(await boxes("model-1").evaluateAll((is) => is.map((i) => i.value)), ["1.25", "8", "0.125", "0", ""]);
      await row("model-2").getByRole("button", { name: L.reset }).click();
      assert.deepEqual(await boxes("model-2").evaluateAll((is) => is.map((i) => i.value)), ["", "", "", "", ""]);
      // no list price: input and output must be given, one box at a time —
      // the part typed stays (PAMI on Discord: it was emptied, so no price
      // could be set), and a Save before the other part asks for it
      await row("model-3").getByRole("textbox", { name: L.input, exact: true }).fill("3");
      await row("model-3").getByRole("textbox", { name: L.input, exact: true }).press("Enter");
      await page.locator("#status", { hasText: L.nolist }).waitFor();
      assert.deepEqual(await boxes("model-3").evaluateAll((is) => is.map((i) => i.value)), ["3", "", "", "", ""]);
      assert(await row("model-3").getByText(L.unsaved, { exact: true }).isVisible());
      await page.getByRole("button", { name: L.save, exact: true }).click();
      await page.locator(".editor-error", { hasText: L.nolist }).waitFor();
      assert.equal(await page.evaluate(() => document.activeElement?.getAttribute("aria-label")), L.output);
      assert.deepEqual(posts, [], "a price half given isn't sent");
      await row("model-3").getByRole("textbox", { name: L.output, exact: true }).fill("15");
      await row("model-3").getByRole("textbox", { name: L.output, exact: true }).press("Enter");
      assert.deepEqual(await boxes("model-3").evaluateAll((is) => is.map((i) => i.value)), ["3", "15", "0", "0", ""]);
      assert.equal(await page.evaluate(() => scrollY), y, "nothing scrolled the page");
      assert.deepEqual(posts, [], "nothing is sent before the Save");
      if (process.env.ARTIFACT_DIR) await page.locator(".mnames").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-model-price-rows.png`) });

      await page.getByRole("button", { name: L.save, exact: true }).click();
      await page.waitForFunction(() => !document.querySelector(".mnames"));
      assert.deepEqual(posts.map((p) => p.path), ["/api/provider/save"]);
      assert.deepEqual(posts[0].body.modelPrefs, {
        "model-1": { price: { input: 1.25, output: 8, cache_read: 0.125, cache_write: 0 } },
        "model-2": { ownPrice: true },
        "model-3": { price: { input: 3, output: 15, cache_read: 0, cache_write: 0 } },
      });
      const missing = await page.evaluate(() => ["Price, $ / 1M tokens", "{id} has no list price: give its input and output prices", "A price is a number of dollars, 0 or more"]
        .filter((k) => !I18N.zh[k] || !I18N.ja[k] || !I18N.de[k]));
      assert.deepEqual(missing, []);
      assert.deepEqual(errors, []);
    });
  }
}
