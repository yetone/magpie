// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider's endpoint test that fails says why under its settings, and the
// words can be selected and copied, to hand to an agent or send in a report
// (imwritingbugs on X: 建议供应商设置这里的报错信息可以复制). The failed
// row has the small copy button other values have; a passing one has none.
// Its controls stay controls. In both engines, every language, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const why = "model_not_found: The model `claude-opus-9` does not exist or you do not have access to it";
const provider = {
  icon: "generic", id: "relay", preset: "relay", name: "Relay", chat: "https://relay.example/v1", responses: "", anthropic: "https://relay.example", catalog: "",
  agents: [], fallback: [], headers: {}, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", key: { set: true, masked: "sk-…one" },
  models: [{ id: "claude-opus-9", name: "", on: true }],
};

function serve(lang, copied) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json({ providers: [provider], presets: [{ id: "relay", name: "Relay", icon: "generic", chat: provider.chat, added: true }], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/provider/test") {
      return json({ results: [{ protocol: "chat", ok: false, status: 404, error: why }, { protocol: "anthropic", ok: true, ms: 120, model: "claude-opus-9" }] });
    }
    if (url.pathname === "/api/copy") { copied.push(route.request().postDataJSON().text); return json({ ok: true }); }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const selectOf = (loc) => loc.evaluate((e) => { const s = getComputedStyle(e); return s.userSelect || s.webkitUserSelect; });

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    for (const width of [1000, 420]) {
      test(`${engine} ${lang} ${width}: a failed endpoint test can be selected and copied`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        t.after(() => browser.close());
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], copied = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, copied));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Relay" }).first().click();
        await page.locator(".editor .mchips .mchip").first().waitFor();
        await page.locator(".editor .eps:not(.detect-out) .ep").first().waitFor();
        await page.waitForTimeout(300);
        await page.locator(".editor .eps button[data-tests]").click();
        const bad = page.locator(".editor .eps:not(.detect-out) .ep .res.bad span");
        await bad.waitFor();
        assert.equal(await selectOf(bad), "text");
        assert.equal(await bad.textContent(), "404 · " + why);
        // the copy button is on the failed row alone, and a control
        assert.equal(await page.locator(".editor .eps:not(.detect-out) .ep .copy").count(), 1);
        const cp = page.locator(".editor .eps:not(.detect-out) .ep .copy");
        assert.equal(await selectOf(cp), "none");
        await cp.click();
        await page.waitForFunction(() => document.querySelector(".editor .eps:not(.detect-out) .ep .copy.done"));
        assert.deepEqual(copied, ["404 · " + why]);
        assert.deepEqual(errors, []);
      });
    }
  }
}
