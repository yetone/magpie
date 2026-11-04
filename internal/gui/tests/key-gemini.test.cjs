// Run with Node's test runner and Playwright on the module path; see README.md.
// A relay's key made for its Gemini API (corddis_114 on Discord): the key
// row's protocol menu offers Gemini beside Anthropic, Chat Completions and
// Responses when the provider has a Gemini URL, with what it is for, and
// picking it saves "gemini" for that key. A key set to Gemini on a
// provider with no Gemini URL says "No Gemini URL". Nothing runs off a
// 420px window. In every language, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const relay = (id, name, gemini) => ({
  id, name, icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "https://relay.example.com", gemini, catalog: "",
  models: [{ id: "gemini-3-pro", name: "", on: true }], agents: [], fallback: [], headers: {}, routing: "",
  key: { set: true, masked: "sk-…one" }, balanceToken: { takes: false, set: false }, proxy: "",
  keyList: [
    { id: id + "-a", name: "claude", masked: "sk-…one", on: true, active: true, protocol: "anthropic" },
    { id: id + "-g", name: "gemini", masked: "sk-…SarG", on: true, protocol: gemini ? "" : "gemini" },
  ],
});

function serve(lang, saved) {
  return async (request) => {
    const url = new URL(request.request().url());
    const json = (data) => request.fulfill({ json: data });
    if (url.pathname === "/boot.js") return request.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return request.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    const list = () => ({ providers: [relay("relay", "Relay", "https://relay.example.com/v1beta"), relay("nogem", "No Gem", "")], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/keys/protocol") {
      saved.push(JSON.parse(request.request().postData() || "{}"));
      return json(list());
    }
    if (url.pathname === "/api/providers") return json(list());
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await request.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

async function open(t, engine, lang, width) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [], saved = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, saved));
  await page.goto("http://magpie.test/?view=providers");
  const say = (key, vars = {}) => page.evaluate(([lang, key, vars]) => {
    const s = (lang !== "en" && I18N[lang]?.[key]) || key;
    return s.replace(/\{(\w+)\}/g, (_, k) => vars[k] ?? `{${k}}`);
  }, [lang, key, vars]);
  return { page, errors, saved, say };
}

const sideways = (page) => page.evaluate(() => document.documentElement.scrollWidth > innerWidth);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [420, 1000]) {
      test(`${engine} ${lang} ${width}px: a key can be set to Gemini`, async (t) => {
        const { page, errors, saved, say } = await open(t, engine, lang, width);
        await page.locator(".row.provider", { hasText: "Relay" }).first().click();
        const row = page.locator('.editor .accts.keys .acc[data-account-id="relay-g"]');
        await row.waitFor();
        await row.hover(); // an unset pill shows on the row's hover
        await row.locator("button.proto").click();
        const items = page.locator(".proto-menu .pm-item");
        await items.first().waitFor();
        const names = (await items.locator(".pm-name").allTextContents()).map((s) => s.trim());
        const want = [];
        for (const n of ["Any protocol", "Anthropic", "Chat Completions", "Gemini"]) want.push(await say(n));
        assert.deepEqual(names, want);
        const gem = items.filter({ has: page.locator(".pm-name", { hasText: /^Gemini$/ }) });
        assert.equal((await gem.locator(".pm-note").textContent()).trim(), await say("Gemini API (generateContent) · Gemini CLI, Gemini models"));
        const box = await page.locator(".proto-menu").boundingBox();
        assert.ok(box && box.x >= 0 && box.x + box.width <= width, `the menu runs off the window: ${JSON.stringify(box)}`);
        await gem.click();
        await page.waitForFunction(() => !document.querySelector(".proto-menu"));
        assert.deepEqual(saved, [{ id: "relay", ref: "relay-g", protocol: "gemini" }]);
        assert.equal(await sideways(page), false, "the page scrolls sideways");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: a Gemini key with no Gemini URL says so`, async (t) => {
        const { page, errors, say } = await open(t, engine, lang, width);
        await page.locator(".row.provider", { hasText: "No Gem" }).first().click();
        const row = page.locator('.editor .accts.keys .acc[data-account-id="nogem-g"]');
        await row.waitFor();
        const api = await say("Gemini");
        assert.equal((await row.locator(".key-nourl").textContent()).trim(), await say("No {api} URL", { api }));
        assert.equal((await row.locator("button.proto").textContent()).trim(), api);
        assert.equal(await sideways(page), false, "the page scrolls sideways");
        assert.deepEqual(errors, []);
      });
    }
  }
}
