// Run with Node's test runner and Playwright on the module path; see README.md.
// A model's own system prompt is set in the editor's Model settings (liucl
// on X): the gateway adds it after the agent's own on every request for that
// model. A saved one shows in its box; one typed is staged as
// modelPrefs[id].system, which the Save sends; Escape puts back what it had;
// Restore default sends it empty. Every language, Chromium and WebKit, wide
// and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets"));
const relay = {
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example/v1", responses: "", anthropic: "", catalog: "",
  models: [
    { id: "deepseek-v4.1-flash", name: "DeepSeek V4.1 Flash", on: true, context: 1000000, output: 384000 },
    { id: "glm-5", name: "GLM-5", on: true, context: 200000, output: 128000, system: "Answer in Chinese." },
  ],
  chosen: ["deepseek-v4.1-flash", "glm-5"], agents: [], fallback: [], headers: {}, 
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
      test(`${engine} ${lang} ${width}px: a model's own system prompt is set in Model settings`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [], saves = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, saves));
        await page.goto("http://magpie.test/?view=providers");
        const L = {};
        for (const k of ["Model settings", "System prompt", "Restore default", "unsaved",
          "Sent with every request for {id}, after the agent’s own system prompt, whichever agent sends it; empty: none",
          "Its own name, every reasoning level it has, whether it sees images, the API it is asked on, the model it is the same as, its own id, its list price, its listed window and reply limit, and no headers or system prompt of its own"]) {
          L[k] = await say(page, lang, k);
          assert.ok(L[k], `${k} has its ${lang}`);
        }
        await page.locator(".row.provider", { hasText: "Relay" }).click();
        const editor = page.locator("#modal:not([hidden]) .editor");
        await editor.getByRole("button", { name: L["Model settings"], exact: true }).click();
        const names = editor.locator(".mnames:not([hidden])");
        const row = (id) => names.locator(".mname", { has: page.locator("code", { hasText: new RegExp(`^${id.replace(/\./g, "\\.")}$`) }) });
        const sys = (id) => row(id).locator(".msys textarea");

        // a saved one shows; none is empty; it sits before unsaved and Restore default
        assert.equal(await row("glm-5").locator(".msys span").textContent(), L["System prompt"]);
        assert.equal(await sys("glm-5").inputValue(), "Answer in Chinese.");
        assert.equal(await sys("deepseek-v4.1-flash").inputValue(), "");
        assert.equal(await row("deepseek-v4.1-flash").locator(".msys").getAttribute("title"), L["Sent with every request for {id}, after the agent’s own system prompt, whichever agent sends it; empty: none"].replace("{id}", "deepseek-v4.1-flash"));
        const [sb, rb] = [await row("glm-5").locator(".msys").boundingBox(), await row("glm-5").getByRole("button", { name: L["Restore default"], exact: true }).boundingBox()];
        assert.ok(sb.y + sb.height <= rb.y + 1, `the box comes before Restore default (${JSON.stringify([sb, rb, await row("glm-5").evaluate((r) => [...r.children].map((c) => c.className + ":" + getComputedStyle(c).order))])})`);
        assert.equal(await row("glm-5").getByRole("button", { name: L["Restore default"], exact: true }).getAttribute("title"), L["Its own name, every reasoning level it has, whether it sees images, the API it is asked on, the model it is the same as, its own id, its list price, its listed window and reply limit, and no headers or system prompt of its own"]);
        assert.ok(await row("glm-5").getByRole("button", { name: L["Restore default"], exact: true }).isVisible(), "a model with its own prompt can be restored");
        // the box is the row's width and fits in it
        const [r, b] = [await row("deepseek-v4.1-flash").boundingBox(), await sys("deepseek-v4.1-flash").boundingBox()];
        assert.ok(b.x >= r.x && b.x + b.width <= r.x + r.width + 0.5, `the box fits its model row (${JSON.stringify([r, b])})`);
        assert.ok(b.width > r.width / 2, `the box takes the row's width (${JSON.stringify([r, b])})`);

        // typed, with Enter as a new line: staged, unsaved shown, nothing sent yet
        await sys("deepseek-v4.1-flash").click();
        await page.keyboard.type("Be brief.");
        await page.keyboard.press("Enter");
        await page.keyboard.type("Use metric units.");
        assert.equal(await sys("deepseek-v4.1-flash").inputValue(), "Be brief.\nUse metric units.");
        assert.ok(await row("deepseek-v4.1-flash").locator(".munsaved").isVisible(), "the row says unsaved");
        assert.ok(await editor.isVisible(), "Enter in the box keeps the editor open");
        // Escape puts back what it had
        await sys("glm-5").fill("Something else");
        await sys("glm-5").press("Escape");
        assert.equal(await sys("glm-5").inputValue(), "Answer in Chinese.");
        assert.deepEqual(saves, [], "nothing is sent before the Save");
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-model-system.png`) });
        }
        await editor.locator(".bar button.primary").click();
        for (let i = 0; i < 100 && !saves.length; i++) await page.waitForTimeout(20);
        assert.equal(saves.length, 1);
        assert.deepEqual(saves[0].modelPrefs, { "deepseek-v4.1-flash": { system: "Be brief.\nUse metric units." } });

        // Restore default takes the model's own away
        await page.locator(".row.provider", { hasText: "Relay" }).click();
        if (!(await editor.locator(".mnames:not([hidden])").count())) await editor.getByRole("button", { name: L["Model settings"], exact: true }).click();
        await row("glm-5").getByRole("button", { name: L["Restore default"], exact: true }).click();
        assert.equal(await sys("glm-5").inputValue(), "");
        await editor.locator(".bar button.primary").click();
        for (let i = 0; i < 100 && saves.length < 2; i++) await page.waitForTimeout(20);
        assert.equal(saves.length, 2);
        assert.deepEqual(saves[1].modelPrefs, { "glm-5": { system: "" } });
        assert.deepEqual(errors, []);
      });
    }
  }
}
