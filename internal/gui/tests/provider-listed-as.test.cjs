// A relay's model can be listed by an id of the user's (zeewell on X:
// deepseek-v4.1-flash-1017 as deepseek-v4.1-flash), in the provider
// editor's Names & levels beside Same as: magpie lists, routes and counts
// it by that id, and the relay is still asked for its own. It is staged
// and made with the editor's Save, as the rest of Names & levels is;
// Restore default and an emptied box list it by its own id again. A
// signed-in account's models (ids false) have no such box.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const levels = ["none", "low", "medium", "high"];

function serve(lang, posts) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const models = [
    { id: "deepseek-v4.1-flash-1017", name: "deepseek-v4.1-flash-1017", on: true, efforts: levels, images: false, merge: "deepseek-v4-1-flash-1017", ids: true },
    { id: "grok-4.6", name: "grok-4.6", on: true, efforts: levels, images: false, merge: "grok-4-6", ids: true, vendor: "grok-4.6@500k" },
  ];
  const relay = { id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example/v1", responses: "", anthropic: "", models, agents: [], key: { set: true, masked: "sk-…1234" }, ready: true };
  const acct = { id: "acct", name: "Acct", icon: "generic", chat: "https://acct.example/v1", responses: "", anthropic: "", models: [{ id: "m1", name: "m1", on: true, efforts: levels, images: false, merge: "m1" }], agents: [], key: { set: true, masked: "sk-…9999" }, ready: true };
  const providers = { providers: [relay, acct], presets: [], excluded: [], gateway: { running: true, window: true } };
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

const keys = ["Names & levels", "Listed as", "unsaved", "Save", "Restore default"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: the id a model is listed by is staged and made with the provider's Save`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 720 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=providers");
      await page.waitForFunction(() => typeof I18N === "object");
      const L = Object.fromEntries(await page.evaluate(([lang, keys]) => keys.map((k) => [k, lang === "en" ? k : I18N[lang][k]]), [lang, keys]));
      for (const k of keys) assert(L[k], `${lang} has ${k}`);
      const open = async (id) => {
        await page.locator(`.row.provider[data-id="${id}"]`).click();
        if (!(await page.locator(".mnames:not([hidden])").count())) await page.getByRole("button", { name: L["Names & levels"], exact: true }).click();
      };
      const row = (id) => page.locator(".mname", { has: page.locator("code", { hasText: id }) });
      const box = (id) => row(id).getByRole("textbox", { name: L["Listed as"], exact: true });
      await open("relay");
      // empty, it is its own id; one given shows, the own id beneath it
      assert.equal(await box("deepseek-v4.1-flash-1017").inputValue(), "");
      assert.equal(await box("deepseek-v4.1-flash-1017").getAttribute("placeholder"), "deepseek-v4.1-flash-1017");
      assert.equal(await box("grok-4.6").inputValue(), "grok-4.6");
      assert.equal(await box("grok-4.6").getAttribute("placeholder"), "grok-4.6@500k");
      // the id beside its name is its own, not the one it is listed by
      // (axelburks on Discord)
      assert.deepEqual(await page.locator(".mname .mwho code").allTextContents(), ["deepseek-v4.1-flash-1017", "grok-4.6@500k"]);
      const list = await page.locator(".mnames:not([hidden])").elementHandle();
      const y = await page.evaluate(() => scrollY);
      await box("deepseek-v4.1-flash-1017").fill("deepseek-v4.1-flash");
      await box("deepseek-v4.1-flash-1017").press("Enter");
      await row("grok-4.6").getByRole("button", { name: L["Restore default"] }).click();
      await page.waitForTimeout(200);
      assert.deepEqual(posts, [], "nothing is sent before the Save");
      assert(await list.evaluate((e) => e.isConnected), "the list is not redrawn");
      assert.equal(await page.evaluate(() => scrollY), y, "nothing scrolls the page");
      assert.equal(await box("grok-4.6").inputValue(), "", "Restore default empties it");
      for (const id of ["deepseek-v4.1-flash-1017", "grok-4.6"]) assert(await row(id).getByText(L.unsaved, { exact: true }).isVisible(), id + " says it is unsaved");
      // the box sits in the row as Same as does, no wider than the row
      const fits = await row("grok-4.6").evaluate((r) => [...r.querySelectorAll("input")].every((i) => i.getBoundingClientRect().right <= r.getBoundingClientRect().right + 1));
      assert(fits, "the box fits its row");
      await page.getByRole("button", { name: L.Save, exact: true }).click();
      await page.waitForFunction(() => !document.querySelector(".mnames"));
      assert.deepEqual(posts.map((p) => p.path), ["/api/provider/save"]);
      assert.deepEqual(posts[0].body.modelPrefs["deepseek-v4.1-flash-1017"], { id: "deepseek-v4.1-flash" });
      assert.equal(posts[0].body.modelPrefs["grok-4.6"].id, "");
      // an account's models take none
      await open("acct");
      assert.equal(await row("m1").getByRole("textbox", { name: L["Listed as"], exact: true }).count(), 0);
      const missing = await page.evaluate(() => ["Listed as",
        "The id magpie lists {id} by at this provider: agents, routing groups, the groups magpie finds, gateway keys and Usage all use it, and the provider is still asked for {own}. Empty: its own id",
        "Its own name, every reasoning level it has, whether it sees images, the API it is asked on, the model it is the same as, its own id, and its list price",
      ].flatMap((k) => ["zh", "zh-TW", "ja", "de"].filter((l) => !I18N[l][k]).map((l) => `${l}: ${k}`)));
      assert.deepEqual(missing, [], "every string has every language");
      assert.deepEqual(errors, []);
    });
  }
}
