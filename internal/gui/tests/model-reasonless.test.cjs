// Run with Node's test runner and Playwright on the module path; see README.md.
// yuexian7 on Discord: models.dev lists their model as reasoning, and the
// vendor answers any reasoning parameter with 422 while the request
// without it works. In the provider editor's Model settings, a model's
// levels can all be unticked: magpie then sends it no reasoning field, said
// beside the levels, and the Save sends {reasonless: true}. A model saved
// so shows none ticked, and ticking one takes it back. Every language,
// at a wide and a narrow window.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function serve(lang, posts) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const models = [
    { id: "model-1", name: "Model 1", on: true, efforts: ["low", "high"], images: false },
    // saved with none kept: the backend says so, and gives no kept levels
    { id: "model-2", name: "Model 2", on: true, efforts: ["low", "high"], images: false, reasonless: true },
    // levels not known: none ticked is what it was, no levels known
    { id: "model-3", name: "Model 3", on: true, efforts: ["low", "high"], given: true, images: false },
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

const keys = [
  "No reasoning field sent",
  "Reasoning levels agents are offered; none ticked: it is sent no reasoning field",
  "None ticked: magpie sends {id} no reasoning field, whatever effort the agent asks, as for a model that doesn't reason",
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      test(`${engine} ${width} ${lang}: a model's levels all unticked sends it no reasoning field`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${width}-${lang}-model-reasonless.png`) });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=providers");
        const tr = (k, v = {}) => page.evaluate(([k, v]) => t(k, v), [k, v]);
        await page.locator(".row.provider").click();

        if (!(await page.locator(".mnames:not([hidden])").count())) await page.getByRole("button", { name: await tr("Model settings"), exact: true }).click();
        const row = (id) => page.locator(".mname", { has: page.locator("code", { hasText: id }) });
        const boxes = (id) => row(id).locator(".mlevels input[type=checkbox]");
        const hint = (id) => row(id).locator(".mlevels .mnone");
        const ticked = (id) => boxes(id).evaluateAll((bs) => bs.map((b) => b.checked));
        const none = await tr("No reasoning field sent");
        assert.equal(await row("model-1").locator(".mlevels").getAttribute("title"), await tr(keys[1]));

        // model-1: both unticked, no error, the hint shows
        assert.deepEqual(await ticked("model-1"), [true, true]);
        assert(!(await hint("model-1").isVisible()));
        await boxes("model-1").nth(0).click();
        await boxes("model-1").nth(1).click();
        assert.deepEqual(await ticked("model-1"), [false, false], "the last level unticks");
        assert(await hint("model-1").isVisible());
        assert.equal(await hint("model-1").textContent(), none);
        assert.equal(await hint("model-1").getAttribute("title"), await tr(keys[2], { id: "model-1" }));
        assert.equal(await page.locator("#status.err").count(), 0, "no error for none kept");
        // its Restore default shows, and puts the levels back
        assert(await row("model-1").getByRole("button", { name: await tr("Restore default") }).isVisible());

        // model-2, saved so: none ticked, said; ticking one takes it back
        assert.deepEqual(await ticked("model-2"), [false, false]);
        assert(await hint("model-2").isVisible());
        await boxes("model-2").nth(1).click();
        assert.deepEqual(await ticked("model-2"), [false, true]);
        assert(!(await hint("model-2").isVisible()));

        // model-3, levels not known: none ticked is as it was, no hint
        assert(!(await hint("model-3").isVisible()));

        const rect = await row("model-1").locator(".mlevels").boundingBox();
        assert(rect.x >= 0 && rect.x + rect.width <= width, `levels in the window: ${JSON.stringify(rect)}`);
        assert.deepEqual(posts, [], "nothing is sent before the Save");
        await page.getByRole("button", { name: await tr("Save"), exact: true }).click();
        await page.waitForFunction(() => !document.querySelector(".mnames"));
        const save = posts.find((p) => p.path === "/api/provider/save");
        assert(save, JSON.stringify(posts));
        assert.deepEqual(save.body.modelPrefs, {
          "model-1": { reasonless: true },
          "model-2": { reasonless: false, efforts: ["high"] },
        });
        const missing = await page.evaluate((keys) => keys.filter((k) => ["zh", "zh-TW", "ja", "de"].some((l) => !I18N[l][k])), keys);
        assert.deepEqual(missing, []);
        assert.deepEqual(errors, []);
      });
    }
  }
}
