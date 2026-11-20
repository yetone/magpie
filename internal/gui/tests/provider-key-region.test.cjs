// Run with Node's test runner and Playwright on the module path; see README.md.
// A key its region refuses with 401 and the other region takes (Sidrune
// AI's China key typed in at Global, Axo 11-20): Test says the region in
// its results, and the editor moves to that region, its endpoints with it,
// and says so. Saving a provider magpie moved says where it was saved.
// In both engines, every language, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const { I18N } = strings();
const assets = path.resolve(__dirname, "../assets");
const regions = [
  { id: "cn", name: "China", chat: "https://cn.sid.example/v1" },
  { id: "global", name: "Global", chat: "https://sid.example/v1" },
];
const provider = {
  icon: "generic", id: "sid", preset: "sid", name: "Sid", chat: regions[1].chat, responses: "", anthropic: "", catalog: "",
  agents: [], fallback: [], headers: {}, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", key: { set: true, masked: "sk-…one" },
  models: [{ id: "gpt-5.5", name: "", on: true }],
};
const tr = (lang, s) => lang === "en" ? s : I18N[lang][s];

function serve(lang, saved) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const list = (placed) => ({ providers: [provider], presets: [{ id: "sid", name: "Sid", icon: "generic", regions, added: true }], excluded: [], gateway: { running: true, window: true }, ...(placed ? { placed } : {}) });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(list());
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/provider/test") return json({ results: [{ protocol: "chat", ok: true, ms: 120, model: "gpt-5.5", region: "cn" }] });
    if (url.pathname === "/api/provider/save") { saved.push(route.request().postDataJSON()); return json(list("China")); }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1000, 420]) {
      test(`${engine} ${lang} ${width}: a key of the other region moves the editor there`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        t.after(() => browser.close());
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], saved = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, saved));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Sid" }).first().click();
        const seg = page.locator(".editor .segs.regions");
        await seg.locator(".opt.on").waitFor();
        const name = (r) => tr(lang, r) || r;
        assert.equal(await seg.locator(".opt.on").textContent(), name("Global"));
        await page.waitForTimeout(300);
        await page.locator(".editor .eps button[data-tests]").click();
        await page.waitForFunction((n) => document.querySelector(".editor .segs.regions .opt.on")?.textContent === n, name("China"));
        const said = I18N[lang]?.["The key is {region}'s: moved there. Save to keep it."] ?? "The key is {region}'s: moved there. Save to keep it.";
        assert.equal(await page.locator("#status").textContent(), said.replace("{region}", name("China")));
        // the status fits the window, narrow too
        const s = await page.locator("#status").boundingBox();
        assert.ok(s.x >= 0 && s.x + s.width <= width + 1, `${s.x}+${s.width} > ${width}`);
        await page.locator(".editor button.primary").first().click();
        await page.waitForFunction(() => !document.querySelector(".editor"));
        assert.equal(saved.length, 1);
        assert.equal(saved[0].chat, regions[0].chat);
        const done = I18N[lang]?.["Saved at {region}: the key is that region's"] ?? "Saved at {region}: the key is that region's";
        assert.equal(await page.locator("#status").textContent(), done.replace("{region}", name("China")));
        assert.deepEqual(errors, []);
      });
    }
  }
}
