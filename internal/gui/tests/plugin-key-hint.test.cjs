// Run with Node's test runner and Playwright on the module path; see README.md.
// A plugin's "api" way to sign in titles its key's field with its label,
// as OpenCode's dialog does, and hints at the key with its placeholder
// (Lemon on Discord); a way labelled only "API key" keeps "<name> API
// key". The provider's own icon (a picture magpie kept) shows on its row.
// English and Chinese; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const plugin = { id: "lemon", pid: "lemon", name: "Lemon", icon: "file:0123456789abcdef.svg", spec: "opencode-lemon-auth", signedIn: false, models: 2,
  methods: [{ type: "api", label: "Lemon API key (from lemon.example/keys)", placeholder: "sk-lemon-…" }, { type: "api", label: "API key" }] };

function server(lang, asked) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [{ id: "openai", name: "OpenAI", icon: "openai", preset: "openai", models: [], agents: [], key: { set: true, masked: "sk-…ab12" } }], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [plugin] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/icons/0123456789abcdef.svg") return route.fulfill({ contentType: "image/svg+xml", body: '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><circle cx="8" cy="8" r="7" fill="#fd0"/></svg>' });
    if (url.pathname === "/api/plugin-signin/prompt") { asked.push(["prompt", route.request().postDataJSON()]); return json({ prompt: null, inputs: {} }); }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { section: "From plugins", how: "How do you sign in to Lemon?", generic: "Lemon API key" },
  zh: { section: "来自插件", how: "用哪种方式登录 Lemon？", generic: "Lemon API Key" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a plugin's key field", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = L[lang];
        const page = await (await browser.newContext({ viewport: { width: 900, height: 800 } })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [], asked = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, asked));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator("#addProvider").click();
        const sheet = page.locator("#addSheet");
        await sheet.locator(".kind b", { hasText: w.section }).waitFor();
        const row = sheet.locator('.tile[data-pick="Lemon"]');
        // the plugin's own picture
        const img = row.locator('.ic img[src="/api/icons/0123456789abcdef.svg"]');
        await img.waitFor();
        await page.waitForFunction((e) => e.complete && e.naturalWidth > 0, await img.elementHandle());

        await row.click();
        const box = sheet.locator(".signing");
        await box.locator(".n", { hasText: w.how }).waitFor();
        await box.locator("button", { hasText: "Lemon API key (from lemon.example/keys)" }).click();
        const key = box.getByLabel("Lemon API key (from lemon.example/keys)", { exact: true });
        await key.waitFor();
        assert.equal(await box.locator(".n").first().innerText(), "Lemon API key (from lemon.example/keys)");
        assert.equal(await key.getAttribute("placeholder"), "sk-lemon-…");
        assert.equal(await key.getAttribute("type"), "password");
        if (process.env.ARTIFACT_DIR) await box.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `plugin-key-hint-${engine}-${lang}.png`) });

        // a way that only says "API key": the provider's name, no hint
        await box.locator("button.text:not(.primary)").last().click();
        await row.click();
        await box.locator("button", { hasText: /^API key$/ }).click();
        const plain = box.getByLabel(w.generic, { exact: true });
        await plain.waitFor();
        assert.equal(await plain.getAttribute("placeholder") ?? "", "");

        assert.deepEqual(errors, []);
      });
    }
  });
}
