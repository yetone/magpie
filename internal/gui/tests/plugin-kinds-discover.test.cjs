// Run with Node's test runner and Playwright on the module path; see README.md.
// Every kind of plugin has its place on the Plugins page: Discover lists the
// market's tools plugins and page plugins in sections of their own, with the
// Tools and Page chips, never under Subscriptions; the intro's "Write a
// plugin" link opens the plugin docs in the window's language; and an
// installed plugin that only gives tools wears the Tools chip, not Provider.
// Every language, both engines, at a narrow width too; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const CALC = "/Users/x/pl/calc";
const state = { bun: true, bunVersion: "1.3.0", plugins: [
  { spec: CALC, providers: [], moved: [], inMagpieOnly: true, tools: [{ name: "add", mcp: "calc_add" }] },
] };
const listings = [
  { package: "@magpie-community/provider-x", name: "X plan", community: true, providers: ["x"], summary: { en: "Signs in to X" }, npm: { version: "0.1.0" } },
  { package: "@magpie-community/tools-calc", name: "Calc tools", kind: "tools", community: true, summary: { en: "Exact arithmetic" }, npm: { version: "0.1.0" } },
  { package: "@magpie-community/page-glance", name: "Glance page", kind: "page", community: true, summary: { en: "Today's usage" }, npm: { version: "0.1.0" } },
];
const opened = [];

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/plugins") return json(state);
    if (url.pathname === "/api/plugins/listings") return json({ listings });
    if (url.pathname === "/api/plugins/market") return json({ listings, state });
    if (url.pathname === "/api/open") { opened.push(route.request().postDataJSON()?.url); return json({}); }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { installed: "Installed", subs: "Subscriptions", tls: "Tools for agents", pgs: "Pages", tool: "Tools", page: "Page", provider: "Provider", write: "Write a plugin", docs: "https://usemagpie.ai/docs/plugins" },
  zh: { installed: "已安装", subs: "订阅", tls: "给 Agent 的工具", pgs: "页面", tool: "工具", page: "页面", provider: "供应商", write: "编写插件", docs: "https://usemagpie.ai/docs/zh/plugins" },
  "zh-TW": { installed: "已安裝", subs: "訂閱", tls: "給 Agent 的工具", pgs: "頁面", tool: "工具", page: "頁面", write: "編寫外掛", docs: "https://usemagpie.ai/docs/zh/plugins" },
  ja: { installed: "インストール済み", subs: "サブスクリプション", tls: "エージェント向けツール", pgs: "ページ", tool: "ツール", page: "ページ", write: "プラグインを書く", docs: "https://usemagpie.ai/docs/ja/plugins" },
  de: { installed: "Installiert", subs: "Abos", tls: "Werkzeuge für Agenten", pgs: "Seiten", tool: "Tools", page: "Seite", write: "Eine Erweiterung schreiben", docs: "https://usemagpie.ai/docs/plugins" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": tools and page plugins in Discover, and the way to write one", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) for (const width of [980, 420]) {
      await t.test(lang + " " + width, async () => {
        const w = L[lang];
        const page = await (await browser.newContext({ viewport: { width, height: 820 } })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/?view=plugins");
        const view = page.locator("#view-plugins");
        const kinds = (r) => r.locator(".pm-chip.kind").allInnerTexts().then((a) => a.map((x) => x.trim()));
        const sec = (name) => view.locator(".pm-sec").filter({ has: page.locator(".pm-sechead h3", { hasText: new RegExp("^" + name + "$") }) });

        // each kind in its own section, wearing its own chip
        await sec(w.tls).waitFor();
        const subs = await sec(w.subs).locator(".pm-card").allInnerTexts();
        assert.equal(subs.length, 1);
        assert.ok(subs[0].includes("X plan"));
        const tl = sec(w.tls).locator(".pm-card");
        assert.equal(await tl.count(), 1);
        assert.ok((await tl.innerText()).includes("Calc tools"));
        assert.deepEqual(await kinds(tl), [w.tool]);
        assert.ok(await tl.locator(".pm-chip.kind.tl").count());
        const pg = sec(w.pgs).locator(".pm-card");
        assert.equal(await pg.count(), 1);
        assert.ok((await pg.innerText()).includes("Glance page"));
        assert.deepEqual(await kinds(pg), [w.page]);

        // the way to write one: the docs in the window's language, opened outside
        const how = view.locator(".pm-foot .pm-write");
        assert.equal((await how.innerText()).trim(), w.write);
        assert.equal(await how.getAttribute("href"), w.docs);
        opened.length = 0;
        await how.click();
        for (let i = 0; i < 50 && !opened.length; i++) await page.waitForTimeout(20);
        assert.deepEqual(opened, [w.docs]);
        assert.equal(page.url(), "http://magpie.test/?view=plugins", "the window stays on magpie");

        const over = await page.evaluate(() => document.scrollingElement.scrollWidth - document.scrollingElement.clientWidth);
        assert.ok(over <= 0, "page scrolls sideways by " + over);

        // installed: a plugin with tools alone is Tools, not Provider
        await view.page().locator(":is(.lib-tabs .opt, .nav-s):visible", { hasText: w.installed }).click();
        const calc = view.locator(".pm-row").filter({ has: page.locator(".name", { hasText: "calc" }) });
        await calc.waitFor();
        assert.deepEqual(await kinds(calc), [w.tool]);
        if (w.provider) assert.ok(!(await kinds(calc)).includes(w.provider));
        if (process.env.ARTIFACT_DIR) await view.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `plugin-kinds-${engine}-${lang}-${width}.png`) });
        assert.deepEqual(errors, []);
      });
    }
  });
}
