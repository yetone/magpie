// Run with Node's test runner and Playwright on the module path; see README.md.
// A vendor that lists only embedding and rerank models (heatonz on Discord,
// 11-05): there is nothing for an agent to chat with, and nothing to pick,
// as they are served on /v1/embeddings and /v1/rerank as listed. The add
// form's Fetch models lists each of them by kind, with the path it is
// asked on (solider1394 on X, 11-22: a bare notice read as no models, while
// Voyage has fifteen), under the chat models when there are some too. A
// saved provider's editor says there is none to chat with and lists them
// below, each kind with its path and a model id to send; a list that is
// empty still says it is. Every language; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = require("./strings.cjs").strings();
const ONLY = "No models to chat with: its embedding and rerank models are below.";
const ADD_EMPTY = "The vendor's list is empty. Type the model ids instead.";
const SAVED_EMPTY = "The vendor's list is empty. Fetch models again, or type a model id.";
const EMBED = "Embedding models", RERANK = "Rerank models";
const ON = "Call them on {path}", ON_AS = "Call them on {path}, e.g. as {model}";
const say = (lang, k, v = {}) => (lang === "en" ? k : I18N[lang][k]).replace(/\{(\w+)\}/g, (_, n) => v[n]);
const EMBED_IDS = ["voyage-4-large", "voyage-4"], RERANK_IDS = ["rerank-2.5"];
const ids = (on) => on ? { embedIds: EMBED_IDS, rerankIds: RERANK_IDS } : {};

const relay = (retrieval) => ({ id: "relay", name: "Embed Relay", icon: "openai", models: [], agents: [], key: { set: true, masked: "sk-…ab12" }, chat: "http://embed.test/v1", ...(retrieval ? { retrieval, ...ids(1) } : {}) });

function server(lang, list, retrieval, chat = []) {
  const providers = { providers: list, presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/provider/list") return json({ models: chat, ...ids(retrieval) });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    try { await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

test("each language says it", () => {
  for (const lang of ["zh", "zh-TW", "ja", "de"]) for (const k of [ONLY, ON, ON_AS, EMBED, RERANK]) assert(I18N[lang][k], lang + " " + k);
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a list of embedding and rerank models alone", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const [retrieval, chat] of [[3, []], [0, []], [3, [{ id: "gpt-5.5" }]]]) {
      await t.test(`${lang}, ${retrieval} retrieval, ${chat.length} chat`, async () => {
        const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, [relay(retrieval)], retrieval, chat));
        await page.goto("http://magpie.test/?view=providers");

        // the add form's Fetch models
        await page.locator("#addProvider").click();
        await page.locator("#addSheet .row-head .custom").click();
        const ed = page.locator(".editor.new");
        await ed.locator(".add-models").waitFor();
        await ed.locator('input[type="url"][placeholder="https://…/v1"]').first().fill("http://embed.test/v1");
        await ed.locator(".add-models .detect-row button").click();
        // past "Asking the vendor…": the kinds listed, or the list empty
        const list = ed.locator(".add-models-list");
        await page.waitForFunction(() => { const h = document.querySelector(".editor.new .add-models-list > .hint"); return !h || !/…$/.test(h.textContent); });
        if (retrieval) await list.locator(".rgroup").nth(1).waitFor();
        const groups = await list.locator(".rgroup").evaluateAll((gs) => gs.map((g) => [g.querySelector(".rkind").textContent, [...g.querySelectorAll(".mchip")].map((c) => c.textContent), g.querySelector(".hint").textContent]));
        assert.deepEqual(groups, retrieval ? [
          [say(lang, EMBED), EMBED_IDS, say(lang, ON, { path: "/v1/embeddings" })],
          [say(lang, RERANK), RERANK_IDS, say(lang, ON, { path: "/v1/rerank" })],
        ] : []);
        const hints = await list.locator(":scope > .hint").allTextContents();
        assert.deepEqual(hints, retrieval || chat.length ? [] : [say(lang, ADD_EMPTY)]);
        // with chat models too, the kinds come under their chips
        if (chat.length) assert.equal(await list.evaluate((l) => [...l.children].findIndex((c) => c.classList.contains("mchips")) < [...l.children].findIndex((c) => c.classList.contains("rgroup"))), true);

        // the saved provider's editor
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Embed Relay" }).first().click();
        await page.locator(".editor .ehead b", { hasText: "Embed Relay" }).waitFor();
        if (retrieval) await page.locator(".editor label", { hasText: say(lang, RERANK) }).waitFor();
        const all = await page.locator(".editor .hint").allTextContents();
        const want = say(lang, retrieval ? ONLY : SAVED_EMPTY), not = say(lang, retrieval ? SAVED_EMPTY : ONLY);
        assert(all.includes(want), `no ${JSON.stringify(want)} among ${JSON.stringify(all)}`);
        assert(!all.includes(not), "also " + not);
        // each kind's row: its ids, the path, and a model id to send there
        const rows = await page.evaluate(() => [...document.querySelectorAll(".editor label")].filter((l) => l.nextElementSibling?.querySelector(".mchips.still")).map((l) => [l.textContent, [...l.nextElementSibling.querySelectorAll(".mchip")].map((c) => c.textContent), l.nextElementSibling.querySelector(".hint")?.textContent]));
        assert.deepEqual(rows, retrieval ? [
          [say(lang, EMBED), EMBED_IDS, say(lang, ON_AS, { path: "/v1/embeddings", model: "relay/voyage-4-large" })],
          [say(lang, RERANK), RERANK_IDS, say(lang, ON_AS, { path: "/v1/rerank", model: "relay/rerank-2.5" })],
        ] : []);
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });
}
