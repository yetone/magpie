// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider saved with no key — a local Ollama, from its preset or added as
// a custom provider — opens its editor on a click (willz on Discord: the row
// only toggled and WebKit said "undefined is not an object (evaluating
// 'copied?.key.set')"). The key box says one is optional, and nothing throws.
// The providers are what /api/providers gives for them. A plugin's provider,
// its base plugin://<id>, has no website link in its head (Lemon on Discord:
// CodeArts's said "codearts ↗" and opened https://codearts), while a local
// server's keeps its address. In English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const local = (id, name, preset, icon) => ({
  id, name, icon, preset, host: "localhost:11434", chat: "http://localhost:11434/v1", responses: "", anthropic: "", catalog: "",
  website: preset ? "https://ollama.com" : "", keysUrl: "", proxy: "", balanceToken: { takes: false, set: false },
  key: { set: false, masked: "", optional: true }, ready: true, chosen: [], fallback: [], routing: "", affinity: "",
  models: [{ id: "qwen3:8b", name: "qwen3:8b", efforts: ["none", "low", "medium", "high"], given: true, images: false, on: true }],
  exposed: 1, unlisted: false, off: false, fetched: "just now", agents: [], sponsored: false, keyList: [],
});
const providers = {
  providers: [local("ollama", "Ollama", "ollama", "ollama"), local("home-ollama", "Home Ollama", "", ""),
    { ...local("codearts", "CodeArts", "", ""), host: "codearts", chat: "plugin://codearts" }],
  presets: [{ id: "ollama", name: "Ollama", icon: "ollama", kind: "local", noKey: true, chat: "http://localhost:11434/v1", anthropic: "http://localhost:11434", note: "your local models", website: "https://ollama.com", added: true }],
  excluded: [], gateway: { running: true, window: true },
};

function serve(lang) {
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

const words = { en: { optional: "optional for local servers", cancel: "Cancel" }, zh: { optional: "本地服务可不填", cancel: "取消" } };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a provider with no key opens its editor`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=providers");
      const links = { Ollama: "ollama.com ↗", "Home Ollama": "localhost:11434 ↗", CodeArts: null };
      for (const name of ["Ollama", "Home Ollama", "CodeArts"]) {
        await page.locator(".row.provider", { has: page.locator(".name", { hasText: new RegExp(`^${name}$`) }) }).click();
        const ed = page.locator(".editor");
        await ed.waitFor();
        assert.equal(await ed.locator(".ehead b").textContent(), name);
        assert.equal(await ed.locator('input[type="password"]').first().getAttribute("placeholder"), w.optional);
        const link = ed.locator(".ehead .link");
        assert.equal(await link.count() ? await link.textContent() : null, links[name], `${name}: its head's link`);
        assert.deepEqual(errors, [], `${name}: nothing throws`);
        await ed.locator(".bar").getByRole("button", { name: w.cancel, exact: true }).click();
        await ed.waitFor({ state: "detached" });
      }
    });
  }
}
