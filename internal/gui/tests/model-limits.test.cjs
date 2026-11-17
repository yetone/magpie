// Run with Node's test runner and Playwright on the module path; see README.md.
// A model's context window and max output are set in the editor's Model
// settings, over the vendor's and models.dev's (sayagoodbyeplz on Discord:
// WorkBuddy's deepseek-v4.1-flash replies 128000 tokens, not models.dev's
// 384000). Each row shows the listed value as its placeholder; one typed is
// staged as model=size in Advanced's Context window and Max output, which
// the Save sends; one for all of them typed in Advanced becomes the
// placeholder; Restore default takes the model's own away; a bad value is
// refused. Every language, Chromium and WebKit, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets"));
const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example/v1", responses: "", anthropic: "", catalog: "",
  models: [
    { id: "deepseek-v4.1-flash", name: "DeepSeek V4.1 Flash", on: true, context: 1000000, output: 384000, listed: 1000000, listedOutput: 384000 },
    { id: "glm-5", name: "GLM-5", on: true, context: 200000, output: 128000, listed: 200000, listedOutput: 128000 },
  ],
  chosen: ["deepseek-v4.1-flash", "glm-5"], agents: [], fallback: [], headers: {}, contexts: { "glm-5": 160000 },
  key: { set: true, masked: "sk-…aaa" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", ready: true,
};

function serve(lang, saves) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const list = { providers: [relay], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(list);
    if (url.pathname === "/api/provider/save") { saves.push(req.postDataJSON()); return json(list); }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const say = (page, lang, s) => page.evaluate(([l, s]) => (l === "en" ? s : I18N[l][s]), [lang, s]);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1200, 420]) {
      test(`${engine} ${lang} ${width}px: a model's context window and max output are set in Model settings`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], saves = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, saves));
        await page.goto("http://magpie.test/?view=providers");
        const L = {};
        for (const k of ["Model settings", "Context window", "Max output", "Restore default", "unsaved",
          "The window agents are told {id} takes; empty: the vendor's or models.dev's",
          "The most a reply of {id} may hold, told to agents as their max tokens and kept to by magpie; empty: the vendor's or models.dev's",
          "Max output: {v} is not a number of tokens like 32k"]) {
          L[k] = await say(page, lang, k);
          assert.ok(L[k], `${k} has its ${lang}`);
        }
        await page.locator(".row.provider", { hasText: "Relay" }).click();
        const editor = page.locator("#modal:not([hidden]) .editor");
        await editor.getByRole("button", { name: L["Model settings"], exact: true }).click();
        const names = editor.locator(".mnames:not([hidden])");
        const row = (id) => names.locator(".mname", { has: page.locator("code", { hasText: new RegExp(`^${id.replace(/\./g, "\\.")}$`) }) });
        const limit = (id, k) => row(id).locator(`.mlimit[data-limit=${k}] input`);
        const adv = (k) => editor.locator(`input.${k}`).evaluate((i) => i.value);

        // the listed values are the placeholders; a saved one of its own shows
        const out = limit("deepseek-v4.1-flash", "outputs"), win = limit("deepseek-v4.1-flash", "contexts");
        assert.equal(await row("deepseek-v4.1-flash").locator(".mlimit[data-limit=outputs] span").textContent(), L["Max output"]);
        assert.equal(await row("deepseek-v4.1-flash").locator(".mlimit[data-limit=contexts] span").textContent(), L["Context window"]);
        assert.equal(await out.getAttribute("placeholder"), "384K");
        assert.equal(await out.inputValue(), "");
        assert.equal(await win.getAttribute("placeholder"), "1M");
        assert.equal(await limit("glm-5", "contexts").inputValue(), "160K");
        assert.equal(await out.getAttribute("title"), L["The most a reply of {id} may hold, told to agents as their max tokens and kept to by magpie; empty: the vendor's or models.dev's"].replace("{id}", "deepseek-v4.1-flash"));
        assert.equal(await win.getAttribute("title"), L["The window agents are told {id} takes; empty: the vendor's or models.dev's"].replace("{id}", "deepseek-v4.1-flash"));
        // the row fits its model row
        const [r, b] = [await row("deepseek-v4.1-flash").boundingBox(), await out.boundingBox()];
        assert.ok(b.x >= r.x && b.x + b.width <= r.x + r.width + 0.5, `the max output box fits its model row (${JSON.stringify([r, b])})`);

        // typed: staged in Advanced, unsaved shown, nothing sent yet
        await out.fill("128k");
        await out.press("Enter");
        assert.equal(await out.inputValue(), "128K");
        assert.equal(await adv("outputs"), "deepseek-v4.1-flash=128k");
        assert.equal(await row("deepseek-v4.1-flash").locator(".munsaved").count(), 1, "the row says unsaved");
        // a bad value is refused and the box keeps what it had
        await limit("glm-5", "outputs").fill("lots");
        await limit("glm-5", "outputs").press("Enter");
        assert.equal(await limit("glm-5", "outputs").inputValue(), "");
        assert.equal(await adv("outputs"), "deepseek-v4.1-flash=128k");
        // one for all of them in Advanced is every row's placeholder
        await editor.locator("input.outputs").evaluate((i) => { i.value = "64k, deepseek-v4.1-flash=128k"; i.dispatchEvent(new Event("input")); });
        assert.equal(await limit("glm-5", "outputs").getAttribute("placeholder"), "64K");
        assert.equal(await out.inputValue(), "128K");
        assert.deepEqual(saves, [], "nothing is sent before the Save");
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-model-limits.png`) });
        }
        await editor.locator(".bar button.primary").click();
        for (let i = 0; i < 100 && !saves.length; i++) await page.waitForTimeout(20);
        assert.equal(saves.length, 1);
        assert.deepEqual(saves[0].outputs, { "*": 64000, "deepseek-v4.1-flash": 128000 });
        assert.deepEqual(saves[0].contexts, { "glm-5": 160000 });

        // Restore default takes the model's own away
        await page.locator(".row.provider", { hasText: "Relay" }).click();
        if (!(await editor.locator(".mnames:not([hidden])").count())) await editor.getByRole("button", { name: L["Model settings"], exact: true }).click();
        await row("glm-5").getByRole("button", { name: L["Restore default"], exact: true }).click();
        assert.equal(await limit("glm-5", "contexts").inputValue(), "");
        assert.equal(await limit("glm-5", "contexts").getAttribute("placeholder"), "200K");
        assert.equal(await adv("contexts"), "");
        await editor.locator(".bar button.primary").click();
        for (let i = 0; i < 100 && saves.length < 2; i++) await page.waitForTimeout(20);
        assert.equal(saves.length, 2);
        assert.deepEqual(saves[1].contexts, {});
        assert.deepEqual(errors, []);
      });
    }
  }
}
