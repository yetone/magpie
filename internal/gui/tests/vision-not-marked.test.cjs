// Run with Node's test runner and Playwright on the module path; see README.md.
// nexnull on Discord: a custom provider's model that nobody has said takes
// images is not among Image recognition's models, and nothing said why or
// where to change it. Under the row a line now names those providers and
// says the model is marked in its provider's Model settings › Accepts
// images, in the editor's own words. None such, or Image recognition off,
// no line. Every language; wide and at a phone's width, nothing running
// off to the side; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function settingsPayload(lang, over) {
  return {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", textSize: 100,
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [{ id: "codex/gpt-6.1-sol", name: "GPT-6.1 Sol", provider: "codex", providerName: "Codex", icon: "openai" }],
    visionAuto: "codex/gpt-6.1-sol",
    imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

function server(lang, cur) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true,textSize:100};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", textSize: 100 }, fx: cur.fx });
    if (url.pathname === "/api/settings") return json(cur);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Image recognition says where a custom provider's model is marked as seeing", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const open = async (lang, cur, width) => {
      const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.errors = [];
      page.on("pageerror", (e) => page.errors.push(e.message));
      await page.route("**/*", server(lang, cur));
      await page.goto("http://magpie.test/?view=settings&tab=models");
      await page.locator("#imagesList .row").nth(1).waitFor();
      return page;
    };
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1000, 360]) {
        await t.test(`${lang} at ${width}px: the providers are named, with where to mark a model`, async () => {
          const page = await open(lang, settingsPayload(lang, { visionNotMarked: ["My Relay", "Local vLLM"] }), width);
          const tr = (k, vars = {}) => page.evaluate(([k, vars]) => t(k, vars), [k, vars]);
          const names = await tr("Model settings"), images = await tr("Accepts images");
          const line = page.locator("#imagesList .row").nth(0).locator(".vision-not-marked");
          assert.equal(await line.isVisible(), true);
          const text = await line.textContent();
          assert.match(text, /My Relay, Local vLLM/);
          // the checkbox and the section by the words the editor shows
          assert.ok(text.includes(names) && text.includes(images), text);
          if (lang !== "en") assert.ok(!/Model settings|Accepts images|aren’t listed/.test(text), "untranslated: " + text);
          if (lang === "zh") assert.ok(text.includes("模型设置") && text.includes("支持图片输入"), text);
          const fit = await page.evaluate(() => {
            const l = document.querySelector("#imagesList .vision-not-marked"), row = l.closest(".row").getBoundingClientRect(), r = l.getBoundingClientRect();
            return { inRow: r.left >= row.left - 1 && r.right <= row.right + 1, scroll: document.documentElement.scrollWidth - document.documentElement.clientWidth,
              stripe: getComputedStyle(l).borderLeftWidth };
          });
          assert.deepEqual(fit, { inRow: true, scroll: 0, stripe: "0px" });
          assert.deepEqual(page.errors, []);
          await page.context().close();
        });
      }
      await t.test(`${lang}: none unmarked, or Image recognition off, says nothing`, async () => {
        for (const over of [{}, { visionNotMarked: ["My Relay"], vision: "off" }]) {
          const page = await open(lang, settingsPayload(lang, over), 1000);
          assert.equal(await page.locator("#imagesList .vision-not-marked").count(), 0);
          assert.deepEqual(page.errors, []);
          await page.context().close();
        }
      });
    }
  });
}
