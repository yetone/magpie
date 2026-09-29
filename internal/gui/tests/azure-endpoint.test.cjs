// Run with Node's test runner and Playwright on the module path; see README.md.
// Azure OpenAI's editor asks for the resource's endpoint (no host magpie
// knows serves it): a field above the key, focused first, with the portal's
// shape as its placeholder and a hint on where to find it and that the
// model ids are the deployments' names. Adding without one says so and
// sends nothing; with one, the endpoint goes as both chat and Responses.
// Nothing moves the page. English and Chinese; no backend, the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const presets = [
  { id: "openai", name: "OpenAI", icon: "openai", kind: "vendor", chat: "https://api.openai.com/v1", added: false },
  { id: "azure", name: "Azure OpenAI", icon: "azure-color", kind: "vendor", added: false, catalog: "azure, openai",
    note: "your resource's endpoint and key", endpoint: "https://<resource>.openai.azure.com",
    endpointHint: "Your resource's endpoint, from Keys and Endpoint in the Azure portal. magpie asks its v1 API; the model ids are your deployments' names." },
];
const providers = [{ id: "openrouter", name: "OpenRouter", icon: "openai", preset: "openrouter", models: [], agents: [], key: { set: true, masked: "sk-…ab12" } }];

function server(lang, saves) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers, presets, excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/provider/save") {
      saves.push(route.request().postDataJSON());
      return json({ providers, presets, excluded: [], gateway: { running: true, window: true } });
    }
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { field: "Endpoint", hint: /Keys and Endpoint in the Azure portal.*deployments' names/, needed: "Your resource's endpoint is needed", add: "Add" },
  zh: { field: "终结点", hint: /Azure 门户的「密钥和终结点」.*部署名称/, needed: "需要填写你的资源终结点", add: "添加" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Azure OpenAI asks for its endpoint", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = L[lang];
        const saves = [];
        const page = await (await browser.newContext({ viewport: { width: 900, height: 700 } })).newPage();
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, saves));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator("#addProvider").click();
        const sheet = page.locator("#addSheet");
        await sheet.locator(".tile .n", { hasText: /^Azure OpenAI$/ }).click();
        const ed = page.locator(".editor.new");
        await ed.locator(".ehead b", { hasText: "Azure OpenAI" }).waitFor();

        // the field, above the key, focused first; the OpenAI preset has none
        const label = ed.locator("label", { hasText: new RegExp("^" + w.field + "$") });
        assert.equal(await label.count(), 1);
        const endpoint = ed.locator("input.endpoint");
        assert.equal(await endpoint.getAttribute("placeholder"), "https://<resource>.openai.azure.com");
        assert.equal(await endpoint.getAttribute("type"), "url");
        assert.match(await endpoint.locator("xpath=following-sibling::div[contains(@class,'hint')]").textContent(), w.hint);
        await page.waitForFunction(() => document.activeElement?.classList.contains("endpoint"));
        const key = ed.locator("input[type=password]").first();
        const [ey, ky] = [await endpoint.boundingBox(), await key.boundingBox()].map((b) => b.y);
        assert(ey < ky, "the endpoint above the key");

        // no endpoint: said, nothing sent, the page where it was
        await key.fill("az-key");
        const y = await page.evaluate(() => document.scrollingElement.scrollTop);
        await ed.locator(".bar button.primary", { hasText: w.add }).click();
        assert.equal(await ed.locator(".editor-error").textContent(), w.needed);
        assert.equal(saves.length, 0);
        assert.equal(await page.evaluate(() => document.activeElement?.classList.contains("endpoint")), true);
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), y);

        // with one: sent as both chat and Responses, with the key
        await endpoint.fill("https://contoso.openai.azure.com/");
        await ed.locator(".bar button.primary", { hasText: w.add }).click();
        await page.waitForFunction(() => !document.querySelector(".editor.new"));
        assert.equal(saves.length, 1);
        const s = saves[0];
        assert.equal(s.preset, "azure");
        assert.equal(s.key, "az-key");
        assert.equal(s.chat, "https://contoso.openai.azure.com/");
        assert.equal(s.responses, "https://contoso.openai.azure.com/");

        // another preset's editor has no endpoint field
        await page.locator("#addProvider").click();
        await sheet.locator(".tile .n", { hasText: /^OpenAI$/ }).click();
        await ed.locator(".ehead b", { hasText: "OpenAI" }).waitFor();
        assert.equal(await ed.locator("input.endpoint").count(), 0);
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });
}
