// Run with Node's test runner and Playwright on the module path; see README.md.
// A plugin's model its vendor has queued (Qoder's "queued the request for
// ...", Mr7Faith on X) answers its test OK: a green dot whose title says the
// queue, and Working only keeps it picked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const fetched = new Date(Date.now() - 3600e3).toISOString();
const m = (id, i) => ({ id, name: id, on: i < 4 });
const ids = ["glm-5", "kimi-k3", "qwen4-max", "deepseek-v4", "minimax-m3", "gpt-oss-200b"];
const relay = {
  id: "mixed", name: "Mixed Relay", icon: "generic", host: "mixed.example.com", chat: "https://mixed.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: ids.map(m), chosen: ids.slice(0, 4), fetched, agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};
// what the vendor answered: two of the four picks fail
const answers = {
  "glm-5": { ok: true, ms: 410, protocol: "openai", queued: "Qoder queued the request for glm-5 (this plan's queue: p4, 1 ahead); try again in 60s" },
  "kimi-k3": { ok: false, status: 404, error: "model not found", protocol: "openai" },
  "qwen4-max": { ok: true, ms: 380, protocol: "openai" },
  "deepseek-v4": { ok: false, status: 503, error: "no available channel", protocol: "openai" },
};
const KEPT = ["glm-5", "minimax-m3", "qwen4-max"];

function serve(lang, posts) {
  const providers = { providers: [relay], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (req.method() === "POST" && url.pathname.startsWith("/api/")) posts.push({ path: url.pathname, body: req.postDataJSON() || {} });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/provider/test") {
      const body = req.postDataJSON();
      return json({ results: body.test.map((model) => ({ model, ...answers[model] })) });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}


for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a queued model's test is green and says the queue`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" })).newPage();
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    await page.route("**/*", serve("en", []));
    await page.goto("http://magpie.test/?view=providers");
    const editor = page.locator("#modal:not([hidden]) .editor");
    await page.locator(".row.provider", { hasText: "Mixed Relay" }).click();
    await editor.locator(".mchips .mchip").first().waitFor();
    await editor.getByRole("button", { name: "Test models", exact: true }).click();
    await editor.locator(".mchips .tdot.ok").first().waitFor();
    const chip = editor.locator(".mchips .mchip", { hasText: "glm-5" });
    assert.equal(await chip.locator(".tdot.ok").count(), 1);
    assert.match(await chip.getAttribute("title"), /queued the request for glm-5/);
  });
}
