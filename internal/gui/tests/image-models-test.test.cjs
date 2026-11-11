// Run with Node's test runner and Playwright on the module path; see README.md.
// AlphaChatFun on X: a provider of image models only answered "Pick a model
// first" to Test models. The ticked image models are drawn with instead.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const fetched = new Date(Date.now() - 3600e3).toISOString();
const drawIds = ["gpt-image-2", "gpt-image-1.5", "dall-e-3"];

function providerFor(hidden) {
  return {
    id: "openai", name: "OpenAI", icon: "openai", host: "", chat: "https://api.openai.com/v1", responses: "", anthropic: "", catalog: "",
    models: [], chosen: [], fetched, agents: [], fallback: [], headers: {},
    key: { set: true, masked: "sk-…one", optional: false }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", ready: true,
    draws: drawIds.length, drawIds, hiddenDrawers: hidden,
  };
}

function settingsPayload(lang, over) {
  return {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", textSize: 100,
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [
      { id: "openai/gpt-image-2", name: "gpt-image-2", provider: "openai", providerName: "OpenAI", icon: "openai" },
      { id: "openai/dall-e-3", name: "dall-e-3", provider: "openai", providerName: "OpenAI", icon: "openai" },
    ],
    imageGenAuto: "openai/gpt-image-2", workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

function serve(lang, tests, cur) {
  let hidden = ["dall-e-3"];
  const providers = () => ({ providers: [providerFor(hidden)], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true,textSize:100};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", textSize: 100 } });
    if (url.pathname === "/api/settings") return json(cur);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(providers());
    if (url.pathname === "/api/provider/test") {
      const b = req.postDataJSON();
      tests.push(b.test);
      return json({ results: b.test.map(() => ({ ok: true, ms: 900 })) });
    }
    if (url.pathname === "/api/provider/save") {
      const body = req.postDataJSON();
            if (body.hiddenDrawers) hidden = body.hiddenDrawers;
      return json(providers());
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [], models: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}


const pick = { en: /Pick a model first/, zh: /请先选择模型/, "zh-TW": /請先選擇模型/, ja: /先にモデルを選んで/, de: /Wähle zuerst/ };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Test models on a provider of image models draws with the ticked ones", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(pick)) {
      for (const width of [1000, 360]) {
        await t.test(`${lang} at ${width}px`, async () => {
          const tests = [];
          const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          page.errors = [];
          page.on("pageerror", (e) => page.errors.push(e.message));
          await page.route("**/*", serve(lang, tests, settingsPayload(lang, {})));
          await page.goto("http://magpie.test/?view=providers");
          await page.locator(".row.provider", { hasText: "OpenAI" }).first().click();
          const editor = page.locator("#modal:not([hidden]) .editor");
          await editor.locator(".mchips.drawers .mchip").first().waitFor();
          await editor.locator('button[data-tests="openai"]').click();
          await page.waitForFunction(() => document.querySelector("#modal:not([hidden]) .editor"));
          await new Promise((r) => setTimeout(r, 300));
          assert.deepEqual(tests, [["gpt-image-2", "gpt-image-1.5"]], "only the ticked image models are tested");
          assert.doesNotMatch(await page.locator("body").innerText(), pick[lang]);
          assert.deepEqual(page.errors, []);
          await page.context().close();
        });
      }
    }
  });
}
