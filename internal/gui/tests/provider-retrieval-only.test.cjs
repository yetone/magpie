// Run with Node's test runner and Playwright on the module path; see README.md.
// A vendor that lists only embedding and rerank models (heatonz on Discord,
// 11-05): there is nothing for an agent to chat with, and nothing to pick,
// as they are served on /v1/embeddings and /v1/rerank as listed. The add
// form's Fetch models, and a saved provider's editor, say so rather than
// that the list is empty; a list that is empty still says it is. Every
// language; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = require("./strings.cjs").strings();
const ONLY = "Only embedding and rerank models are listed. They need no pick: ask them on /v1/embeddings and /v1/rerank.";
const ADD_EMPTY = "The vendor's list is empty. Type the model ids instead.";
const SAVED_EMPTY = "The vendor's list is empty. Fetch models again, or type a model id.";
const say = (lang, k) => lang === "en" ? k : I18N[lang][k];

const relay = (retrieval) => ({ id: "relay", name: "Embed Relay", icon: "openai", models: [], agents: [], key: { set: true, masked: "sk-…ab12" }, chat: "http://embed.test/v1", ...(retrieval ? { retrieval } : {}) });

function server(lang, list, retrieval) {
  const providers = { providers: list, presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/provider/list") return json({ models: [], ...(retrieval ? { retrieval } : {}) });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    try { await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

test("each language says it", () => {
  for (const lang of ["zh", "zh-TW", "ja", "de"]) assert(I18N[lang][ONLY], lang);
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a list of embedding and rerank models alone", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const retrieval of [2, 0]) {
      await t.test(`${lang}, ${retrieval} retrieval`, async () => {
        const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, [relay(retrieval)], retrieval));
        await page.goto("http://magpie.test/?view=providers");

        // the add form's Fetch models
        await page.locator("#addProvider").click();
        await page.locator("#addSheet .custom-foot .custom").click();
        const ed = page.locator(".editor.new");
        await ed.locator(".add-models").waitFor();
        await ed.locator('input[type="url"][placeholder="https://…/v1"]').first().fill("http://embed.test/v1");
        await ed.locator(".add-models .detect-row button").click();
        // past "Asking the vendor…", the one hint the answer leaves
        const hint = ed.locator(".add-models-list .hint");
        await page.waitForFunction(() => !/…$/.test(document.querySelector(".editor.new .add-models-list .hint")?.textContent ?? "…"));
        assert.deepEqual(await hint.allTextContents(), [say(lang, retrieval ? ONLY : ADD_EMPTY)]);

        // the saved provider's editor
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Embed Relay" }).first().click();
        await page.locator(".editor .ehead b", { hasText: "Embed Relay" }).waitFor();
        const all = await page.locator(".editor .hint").allTextContents();
        const want = say(lang, retrieval ? ONLY : SAVED_EMPTY), not = say(lang, retrieval ? SAVED_EMPTY : ONLY);
        assert(all.includes(want), `no ${JSON.stringify(want)} among ${JSON.stringify(all)}`);
        assert(!all.includes(not), "also " + not);
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });
}
