// Run with Node's test runner and Playwright on the module path; see README.md.
// Tom on X asked for a way to add a model by hand and for more built-in
// icons. A model's list has an Add model button beside its id box, off
// until an id is typed; a click (or Enter) picks the typed model and a Save
// sends it. Built-in icons offers the vendors' icons no preset uses as well,
// every one of them drawn (none falls back to the generic cube), found by
// name in a box over the grid, and the one picked is what Save sends. In
// English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const relay = {
  id: "relay", name: "My Relay", icon: "generic", host: "relay.example.com", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-5", name: "gpt-5", on: true }, { id: "gpt-5-mini", name: "gpt-5-mini", on: false }], chosen: ["gpt-5"],
  fetched: new Date(Date.now() - 3600e3).toISOString(), agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};
const presets = [{ id: "deepseek", name: "DeepSeek", icon: "deepseek-color" }, { id: "openrouter", name: "OpenRouter", icon: "openrouter" }];

function serve(lang, posts) {
  const providers = { providers: [relay], presets, excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (req.method() === "POST" && url.pathname.startsWith("/api/")) posts.push({ path: url.pathname, body: req.postDataJSON() || {} });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { add: "Add model", icons: "Built-in icons", find: "Find an icon…", save: "Save" },
  zh: { add: "添加模型", icons: "内置图标", find: "查找图标…", save: "保存" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a model added by id, and an icon found among the built-in ones`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-models-add-and-icons.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posts = [];
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=providers");

      const editor = page.locator("#modal:not([hidden]) .editor");
      await page.locator(".row.provider", { hasText: "My Relay" }).click();
      await editor.locator(".mchips .mchip").first().waitFor();

      // Add model: off with nothing typed, on once an id is, and picks it
      const add = editor.locator(".mfoot").getByRole("button", { name: w.add, exact: true });
      const box = editor.locator(".mfoot input");
      assert(await add.isDisabled(), "Add model is on with nothing typed");
      assert(await add.getAttribute("title"));
      await box.fill("my-own-model");
      assert(await add.isEnabled());
      await add.click();
      assert.equal(await box.inputValue(), "");
      assert(await add.isDisabled(), "Add model stays on once the box is empty");
      const on = () => editor.locator(".mchips .mchip.on").evaluateAll((cs) => cs.map((c) => c.querySelector("span").textContent).sort());
      assert.deepEqual(await on(), ["gpt-5", "my-own-model"]);
      // Enter does the same
      await box.fill("another-model");
      await box.press("Enter");
      assert.deepEqual(await on(), ["another-model", "gpt-5", "my-own-model"]);

      // Built-in icons: the presets' and the others, every one drawn
      await editor.locator(".icon-pick").getByRole("button", { name: w.icons, exact: true }).click();
      const grid = editor.locator(".icon-grid");
      await grid.waitFor();
      const names = await grid.locator("button").evaluateAll((bs) => bs.map((b) => b.title));
      assert(names.length >= 90, `only ${names.length} icons`);
      for (const n of ["deepseek", "openrouter", "doubao", "huggingface", "perplexity", "aws"]) assert(names.includes(n), `no ${n} icon`);
      await page.waitForFunction(() => [...document.querySelectorAll(".icon-grid img")].every((i) => i.complete), null, { timeout: 10000 });
      await page.waitForTimeout(300);
      const broken = await grid.locator("button").evaluateAll((bs) => bs.filter((b) => b.querySelector(".ic.generic") || [...b.querySelectorAll("img")].some((i) => !i.naturalWidth)).map((b) => b.title));
      assert.deepEqual(broken, [], "icons that don't draw");
      // the grid scrolls in its own box, not the dialog off its feet
      assert(await grid.evaluate((g) => g.clientHeight <= 200 && g.scrollHeight > g.clientHeight), "the grid isn't a scrolling box");

      // found by name
      const find = editor.getByPlaceholder(w.find);
      await find.fill("doub");
      assert.deepEqual(await grid.locator("button:visible").evaluateAll((bs) => bs.map((b) => b.title)), ["doubao"]);
      await find.fill("");
      assert.equal(await grid.locator("button:visible").count(), names.length);
      await find.fill("huggingface");
      await grid.locator("button:visible").click();
      await grid.waitFor({ state: "detached" });
      assert.equal(await editor.locator(".icon-now .ic img").getAttribute("src"), "icons/huggingface-color.svg");

      await editor.locator(".bar").getByRole("button", { name: w.save, exact: true }).click();
      for (let i = 0; i < 100 && !posts.some((p) => p.path === "/api/provider/save"); i++) await page.waitForTimeout(30);
      const saved = posts.find((p) => p.path === "/api/provider/save");
      assert(saved, "nothing saved");
      assert.deepEqual([...saved.body.models].sort(), ["another-model", "gpt-5", "my-own-model"]);
      assert.equal(saved.body.icon, "huggingface-color");

      const missing = await page.evaluate(() => ["Add model", "Add a model the list doesn't have, by its id (several: comma separated)", "Find an icon…"].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });
  }
}
