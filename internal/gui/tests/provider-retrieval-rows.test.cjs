// Run with Node's test runner and Playwright on the module path; see README.md.
// A provider the user added that lists chat models and embedding and
// rerank models too (@rfiacne on X, 11-20): its editor lists the embedding
// and rerank ones apart, read only, as they need no pick; before, they
// were nowhere to be seen once the provider had a chat model. A provider
// without them shows neither row. Every language, both engines, wide and
// narrow; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = require("./strings.cjs").strings();
const EMBED = "Embedding models", RERANK = "Rerank models";
const say = (lang, k) => lang === "en" ? k : I18N[lang][k];

const relay = { id: "sf", name: "Silicon Relay", icon: "openai", models: [{ id: "deepseek-ai/DeepSeek-V3" }], agents: [], key: { set: true, masked: "sk-…ab12" }, chat: "http://sf.test/v1", embedIds: ["BAAI/bge-m3", "Qwen/Qwen3-Embedding-8B"], rerankIds: ["BAAI/bge-reranker-v2-m3"] };
const plain = { id: "plain", name: "Plain Relay", icon: "openai", models: [{ id: "gpt-5.5" }], agents: [], key: { set: true, masked: "sk-…cd34" }, chat: "http://plain.test/v1" };

function server(lang) {
  const providers = { providers: [relay, plain], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    try { await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

test("each language names both rows", () => {
  for (const lang of ["zh", "zh-TW", "ja", "de"]) for (const k of [EMBED, RERANK]) assert(I18N[lang][k], lang + " " + k);
});

// the chips under the label named label, in the open editor
const chipsUnder = (page, label) => page.evaluate((label) => {
  const l = [...document.querySelectorAll(".editor label")].find((e) => e.textContent === label);
  const box = l?.nextElementSibling?.querySelector(".mchips.still") ?? l?.nextElementSibling;
  return l ? [...(box?.querySelectorAll(".mchip") ?? [])].map((c) => c.textContent) : null;
}, label);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an added provider's embedding and rerank models in its editor", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [900, 420]) {
      await t.test(`${lang}, ${width}px`, async () => {
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));

        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Silicon Relay" }).first().click();
        await page.locator(".editor .ehead b", { hasText: "Silicon Relay" }).waitFor();
        assert.deepEqual(await chipsUnder(page, say(lang, EMBED)), ["BAAI/bge-m3", "Qwen/Qwen3-Embedding-8B"]);
        assert.deepEqual(await chipsUnder(page, say(lang, RERANK)), ["BAAI/bge-reranker-v2-m3"]);
        // the chips stay inside the editor at a narrow width
        const over = await page.evaluate(() => {
          const ed = document.querySelector(".editor").getBoundingClientRect();
          return [...document.querySelectorAll(".editor .mchips.still .mchip")].filter((c) => c.getBoundingClientRect().right > ed.right + 1).map((c) => c.textContent);
        });
        assert.deepEqual(over, []);

        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Plain Relay" }).first().click();
        await page.locator(".editor .ehead b", { hasText: "Plain Relay" }).waitFor();
        assert.equal(await chipsUnder(page, say(lang, EMBED)), null);
        assert.equal(await chipsUnder(page, say(lang, RERANK)), null);
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });
}
