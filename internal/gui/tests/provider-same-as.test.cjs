// A model a vendor names its own way (Volcengine Ark's
// deepseek-v4-1-flash-260910) can be said, in the provider editor's Names &
// levels, to be the same as another vendor's model, so the routing groups
// magpie finds merge them (kyzhouxu, #583). It is staged and made with the
// editor's Save, as the rest of Names, prices & levels is; Cancel drops it.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const levels = ["none", "low", "medium", "high"];
const longModelID = "deepseek-v4-1-flash-economy-20261017";

function serve(lang, posts) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const models = [
    { id: longModelID, name: longModelID, on: true, efforts: levels, images: false, merge: "deepseek-v4-1-flash" },
    { id: "ep-2026-sol", name: "ep-2026-sol", on: true, efforts: levels, images: false, merge: "ep-2026-sol", same: "sol" },
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
    test(`${engine} ${lang}: the model a model is the same as is staged and made with the provider's Save`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 720 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=providers");
      const zh = lang === "zh";
      const L = { names: zh ? "名称、价格与推理档位" : "Names, prices & levels", same: zh ? "等同于" : "Same as", unsaved: zh ? "未保存" : "unsaved", save: zh ? "保存" : "Save", cancel: zh ? "取消" : "Cancel", reset: zh ? "恢复默认" : "Restore default" };
      const open = async () => {
        await page.locator('.row.provider[data-id="relay"]').click();
        if (!(await page.locator(".mnames:not([hidden])").count())) await page.getByRole("button", { name: L.names, exact: true }).click();
      };
      const row = (id) => page.locator(".mname", { has: page.locator("code", { hasText: id }) });
      const nameField = (id) => row(id).locator(".mwho input");
      const box = (id) => row(id).getByRole("textbox", { name: L.same, exact: true });
      await open();
      const nameFieldBox = await nameField(longModelID).boundingBox();
      assert(nameFieldBox && nameFieldBox.width >= 96, "a long model ID must not squeeze its name field away");
      // empty, it says what the found groups merge it by now; one given shows
      assert.equal(await box(longModelID).inputValue(), "");
      assert.equal(await box(longModelID).getAttribute("placeholder"), "deepseek-v4-1-flash");
      assert.equal(await box("ep-2026-sol").inputValue(), "sol");
      const list = await page.locator(".mnames:not([hidden])").elementHandle();
      const y = await page.evaluate(() => scrollY);
      await box(longModelID).fill("deepseek-v4.1-flash");
      await box(longModelID).press("Enter");
      await row("ep-2026-sol").getByRole("button", { name: L.reset }).click();
      await page.waitForTimeout(200);
      assert.deepEqual(posts, [], "nothing is sent before the Save");
      assert(await list.evaluate((e) => e.isConnected), "the list is not redrawn");
      assert.equal(await page.evaluate(() => scrollY), y, "nothing scrolls the page");
      assert.equal(await box("ep-2026-sol").inputValue(), "", "Restore default empties it");
      for (const id of [longModelID, "ep-2026-sol"]) assert(await row(id).getByText(L.unsaved, { exact: true }).isVisible(), id + " says it is unsaved");
      // Cancel drops them
      await page.getByRole("button", { name: L.cancel, exact: true }).click();
      await page.locator("dialog.action-confirm[open] button").last().click();
      await page.locator("#modal").waitFor({ state: "hidden" });
      await open();
      assert.equal(await box(longModelID).inputValue(), "");
      assert.equal(await box("ep-2026-sol").inputValue(), "sol");
      assert.deepEqual(posts, []);
      // given again and saved: one Save carries them
      await box(longModelID).fill("deepseek-v4.1-flash");
      await box(longModelID).press("Enter");
      await box("ep-2026-sol").fill("");
      await box("ep-2026-sol").press("Enter");
      await page.getByRole("button", { name: L.save, exact: true }).click();
      await page.waitForFunction(() => !document.querySelector(".mnames"));
      assert.deepEqual(posts.map((p) => p.path), ["/api/provider/save"]);
      assert.deepEqual(posts[0].body.modelPrefs, { [longModelID]: { same: "deepseek-v4.1-flash" }, "ep-2026-sol": { same: "" } });
      const missing = await page.evaluate(() => ["Same as",
        "The model other providers serve that {id} is the same as: the routing groups magpie finds put them together, and what no list gives for {id} is that model's: its window, reply limit, reasoning levels, images and list price. Empty: by its own id",
        "Its own name, every reasoning level it has, whether it sees images, the API it is asked on, the model it is the same as, its own id, and its list price",
      ].flatMap((k) => ["zh", "zh-TW", "ja", "de"].filter((l) => !I18N[l][k]).map((l) => `${l}: ${k}`)));
      assert.deepEqual(missing, [], "every string has every language");
      assert.deepEqual(errors, []);
    });
  }
}

// chiperman's case (#5170): beside deepseek-v4-1-flash-economy-20261017 the
// model's name field was squeezed until it couldn't be clicked. It keeps a
// usable width at a phone-wide, the default and a wide window, in every
// language, and the id beside it stays shown, cut short in the room left.
// Only the field's floor is raised: it still takes what an id leaves, so a
// short id is never cut to make it wider.
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a long model id leaves its name field room, at every width and language`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [400, 900, 1440]) {
        const context = await browser.newContext({ viewport: { width, height: 720 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, []));
        await page.goto("http://magpie.test/?view=providers");
        const at = `${lang} at ${width}px`;
        const names = await page.evaluate((l) => (l === "en" ? null : I18N[l]["Names, prices & levels"]) || "Names, prices & levels", lang);
        await page.locator('.row.provider[data-id="relay"]').click();
        await page.getByRole("button", { name: names, exact: true }).click();
        const who = page.locator(".mname .mwho", { has: page.locator("code", { hasText: longModelID }) });
        const [box, input, code] = await Promise.all([who.boundingBox(), who.locator("input").boundingBox(), who.locator("code").boundingBox()]);
        assert(input.width >= 96, `${at}: the name field is ${input.width}px wide`);
        assert(code.width > 0 && code.x >= input.x + input.width, `${at}: the id is shown after the name field`);
        assert(code.x + code.width <= box.x + box.width + 0.5, `${at}: the id stays inside its row`);
        assert(await who.locator("code").evaluate((e) => e.scrollWidth <= e.clientWidth || getComputedStyle(e).textOverflow === "ellipsis"), `${at}: a cut id ends in an ellipsis`);
        // the name field gives way first: an id that fits beside it is never cut
        const short = page.locator(".mname .mwho code", { hasText: "ep-2026-sol" });
        assert(await short.evaluate((e) => e.scrollWidth <= e.clientWidth), `${at}: a short id is shown whole`);
        assert.deepEqual(errors, [], at);
        await context.close();
      }
    }
  });
}
