// Run with Node's test runner and Playwright on the module path; see README.md.
// A model id can hold a space (#1976: "Opus 5.5" on a Baidu OneAPI relay).
// The editor's add box and the add form's Models field split what is typed
// at commas only, so "Opus 5.5" stays one model while "a, b" still adds two,
// and Save sends them so. In every language, Chromium and WebKit, at 1100
// and 420 wide.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const relay = {
  id: "relay", name: "Baidu OneAPI", icon: "generic", host: "relay.example.com", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-5", name: "gpt-5", on: true }], chosen: ["gpt-5"],
  fetched: new Date(Date.now() - 3600e3).toISOString(), agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};

function serve(lang, posts) {
  const providers = { providers: [relay], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (req.method() === "POST" && url.pathname.startsWith("/api/")) posts.push({ path: url.pathname, body: req.postDataJSON() || {} });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const saved = async (page, posts) => {
  for (let i = 0; i < 100 && !posts.some((p) => p.path === "/api/provider/save"); i++) await page.waitForTimeout(30);
  const s = posts.find((p) => p.path === "/api/provider/save");
  assert(s, "nothing saved");
  return s.body.models;
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      const open = async (t, posts) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=providers");
        return { page, errors };
      };

      test(`${engine} ${lang} ${width}: an id typed in the add box keeps its space`, async (t) => {
        const posts = [];
        const { page, errors } = await open(t, posts);
        const editor = page.locator("#modal:not([hidden]) .editor");
        await page.locator(".row.provider", { hasText: "Baidu OneAPI" }).click();
        await editor.locator(".mchips .mchip").first().waitFor();
        const box = editor.locator(".mfoot input");
        const on = () => editor.locator(".mchips .mchip.on").evaluateAll((cs) => cs.map((c) => c.querySelector("span").textContent).sort());
        await box.fill("Opus 5.5");
        await box.press("Enter");
        assert.deepEqual(await on(), ["Opus 5.5", "gpt-5"]);
        await box.fill(" id-one ,id-two, ");
        await box.press("Enter");
        assert.deepEqual(await on(), ["Opus 5.5", "gpt-5", "id-one", "id-two"]);
        // an id left typed in the box goes with the Save, whole
        await box.fill("Claude Opus 5.5");
        await editor.locator(".bar").getByRole("button", { name: await page.evaluate(() => t("Save")), exact: true }).click();
        assert.deepEqual([...await saved(page, posts)].sort(), ["Claude Opus 5.5", "Opus 5.5", "gpt-5", "id-one", "id-two"]);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: an id typed in the add form's Models keeps its space`, async (t) => {
        const posts = [];
        const { page, errors } = await open(t, posts);
        await page.locator("#addProvider").click();
        await page.locator("#addSheet .row-head .custom").click();
        const ed = page.locator(".editor.new");
        await ed.locator(".add-models").waitFor();
        const L = await page.evaluate(() => ({ name: t("e.g. My Relay"), add: t("Add") }));
        await ed.locator(`input[placeholder="${L.name}"]`).first().fill("Relay");
        await ed.locator('input[type="url"][placeholder="https://…/v1"]').first().fill("https://relay.example.com/v1");
        await ed.locator('input[type="password"]').first().fill("sk-typed");
        await ed.locator(".add-models .detect-row input").fill("own-model, Claude Opus 5.5,Opus 5.5");
        await ed.getByRole("button", { name: L.add, exact: true }).click();
        assert.deepEqual(await saved(page, posts), ["own-model", "Claude Opus 5.5", "Opus 5.5"]);
        assert.deepEqual(errors, []);
      });
    }
  }
}
