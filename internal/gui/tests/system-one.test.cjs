// Run with Node's test runner and Playwright on the module path; see README.md.
// Bailian's decision model (#647): its editor picks a workspace's region or
// the Token Plan, asks for the workspace ID and fills the System One
// address in with it; Add without one says so and sends nothing. A custom
// provider can be a System One API: Detect APIs posts its address as
// decide, and the Add sends it. English and Chinese; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const ws = (r) => `https://{WorkspaceId}.${r}.maas.aliyuncs.com/compatible-mode/v1`;
const presets = [
  { id: "typesafe", name: "TypeSafe Jev", icon: "typesafe", kind: "vendor", decide: "https://api.typesafe.ai/v1", note: "routes groups · picks model and effort", added: false },
  { id: "bailian-decision", name: "Bailian Decision Model", icon: "qwen-color", kind: "vendor", decide: ws("cn-beijing"), note: "routes groups · picks model and effort", added: false,
    regionLabel: "Plan", regions: [
      { id: "cn-beijing", name: "Workspace · Beijing", decide: ws("cn-beijing") },
      { id: "ap-southeast-1", name: "Workspace · Singapore", decide: ws("ap-southeast-1") },
      { id: "token-plan", name: "Token Plan · Beijing", decide: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1" },
    ] },
];

const providers = [{ id: "openrouter", name: "OpenRouter", icon: "openai", preset: "openrouter", models: [], agents: [], key: { set: true, masked: "sk-…ab12" } }];

function server(lang, posted) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    const all = { providers, presets, excluded: [], gateway: { running: true, window: true } };
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(all);
    if (url.pathname.startsWith("/api/provider/")) {
      const body = route.request().postDataJSON();
      const action = url.pathname.slice("/api/provider/".length);
      posted.push({ action, body });
      if (action === "detect") return json({ results: [{ protocol: "decide", ok: true, status: 200, ms: 40, model: body.model, base: body.decide.replace(/\/systemone$/, "") }] });
      return json(all);
    }
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { ws: "Workspace ID", ep: "System One endpoint", sg: "Workspace · Singapore", tp: "Token Plan · Beijing", add: "Add", needed: "Give the workspace ID your API key belongs to, or pick the Token Plan", custom: "Custom provider", detect: "Detect APIs", use: "Use these" },
  zh: { ws: "业务空间 ID", ep: "System One 端点", sg: "业务空间 · 新加坡", tp: "Token Plan · 北京", add: "添加", needed: "请填写 API Key 所属的业务空间 ID，或选择 Token Plan", custom: "自定义供应商", detect: "检测协议", use: null },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Bailian's decision model and a custom System One", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = L[lang];
        const posted = [];
        const saves = () => posted.filter((x) => x.action === "save").map((x) => x.body);
        const page = await (await browser.newContext({ viewport: { width: 900, height: 760 } })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posted));
        await page.goto("http://magpie.test/?view=providers");
        const sheet = page.locator("#addSheet");
        const ed = page.locator(".editor.new");
        const open = async (name) => {
          await page.locator("#addProvider").click();
          await sheet.locator(".tile .n", { hasText: new RegExp("^" + name + "$") }).click();
          await ed.locator(".ehead b", { hasText: name }).waitFor();
        };

        // the workspace: asked for, and the address filled in with it
        await open("Bailian Decision Model");
        const wsIn = ed.locator("input.workspace-id");
        assert.equal(await wsIn.isVisible(), true);
        assert.equal(await ed.locator("label", { hasText: new RegExp("^" + w.ws + "$") }).count(), 1);
        const epLabel = ed.locator("label", { hasText: new RegExp("^" + w.ep + "$") });
        assert.equal(await epLabel.count(), 1);
        const ep = ed.locator("input[type=url]").last();
        assert.equal(await ep.inputValue(), ws("cn-beijing"));
        await ed.locator("input[type=password]").first().fill("sk-test");
        await ed.locator(".bar button.primary", { hasText: w.add }).click();
        assert.equal(await ed.locator(".editor-error").textContent(), w.needed);
        assert.equal(saves().length, 0);
        await wsIn.fill("ws-abc");
        assert.equal(await ep.inputValue(), "https://ws-abc.cn-beijing.maas.aliyuncs.com/compatible-mode/v1");
        await ed.locator(".segs.regions .opt", { hasText: w.sg }).click();
        assert.equal(await ep.inputValue(), "https://ws-abc.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1");
        await ed.locator(".bar button.primary", { hasText: w.add }).click();
        await page.waitForFunction(() => !document.querySelector(".editor.new"));
        assert.equal(saves().length, 1);
        assert.equal(saves()[0].preset, "bailian-decision");
        assert.equal(saves()[0].key, "sk-test");
        assert.equal(saves()[0].decide, "https://ws-abc.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1");

        // the Token Plan has no workspace
        await open("Bailian Decision Model");
        await ed.locator(".segs.regions .opt", { hasText: w.tp }).click();
        assert.equal(await ed.locator("input.workspace-id").isVisible(), false);
        assert.equal(await ed.locator("input[type=url]").last().inputValue(), "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1");
        await ed.locator("input[type=password]").first().fill("sk-sp-test");
        await ed.locator(".bar button.primary", { hasText: w.add }).click();
        await page.waitForFunction(() => !document.querySelector(".editor.new"));
        assert.equal(saves()[1].decide, "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1");

        // Jev's preset keeps its own field and no workspace
        await open("TypeSafe Jev");
        assert.equal(await ed.locator("input.workspace-id").isVisible(), false);
        assert.equal(await ed.locator("label", { hasText: lang === "zh" ? /^Jev 端点$/ : /^Jev endpoint$/ }).count(), 1);

        // a custom System One: probed at its address, saved as decide
        await page.goto("http://magpie.test/?view=providers");
        await page.locator("#addProvider").click();
        await sheet.locator(".row-head .custom").click();
        await ed.waitFor();
        await ed.locator("input").first().fill("My Decider");
        await ed.locator(".segs .opt", { hasText: "System One" }).click();
        await ed.locator("input[type=url]").first().fill("https://decide.example.com/v1/systemone");
        await ed.locator("input[type=password]").first().fill("sk-1");
        await ed.locator("input.detect-model").fill("my-decider");
        await ed.locator(".detect button", { hasText: new RegExp("^" + w.detect + "$") }).click();
        await ed.locator(".detect-out .ep[data-api=decide] .res.ok").waitFor();
        const d = posted.find((x) => x.action === "detect").body;
        assert.equal(d.decide, "https://decide.example.com/v1/systemone");
        assert.equal(d.model, "my-decider");
        await ed.locator(".detect-acts button").first().click();
        assert.equal(await ed.locator("input[type=url]").first().inputValue(), "https://decide.example.com/v1");
        await ed.locator(".bar button.primary", { hasText: w.add }).click();
        await page.waitForFunction(() => !document.querySelector(".editor.new"));
        const c = saves()[2];
        assert.equal(c.name, "My Decider");
        assert.equal(c.decide, "https://decide.example.com/v1");
        assert.equal(c.chat, "");
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });
}
