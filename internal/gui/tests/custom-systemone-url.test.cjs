// Run with Node's test runner and Playwright on the module path; see README.md.
// A custom provider whose base URL is OpenAI Responses (a user on Discord:
// "under More endpoints there are OpenAI / Anthropic / Gemini addresses
// but no System One address"): More endpoints has a System One URL, and
// once one is typed Model settings' API row offers System One, and the
// Save sends both. With System One picked as the base URL's API, the field
// is the base URL and More endpoints doesn't show it twice. Every
// language, Chromium and WebKit, wide and at a phone's width.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets"));
const mine = {
  icon: "generic", catalog: "", agents: [], fallback: [], headers: {}, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", ready: true, key: { set: true, masked: "sk-…one" },
  id: "mine", name: "Mine", api: "responses", chat: "", responses: "https://relay.example.com/v1", anthropic: "", gemini: "", decideAPIKind: true, takesModelHeaders: true,
  models: [{ id: "m-chat", name: "m-chat", on: true }, { id: "m-decide", name: "m-decide", on: true }],
  chosen: ["m-chat", "m-decide"],
};

function serve(lang, posts) {
  const providers = { providers: [mine], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/provider/save") { posts.push(route.request().postDataJSON()); return json(providers); }
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
      test(`${engine} ${lang} ${width}px: a custom provider's System One URL under More endpoints`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Mine" }).first().click();
        const editor = page.locator("#modal:not([hidden]) .editor");
        const label = await say(page, lang, "System One URL");
        assert.ok(label, "translated");
        const more = editor.locator("details.more", { has: page.locator(":scope > summary", { hasText: await say(page, lang, "More endpoints") }) });
        await more.locator(":scope > summary").click();
        const input = more.getByLabel(label, { exact: true });
        assert.equal(await input.count(), 1, "one System One URL field");
        await input.fill("https://relay.example.com/v1");

        // Model settings: a model can now be set to System One
        if (!(await editor.locator(".mnames:not([hidden])").count())) await editor.getByRole("button", { name: await say(page, lang, "Model settings"), exact: true }).click();
        const row = editor.locator(".mnames:not([hidden]) .mname", { has: page.locator("code", { hasText: /^m-decide$/ }) });
        await row.locator('.mapi .opt[data-api="decide"]').click();
        const over = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1);
        assert.equal(over, false, "nothing wider than the window");
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-custom-systemone-url.png`), fullPage: true });
        }
        await editor.locator(".bar button.primary").click();
        for (let i = 0; i < 100 && !posts.length; i++) await page.waitForTimeout(20);
        assert.equal(posts[0]?.decide, "https://relay.example.com/v1", "the System One URL is saved beside Responses");
        assert.deepEqual(posts[0]?.modelPrefs, { "m-decide": { api: "decide" } });

        // System One as the base URL's API: the field isn't shown twice
        await page.locator(".row.provider", { hasText: "Mine" }).first().click();
        await editor.locator('.base-apis .opt[data-api="decide"]').click();
        assert.equal(await editor.locator("details.more").getByLabel(label, { exact: true }).count(), 0, "the base URL is the System One URL");
        assert.deepEqual(errors, []);
      });
    }
  }
}
