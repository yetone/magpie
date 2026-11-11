// Run with Node's test runner and Playwright on the module path; see README.md.
// GPUStack is the user's own server: a tile in the Add sheet whose editor asks
// for the Server address (empty, http://localhost its placeholder) and, unlike
// Ollama, a key that is not marked optional. Add sends the address's /v1 as
// the chat URL and the key; the tile shows its logo and its note in each
// language. English, Chinese (Simplified and Traditional), Japanese and
// German; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const presets = [
  { id: "ollama", name: "Ollama", icon: "ollama", kind: "local", chat: "http://localhost:11434/v1", noKey: true, note: "your local models", added: false },
  { id: "gpustack", name: "GPUStack", icon: "gpustack", kind: "local", chat: "http://localhost/v1", note: "your own server and API key", added: false },
];
const note = { en: "your own server and API key", zh: "你自己的服务器和 API 密钥", "zh-TW": "你自己的伺服器和 API 金鑰", ja: "自分のサーバーと API キー", de: "Ihr eigener Server und API-Schlüssel" };

// one provider already added, so the list is not the empty welcome
const base = {
  icon: "generic", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "qwen3", name: "Qwen3", on: true }], agents: [], fallback: [], headers: {},
  key: { set: false, masked: "", optional: true }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", searches: false, unredacted: false,
};
const home = { ...base, id: "ollama", name: "Ollama", icon: "ollama", preset: "ollama", host: "localhost:11434", chat: "http://localhost:11434/v1" };

function server(lang, saves) {
  const list = { providers: [home], presets, excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(list);
    if (url.pathname === "/api/provider/save") { saves.push(route.request().postDataJSON()); return json(list); }
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": GPUStack is added at its server's address with a key", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(note)) {
      for (const width of [900, 440]) {
        await t.test(`${lang} ${width}`, async () => {
          const saves = [];
          const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang, saves));
          await page.goto("http://magpie.test/?view=providers");
          await page.locator("#addProvider").click();
          const tile = page.locator("#addSheet .tile", { hasText: /GPUStack/ });
          assert.equal(await tile.count(), 1);
          assert.equal(await tile.locator("img").first().getAttribute("src"), "icons/gpustack.png");
          assert.equal(await tile.getAttribute("title") === null, false);
          assert.match(await tile.getAttribute("title"), new RegExp(note[lang].replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
          await tile.click();
          const ed = page.locator(".editor");
          await ed.locator(".ehead b", { hasText: "GPUStack" }).waitFor();
          const address = ed.locator("input.address");
          assert.equal(await address.getAttribute("placeholder"), "http://localhost");
          const key = ed.locator("input[type=password]").first();
          assert.equal(await key.getAttribute("placeholder"), { en: "paste an API key" }[lang] ?? await key.getAttribute("placeholder"));
          assert.notEqual(await key.getAttribute("placeholder"), { en: "optional for local servers" }.en, "its key is not marked optional");
          await address.fill("192.168.1.9:8080");
          await key.fill("gpustack_abc_secret");
          await ed.locator(".bar button").last().click();
          for (let i = 0; i < 50 && !saves.length; i++) await page.waitForTimeout(50);
          assert.equal(saves.length, 1);
          assert.equal(saves[0].preset, "gpustack");
          assert.equal(saves[0].chat, "http://192.168.1.9:8080/v1");
          assert.equal(saves[0].key, "gpustack_abc_secret");
          assert.deepEqual(errors, []);
        });
      }
    }
  });
}
