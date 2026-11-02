// Run with Node's test runner and Playwright on the module path; see README.md.
// Azure OpenAI's editor asks for the resource's endpoint (no host magpie
// knows serves it): a field above the key, focused first, with the portal's
// shape as its placeholder and a hint on where to find it and that the
// model ids are the deployments' names, which magpie asks the resource for
// with the key once it is added (#1995). The add page's Models row says no
// typing is needed. Adding without an endpoint says so and sends nothing;
// with one, the endpoint goes as both chat and Responses. A saved Azure
// provider whose list is empty says whether its deployments are still being
// asked for or none came back; another provider's empty list is as it was.
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
    endpointHint: "Your resource's endpoint, from Keys and Endpoint in the Azure portal. Once added, magpie asks the resource for its deployments with this key, over its v1 API; the deployments' names are the model ids." },
];
const providers = [{ id: "openrouter", name: "OpenRouter", icon: "openai", preset: "openrouter", models: [], agents: [], key: { set: true, masked: "sk-…ab12" } }];

function server(lang, saves, list = { providers }) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ ...list, presets, excluded: [], gateway: { running: true, window: true } });
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
  en: { field: "Endpoint", hint: /Keys and Endpoint in the Azure portal\. Once added, magpie asks the resource for its deployments with this key.*deployments' names are the model ids/, needed: "Your resource's endpoint is needed", add: "Add",
    models: "Models", modelsNote: /^No need to type them: once added, magpie lists the deployments this key can call/,
    asking: "Asking your resource for its deployments with this key…", none: /^No deployments came back for this key\..*deployment's name as a model id/, empty: "The vendor's list is empty. Fetch models again, or type a model id." },
  zh: { field: "接口地址", hint: /Azure 门户的「密钥和终结点」。添加后，magpie 会用这把 key.*查询该资源的部署；部署名称就是模型 ID/, needed: "需要填写你的资源接口地址", add: "添加",
    models: "模型", modelsNote: /^不用手填：添加后 magpie 会列出这把 key 能调用的部署/,
    asking: "正在用这把 key 查询该资源的部署…", none: /^这把 key 没有查到部署。.*部署名称当作模型 ID/, empty: "供应商列表为空。重新获取模型，或输入一个模型 ID。" },
  // "Endpoints" is 端點 here, so the field is named apart from it
  "zh-TW": { field: "端點位址", hint: /Azure 入口網站的「金鑰和端點」。新增後，magpie 會用這把 key.*查詢該資源的部署；部署名稱就是模型 ID/, needed: "需要填寫你的資源端點", add: "新增",
    models: "模型", modelsNote: /^不用手動填寫：新增後 magpie 會列出這把 key 能呼叫的部署/,
    asking: "正在用這把 key 查詢該資源的部署…", none: /^這把 key 沒有查到部署。.*部署名稱當作模型 ID/, empty: "供應商列表為空。重新取得模型，或輸入一個模型 ID。" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Azure OpenAI asks for its endpoint", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW"]) {
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
        // the Models row: nothing to type, the deployments come with the key
        const note = ed.locator(".azure-models");
        assert.match(await note.textContent(), w.modelsNote);
        assert.equal(await note.locator("xpath=../preceding-sibling::label[1]").textContent(), w.models);
        const key = ed.locator("input[type=password]").first();
        // both fields in one frame: the editor is still opening, and two
        // reads apart can each catch it at a different height
        const [ey, ky] = await ed.evaluate((editor) => [
          editor.querySelector("input.endpoint"), editor.querySelector('input[type="password"]'),
        ].map((input) => input.getBoundingClientRect().top));
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
        assert.equal(await ed.locator(".azure-models").count(), 0);
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });
}

// a saved Azure OpenAI with no models yet: still asking, or asked and none
const azure = { id: "azure", name: "Azure OpenAI", icon: "azure-color", preset: "azure", models: [], agents: [], key: { set: true, masked: "az-…ab12" }, chat: "https://contoso.openai.azure.com/" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an empty Azure list says whether its deployments are still being asked for", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const cases = [
      { name: "asking", list: { fetching: true, providers: [azure] }, open: "Azure OpenAI", want: "asking" },
      { name: "asked, none", list: { providers: [{ ...azure, fetched: "2026-10-13T10:00:00Z" }] }, open: "Azure OpenAI", want: "none" },
      { name: "asked, failed", list: { fetching: true, providers: [{ ...azure, listError: "401 Unauthorized" }] }, open: "Azure OpenAI", want: "none" },
      { name: "another provider", list: { fetching: true, providers }, open: "OpenRouter", want: "empty" },
    ];
    for (const lang of ["en", "zh"]) for (const c of cases) {
      await t.test(lang + ": " + c.name, async () => {
        const w = L[lang];
        const page = await (await browser.newContext({ viewport: { width: 900, height: 700 } })).newPage();
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, [], c.list));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: c.open }).first().click();
        await page.locator(".editor .ehead b", { hasText: c.open }).waitFor();
        const all = await page.locator(".editor .hint").allTextContents();
        const want = w[c.want];
        const hit = all.some((x) => typeof want === "string" ? x === want : want.test(x));
        assert(hit, "no hint " + want + " among " + JSON.stringify(all));
        for (const other of ["asking", "none", "empty"].filter((k) => k !== c.want)) {
          const o = w[other];
          assert(!all.some((x) => typeof o === "string" ? x === o : o.test(x)), "also " + other);
        }
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });
}
