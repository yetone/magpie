// Run with Node's test runner and Playwright on the module path; see README.md.
// Plugin updates: a plugin whose update waits for the reader (someone
// else's, or one pinned) puts a dot on Plugins, which goes once it's
// updated; a plugin magpie updated by itself says so on its row. English
// and Chinese; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function server(lang) {
  const installed = [
    { spec: "opencode-copilot-auth", providers: ["GitHub Copilot"], version: "0.0.7", latest: "0.0.9" },
    { spec: "@magpie-community/opencode-zed-auth", providers: ["Zed"], version: "0.1.4", latest: "0.1.4",
      autoUpdated: { package: "@magpie-community/opencode-zed-auth", from: "0.1.3", to: "0.1.4", at: "2026-09-30T08:00:00Z" } },
  ];
  const waiting = () => installed.filter((e) => e.version !== e.latest).map((e) => ({ spec: e.spec, package: e.spec, version: e.version, latest: e.latest }));
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/plugins/updates") return json({ checked: "2026-10-01T00:00:00Z", waiting: waiting(), updated: [] });
    if (url.pathname === "/api/plugins/market") return json({ listings: [], state: { bun: true, bunVersion: "1.3.0", plugins: installed } });
    if (url.pathname === "/api/plugins/upgrade") { installed[0].version = "0.0.9"; return json({}); }
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { installed: "Installed", update: "Update", auto: "Auto-updated", out: "An update for opencode-copilot-auth is out", from: /from v0\.1\.3 to v0\.1\.4/ },
  zh: { installed: "已安装", update: "更新", auto: "已自动更新", out: "opencode-copilot-auth 有新版本", from: /从 v0\.1\.3 更新到 v0\.1\.4/ },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": plugin updates", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = L[lang];
        const page = await (await browser.newContext({ viewport: { width: 980, height: 820 } })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));

        await page.goto("http://magpie.test/?view=providers");
        const nav = page.locator('#nav button[data-view="plugins"]');
        await page.waitForFunction(() => document.querySelector('#nav button[data-view="plugins"]')?.classList.contains("has-dot"));
        assert.equal(await nav.getAttribute("title"), w.out);
        // the dot is drawn, small, inside the button
        const dot = await nav.evaluate((b) => { const s = getComputedStyle(b, "::after"); return { w: s.width, pos: s.position }; });
        assert.deepEqual(dot, { w: "5px", pos: "absolute" });

        await nav.click();
        const view = page.locator("#view-plugins");
        await view.locator(".lib-tabs .opt", { hasText: w.installed }).click();
        const chip = view.locator(".pm-chip.soft", { hasText: w.auto });
        await chip.waitFor();
        assert.match(await chip.getAttribute("title"), w.from);

        // updating the waiting one takes the dot away
        await view.getByRole("button", { name: w.update, exact: true }).first().click();
        await page.waitForFunction(() => !document.querySelector('#nav button[data-view="plugins"]').classList.contains("has-dot"));
        assert.equal(await nav.getAttribute("title"), null);
        assert.deepEqual(errors, []);
      });
    }
  });
}
