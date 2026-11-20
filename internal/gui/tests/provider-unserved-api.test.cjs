// Run with Node's test runner and Playwright on the module path; see README.md.
// An endpoint the vendor serves this key nothing on (sub2api's 403 "This
// group does not allow /v1/messages dispatch": Sidrune AI, Axo 11-20) tests
// as that, in the row's plain pill: not red, with nothing to copy, the
// vendor's words in its tooltip. The other endpoint's answer is as before.
// In both engines, every language, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const { I18N } = strings();
const assets = path.resolve(__dirname, "../assets");
const why = "This group does not allow /v1/messages dispatch";
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
      return json({ results: [{ protocol: "chat", ok: true, ms: 120, model: "claude-opus-9" }, { protocol: "anthropic", ok: false, status: 403, error: why, unserved: true, model: "claude-opus-9" }] });
    }
    if (url.pathname === "/api/copy") { copied.push(route.request().postDataJSON().text); return json({ ok: true }); }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1000, 420]) {
      test(`${engine} ${lang} ${width}: an endpoint not served for the key is no failure`, async (t) => {
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
        await page.locator(".editor .eps:not(.detect-out) .ep").first().waitFor();
        await page.waitForTimeout(300);
        await page.locator(".editor .eps button[data-tests]").click();
        const rows = page.locator(".editor .eps:not(.detect-out) .ep");
        await rows.nth(0).locator(".res.ok").waitFor();
        const off = rows.nth(1).locator(".res");
        await page.waitForFunction(() => !document.querySelectorAll(".editor .eps:not(.detect-out) .ep .res")[1]?.classList.contains("wait"));
        const want = lang === "en" ? "Not served for this key" : I18N[lang]["Not served for this key"];
        assert.ok(want);
        assert.equal(await off.textContent(), want);
        assert.equal(await off.getAttribute("class"), "res");
        assert.equal(await off.getAttribute("title"), why);
        assert.equal(await page.locator(".editor .eps:not(.detect-out) .ep .copy").count(), 0);
        // it fits its row, narrow too
        const [r, e] = await Promise.all([off.boundingBox(), rows.nth(1).boundingBox()]);
        assert.ok(r.x + r.width <= e.x + e.width + 1, `${r.x + r.width} > ${e.x + e.width}`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
