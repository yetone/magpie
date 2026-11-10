// Run with Node's test runner and Playwright on the module path; see README.md.
// xgddm88 on X: turning Claude Desktop on in magpie, every earlier project
// and chat was gone. Desktop's third-party mode runs in a userData of its
// own (Claude-3p), so the signed-in profile's history isn't shown there;
// it isn't deleted, and turning Desktop off brings it back. The row says so
// before it is turned on, and its opened row says so once it is on. In
// every language, wide and narrow, in Chromium and WebKit; nothing scrolls
// sideways.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const before = "Not connected · on magpie, Claude Desktop opens a separate profile: chats and projects from your Anthropic sign-in stay, and are back when you turn it off";
const once = "Chats and projects from your Anthropic sign-in stay in Desktop's own profile, not shown on magpie; nothing is deleted, and turning this off brings them back.";
// and how to go on with them on magpie, in Desktop's own importer
// (Jacky_yyyyyyyy on X)
const resume = "To go on with them on magpie, open Settings → Import in Desktop: it can copy in your Claude.ai chats and projects and this computer's earlier sessions, and leaves the originals as they are.";
const plain = "Not connected · {agent} uses its own settings";

function fixture(lang, wired) {
  const settings = { lang, theme: "light" };
  const desktop = {
    id: "claude-desktop", name: "Claude Desktop", path: "/test/claude_desktop_config.json", icon: "claude-color", wired,
    fields: [{ key: "provider", label: "provider", value: wired ? "magpie" : "", options: [{ value: "magpie", label: "magpie", icon: "magpie" }] }],
    models: { shown: 2, listed: 2, by: [{ name: "DeepSeek", icon: "deepseek", n: 2 }], names: ["DeepSeek V4.1 Flash", "DeepSeek V4.1 Pro"] },
  };
  // another agent not connected says what it always did
  const codex = { id: "codex", name: "Codex", icon: "generic", path: "/test/codex", fields: [{ key: "model", label: "model", value: "", options: [{ value: "deepseek/v4.1-flash", label: "DeepSeek V4.1 Flash", ref: "deepseek/v4.1-flash" }] }] };
  const state = () => ({ agents: [desktop, codex], profiles: [], settings });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state());
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    // an icon the assets don't have is a 404, as the app serves it
    const body = await fs.readFile(file).catch(() => null);
    if (body == null) return route.fulfill({ status: 404, body: "" });
    await route.fulfill({ body, contentType });
  };
}

const row = (id) => `.row.agent[data-id="${id}"]`;
const details = { en: "Details", zh: "详情", "zh-TW": "詳情", ja: "詳細", de: "Details" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: Claude Desktop says its signed-in chats and projects stay, before and after it is turned on`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const tr = (k) => page.evaluate(([k, lang]) => (lang === "en" ? k : I18N[lang]?.[k]), [k, lang]);

        // not connected: said on its row before the switch is touched
        await page.route("**/*", fixture(lang, false));
        await page.goto("http://magpie.test/?view=agents");
        await page.locator(`${row("claude-desktop")} .ag-st`).waitFor();
        for (const k of [before, once, resume]) assert.ok(await tr(k), `${lang} has ${k.slice(0, 40)}`);
        assert.equal(await page.locator(`${row("claude-desktop")} .ag-st`).textContent(), await tr(before));
        assert.equal(await page.locator(`${row("codex")} .ag-st`).textContent(), (await tr(plain)).replace("{agent}", "Codex"));
        const wide = await page.evaluate(() => document.scrollingElement.scrollWidth - document.scrollingElement.clientWidth);
        assert.ok(wide <= 0, `the page scrolls sideways by ${wide}px`);

        // connected: its opened row says it too
        await page.unroute("**/*");
        await page.route("**/*", fixture(lang, true));
        await page.goto("http://magpie.test/?view=agents");
        await page.locator(`${row("claude-desktop")} .ag-link`, { hasText: details[lang] }).click();
        const exp = page.locator(`${row("claude-desktop")} .ag-exp`);
        await exp.waitFor();
        assert.ok((await exp.textContent()).includes((await tr(once)) + " " + (await tr(resume))), await exp.textContent());
        assert.deepEqual(errors, []);
      });
    }
  }
}
