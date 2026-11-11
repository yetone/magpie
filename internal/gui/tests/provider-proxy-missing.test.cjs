// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider synced here that names a saved proxy this computer lacks
// (@tsingchao on X) says so on its row, with the proxy's name; one whose
// proxy is there, or that has none, says nothing.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const base = {
  icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "model-a", name: "Model A", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false },
};
const rows = [
  { ...base, id: "gone", name: "Via Office", proxy: "@px1", proxyGone: "Office" },
  { ...base, id: "fine", name: "Plain Relay", proxy: "" },
];

function serve(lang) {
  const providers = { providers: rows, presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const badge = {
  en: "Proxy missing: Office", zh: "缺少代理：Office", "zh-TW": "缺少代理：Office",
  ja: "プロキシがありません：Office", de: "Proxy fehlt: Office",
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(badge)) {
    for (const width of [1000, 420]) {
      test(`${engine} ${lang} ${width}px: a provider's missing saved proxy is named on its row`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=providers");
        const gone = page.locator('.row.provider[data-id="gone"]');
        await gone.waitFor();
        assert.equal(await gone.locator(".badge", { hasText: badge[lang] }).count(), 1);
        assert.equal(await page.locator('.row.provider[data-id="fine"] .badge', { hasText: badge[lang].split("Office")[0] }).count(), 0);
        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert(over <= 0, `the page scrolls sideways by ${over}px`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
