// Run with Node's test runner and Playwright on the module path; see README.md.
// A key's provider's model can be given headers of its own (axelburks on
// Discord: Volcengine Ark routes an endpoint by a header), in the editor's
// Model settings (once Names, prices & levels): + Add header gives a name
// and value row, staged and made with the Save as the rest of it is; one
// saved shows in its row, × and Restore default take it away. A signed-in
// account's models have no such rows. Every language, Chromium and WebKit,
// wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets"));
const ark = {
  id: "volcengine", name: "Volcengine Ark", icon: "generic", chat: "https://ark.cn-beijing.volces.com/api/v3", responses: "", anthropic: "", catalog: "",
  models: [{ id: "ep-a", name: "ep-a", on: true }, { id: "ep-b", name: "ep-b", on: true, headers: { "X-Endpoint": "ep-b-1" } }],
  chosen: ["ep-a", "ep-b"], agents: [], fallback: [], headers: { "X-Route": "provider" }, takesModelHeaders: true,
  key: { set: true, masked: "ark-…aaa" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", ready: true,
};
const account = { id: "codex", name: "Codex", icon: "openai", account: { kind: "codex" }, models: [{ id: "gpt-5.5", name: "GPT-5.5", on: true }], chosen: ["gpt-5.5"], agents: [], fallback: [], headers: {}, keyList: [], ready: true };

function serve(lang, saves) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const list = { providers: [ark, account], presets: [], excluded: [], gateway: { running: true, window: true } };
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
      test(`${engine} ${lang} ${width}px: a model's own headers are staged in Model settings and made with the Save`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [], saves = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, saves));
        await page.goto("http://magpie.test/?view=providers");
        const L = {};
        for (const k of ["Model settings", "+ Add header", "Remove header", "Restore default", "Fold Model settings",
          "Headers sent with {id}'s requests alone, after the provider's; one the provider has too is sent as this model's",
          "Its own name, every reasoning level it has, whether it sees images, the API it is asked on, the model it is the same as, its own id, its list price, and no headers of its own"]) {
          L[k] = await say(page, lang, k);
          assert.ok(L[k], `${k} has its ${lang}`);
        }
        assert.notEqual(await say(page, "en", "Model settings"), undefined);
        await page.locator(".row.provider", { hasText: "Volcengine Ark" }).click();
        const editor = page.locator("#modal:not([hidden]) .editor");
        await editor.getByRole("button", { name: L["Model settings"], exact: true }).click();
        const names = editor.locator(".mnames:not([hidden])");
        assert.equal(await names.locator(".mnhead b").textContent(), L["Model settings"], "the panel is Model settings");
        const row = (id) => names.locator(".mname", { has: page.locator("code", { hasText: new RegExp(`^${id}$`) }) });
        // one saved shows; one with none has only + Add header
        assert.equal(await row("ep-a").locator(".mheaders .pair").count(), 0);
        assert.deepEqual(await row("ep-b").locator(".mheaders .pair input").evaluateAll((l) => l.map((i) => i.value)), ["X-Endpoint", "ep-b-1"]);
        const add = row("ep-a").locator(".mheader-add");
        assert.equal(await add.textContent(), L["+ Add header"]);
        assert.equal(await add.getAttribute("title"), L["Headers sent with {id}'s requests alone, after the provider's; one the provider has too is sent as this model's"].replace("{id}", "ep-a"));
        await add.click();
        const pair = row("ep-a").locator(".mheaders .pair");
        assert.equal(await pair.count(), 1);
        assert.equal(await page.evaluate(() => document.activeElement?.placeholder), await say(page, lang, "Header-Name"), "the new row's name box is focused");
        await pair.locator("input").nth(0).fill("X-Route");
        await pair.locator("input").nth(1).fill("ep-a-1");
        // a row with no width to spare stays inside its model's row
        const [r, p] = [await row("ep-a").boundingBox(), await pair.boundingBox()];
        assert.ok(p.x >= r.x && p.x + p.width <= r.x + r.width + 0.5, `the header row fits its model row (${JSON.stringify([r, p])})`);
        // ep-b's taken away by ×
        await row("ep-b").locator(".mheaders .pair button").click();
        assert.equal(await row("ep-b").locator(".mheaders .pair").count(), 0);
        assert.deepEqual(saves, [], "nothing is sent before the Save");
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-model-headers.png`) });
        }
        await editor.locator(".bar button.primary").click();
        for (let i = 0; i < 100 && !saves.length; i++) await page.waitForTimeout(20);
        assert.equal(saves.length, 1);
        assert.deepEqual(saves[0].modelPrefs, { "ep-a": { headers: { "X-Route": "ep-a-1" } }, "ep-b": { headers: {} } }, "the Save carries them, an empty set for the one taken away");

        // Restore default takes a saved one's away too
        await page.locator(".row.provider", { hasText: "Volcengine Ark" }).click();
        if (!(await editor.locator(".mnames:not([hidden])").count())) await editor.getByRole("button", { name: L["Model settings"], exact: true }).click();
        const reset = row("ep-b").getByRole("button", { name: L["Restore default"], exact: true });
        assert.equal(await reset.getAttribute("title"), L["Its own name, every reasoning level it has, whether it sees images, the API it is asked on, the model it is the same as, its own id, its list price, and no headers of its own"]);
        await reset.click();
        assert.equal(await row("ep-b").locator(".mheaders .pair").count(), 0);
        await editor.locator(".bar button.primary").click();
        for (let i = 0; i < 100 && saves.length < 2; i++) await page.waitForTimeout(20);
        assert.deepEqual(saves[1].modelPrefs, { "ep-b": { headers: {} } });

        // a signed-in account's models carry its sign-in's headers
        await page.locator(".row.provider", { hasText: "Codex" }).click();
        if (!(await editor.locator(".mnames:not([hidden])").count())) await editor.getByRole("button", { name: L["Model settings"], exact: true }).click();
        await editor.locator(".mnames:not([hidden]) .mname").first().waitFor();
        assert.equal(await editor.locator(".mheaders").count(), 0, "no header rows on an account");
        assert.deepEqual(errors, []);
      });
    }
  }
}
