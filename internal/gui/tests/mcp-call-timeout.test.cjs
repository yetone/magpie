// Run with Node's test runner and Playwright on the module path; see README.md.
// steinxia on X: every MCP server behind magpie's router had the same 50s
// for a tool call, and none could be given its own. The server's editor
// has a "Tool call timeout" in seconds under Advanced, folded until it is
// set, digits only, saved with the server as callTimeout and shown again
// with the fold open; emptied, it saves 0 (magpie's own). Every language,
// Chromium and WebKit, 1100 and 390 wide; nothing scrolls sideways. No
// backend: the API is faked here.
const assert = require("node:assert/strict");
const { reach } = require("./reader.cjs");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = require("./strings.cjs").strings(); // i18n.js's and the plugins'
const tr = (lang, s, vars = {}) => ((lang === "en" ? s : I18N[lang][s]) || s).replace(/\{(\w+)\}/g, (_, k) => vars[k]);
const HOME = "/Users/xv";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });


function server(lang, calls, agents, hub) {
  const lib = {
    mcpHub: hub, hubPath: "/mcp",
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
    agents,
    instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [],
    servers: [{ name: "slow", transport: "stdio", command: "npx", args: ["-y", "slow-mcp"], agents: agents.map((a) => a.id) }],
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib });
    if (url.pathname === "/api/library/mcp/check") return route.fulfill({ json: { servers: {} } });
    if (url.pathname === "/api/library/servers/save") {
      const { old, server: s } = req.postDataJSON();
      calls.push(s.name + " " + s.callTimeout);
      const i = lib.servers.findIndex((x) => x.name === old);
      lib.servers[i] = { ...lib.servers[i], ...s };
      if (!s.callTimeout) delete lib.servers[i].callTimeout;
      return route.fulfill({ json: { ...lib, result: { changed: agents.map((a) => a.id) } } });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an MCP server's tool call timeout, under Advanced", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1100, 390]) {
        await t.test(`${lang} ${width}`, async () => {
          const w = (s, v) => tr(lang, s, v);
          assert(lang === "en" || I18N[lang]["Tool call timeout"], `no ${lang} for "Tool call timeout"`);
          const calls = [];
          const ctx = await browser.newContext({ viewport: { width, height: 800 } });
          await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
          const page = await ctx.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang, calls, [agent("claude", "Claude Code")], true));
          await page.goto("http://magpie.test/");
          await page.locator('button[data-view="library"]').click();
          const name = page.locator("#view-library .lib-server .name", { hasText: "slow" });
          const ed = page.locator("#modal .lib-editor");
          const fold = ed.locator("details.lib-servermore");
          const wait = ed.getByLabel(w("Tool call timeout"), { exact: true });
          // at the name text: the row's router switch sits at its end
          const open = async () => { await reach(page, name); await name.click({ position: { x: 4, y: 4 } }); await ed.waitFor(); };
          const save = async () => { await ed.locator(":scope > .bar button.primary").click(); await ed.waitFor({ state: "detached" }); };

          // folded under Advanced, empty, the router's own 50 its placeholder
          await open();
          assert.equal(await fold.evaluate((d) => d.open), false, "Advanced starts open");
          assert.equal(await wait.isVisible(), false, "the field shows with Advanced folded");
          await fold.locator(":scope > summary", { hasText: w("Advanced") }).click();
          await wait.waitFor();
          assert.equal(await wait.inputValue(), "");
          assert.equal(await wait.getAttribute("placeholder"), "50");
          // digits only, saved with the server
          await wait.pressSequentially("6a00");
          assert.equal(await wait.inputValue(), "600");
          await save();
          assert.deepEqual(calls, ["slow 600"]);
          // shown again, the fold open; emptied, saved as magpie's own
          await open();
          assert.equal(await fold.evaluate((d) => d.open), true, "a set timeout is folded away");
          assert.equal(await wait.inputValue(), "600");
          await wait.fill("");
          await save();
          assert.deepEqual(calls, ["slow 600", "slow 0"]);
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
          await ctx.close();
        });
      }
    }
    assert.deepEqual(errors, []);
  });
}
