// Run with Node's test runner and Playwright on the module path; see README.md.
// The sidebar's list of a page's tabs (app.js navSubs) gives each tab a
// mark of its own (NAV_SUB_IC), as the pages above it have: Library,
// Usage, Plugins and Settings. Every row has one, they stand in one
// column, and the name beside it is whole (German's longest names were
// cut short with an ellipsis before the marks, and still are). Every
// language, Chromium and WebKit. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/aimer";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const lib = {
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("pi", "Pi")],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [], servers: [],
};

function serve(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { url: "http://127.0.0.1:3425", running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/library") return json(lib);
    if (url.pathname === "/api/plugins") return json({ bun: true, bunVersion: "1.3.0", plugins: [], movable: [] });
    if (url.pathname === "/api/plugins/listings") return json({ listings: [] });
    if (url.pathname === "/api/plugins/npm") return json({ npm: {} });
    if (url.pathname === "/api/plugins/github") return json({ repos: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// how many rows each page's list has here: Library with Pi has its Pi tab
const pages = { library: 10, usage: 4, plugins: 3, settings: 8 };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: every row of the sidebar's tab lists has its own mark`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) await t.test(lang, async () => {
      const context = await browser.newContext({ viewport: { width: 1100, height: 900 } });
      t.after(() => context.close());
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/");
      await page.waitForSelector("#nav button.on");
      // the marks come from navicons.js, imported with the first list
      assert.ok(await page.evaluate(() => document.body.classList.contains("side-nav")), "a sidebar");
      for (const [view, n] of Object.entries(pages)) {
        await page.locator(view === "settings" ? "#prefs" : `#nav button[data-view="${view}"]`).click();
        await page.waitForFunction(() => window.NAV_SUB_IC);
        await page.waitForFunction(([v, n]) => document.querySelectorAll(`.nav-sub[data-view="${v}"]:not(.leaving) .nav-s`).length >= n, [view, n]);
        await page.waitForFunction(() => [...document.querySelectorAll(".nav-sub, .nav-sub *")].every((e) => e.getAnimations().every((a) => a.playState !== "running")));
        const rows = await page.evaluate((v) => [...document.querySelectorAll(`.nav-sub[data-view="${v}"]:not(.leaving) .nav-s`)].map((s) => {
          const ic = s.querySelector(":scope > svg")?.getBoundingClientRect(), name = s.querySelector(":scope > span");
          const nb = name.getBoundingClientRect();
          return { text: name.textContent, ic: ic && { left: ic.left, right: ic.right, w: ic.width, mid: ic.top + ic.height / 2 }, nameLeft: nb.left, mid: nb.top + nb.height / 2, whole: name.scrollWidth <= name.clientWidth + 1, over: name.scrollWidth - name.clientWidth, sw: s.getBoundingClientRect().width };
        }), view);
        for (const r of rows) {
          assert.ok(r.ic, `${view}: ${r.text} has a mark`);
          assert.equal(Math.round(r.ic.w), 14, `${view}: ${r.text}'s mark is 14px`);
          assert.ok(r.ic.right <= r.nameLeft, `${view}: ${r.text}'s mark is before its name`);
          assert.ok(Math.abs(r.ic.mid - r.mid) <= 1.5, `${view}: ${r.text}'s mark is level with its name`);
          if (lang !== "de") assert.ok(r.whole, `${view}: ${r.text} is whole (${r.over}px over, row ${r.sw})`);
        }
        assert.equal(new Set(rows.map((r) => Math.round(r.ic.left))).size, 1, `${view}: the marks stand in one column`);
      }
      assert.deepEqual(errors, []);
    });
  });
}
