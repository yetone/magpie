// Run with Node's test runner and Playwright on the module path; see README.md.
// A computer that follows the server (Settings > Sync > This computer) has
// what it changes in the Library replaced by the next sync (a report on X:
// a skill added there was taken for deleted by the update). Skills, MCP,
// Instructions, Hooks, Subagents and Plugins say so in one line above their
// body with a link to that setting; Memory says it while memory is synced;
// RTK says nothing; nothing is said when the computer is not following.
// The link opens Settings > Sync with the This computer row. English, Chinese,
// Chromium and WebKit, 1100 and 420px. The API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/emo";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const lib = () => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("codex", "Codex")],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [], servers: [],
});

function server(lang, sync) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib() });
    if (url.pathname === "/api/library/rtk") {
      return route.fulfill({ json: { path: `${HOME}/.local/bin/rtk`, version: "0.51.0", url: "https://www.rtk-ai.app", agents: [{ id: "codex", name: "Codex", icon: "", on: true }], gain: { commands: 42, input: 9000, saved: 8000, pct: 88.9 } } });
    }
    if (url.pathname === "/api/memory") return route.fulfill({ json: { agents: [], memories: [], digest: false, total: 0, counts: {}, scopes: [] } });
    if (url.pathname === "/api/davsync") return route.fulfill({ json: sync });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const follows = { on: true, kind: "webdav", keys: true, agents: true, library: true, memory: false, followServer: true, auto: 3 };
const cases = [
  ["skills", follows, true], ["mcp", follows, true], ["instructions", follows, true],
  ["hooks", follows, true], ["subagents", follows, true], ["plugins", follows, true],
  ["rtk", follows, false], ["memory", follows, false],
  ["memory", { ...follows, memory: true }, true],
  ["skills", { ...follows, followServer: undefined }, false],
  ["skills", { ...follows, on: false }, false],
  ["skills", { ...follows, library: false }, false],
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Library says a following computer's changes are replaced", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => { if (errors.length) console.log(errors); await browser.close(); });
    for (const lang of ["en", "zh"]) {
      for (const width of [1100, 420]) {
        for (const [tab, sync, shown] of cases) {
          await t.test(`${lang} ${width} ${tab} ${JSON.stringify(sync)}`, async () => {
            const ctx = await browser.newContext({ viewport: { width, height: 800 } });
            await ctx.addInitScript((tab) => { try { localStorage.setItem("magpie.libTab", tab); } catch {} }, tab);
            const page = await ctx.newPage();
            page.setDefaultTimeout(5000);
            page.setDefaultNavigationTimeout(60000);
            page.on("pageerror", (e) => errors.push(e.message));
            await page.route("**/*", server(lang, sync));
            await page.goto("http://magpie.test/");
            await page.locator('button[data-view="library"]').click();
            const v = page.locator("#view-library");
            await v.locator(".lib-body").waitFor();
            await page.waitForTimeout(400); // davsync's read, drawn in place
            const note = v.locator(".lib-follow");
            assert.equal(await note.count(), shown ? 1 : 0, `${lang} ${tab}: note ${shown ? "missing" : "shown"}`);
            if (shown) {
              assert((await note.textContent()).length > 20);
              const sideways = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
              assert(sideways <= 0, `page scrolls sideways by ${sideways}px`);
              if (width === 1100 && tab === "skills") {
                await note.locator("button").click();
                await page.locator("#setPage-sync").waitFor({ state: "visible" });
              }
            }
            await ctx.close();
          });
        }
      }
    }
  });
}
