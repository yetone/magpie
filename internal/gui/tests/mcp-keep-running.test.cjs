// Run with Node's test runner and Playwright on the module path; see README.md.
// MasterYi on Discord: the MCP router ended a command it runs after ten
// minutes unused, and chrome-devtools-mcp --autoConnect then asked Chrome
// to allow a new connection again. The editor of a command shared through
// the router has Keep running; on saves keepAlive, and the server's own
// idleTimeout (set from the CLI) goes back as it came. A URL's server, one
// kept out of the router, and the router off have no such switch. Every
// language, Chromium and WebKit, 1100 and 390 wide. No backend: the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { reach } = require("./reader.cjs");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = require("./strings.cjs").strings();
const tr = (lang, s) => (lang === "en" ? s : I18N[lang][s]) || s;
const HOME = "/Users/xv";
const agents = [{ id: "claude", name: "Claude Code", icon: "", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude.json`, hub: "on" }];
const WORDS = ["Keep running", "On keeps it running until magpie quits, for a server that asks you to allow each new connection. Off ends it after 10 minutes unused"];

function server(lang, calls, hubOn) {
  const lib = {
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
    agents, mcpHub: hubOn, hubPath: "/mcp/magpie/hub/",
    instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [],
    servers: [
      { name: "devtools", transport: "stdio", command: "npx", args: ["chrome-devtools-mcp", "--autoConnect"], idleTimeout: 600, agents: ["claude"] },
      { name: "web", transport: "http", url: "https://example.com/mcp", agents: ["claude"] },
      { name: "own", transport: "stdio", command: "/opt/own/mcp", hub: "direct", agents: ["claude"] },
    ],
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light", gateway: "http://127.0.0.1:3425" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib });
    if (url.pathname === "/api/library/mcp/check") return route.fulfill({ json: { servers: {} } });
    if (url.pathname === "/api/library/servers/save") {
      const { old, server: s } = req.postDataJSON();
      calls.push(`${s.name} keepAlive=${s.keepAlive} idleTimeout=${s.idleTimeout}`);
      const i = lib.servers.findIndex((x) => x.name === old);
      lib.servers[i] = { ...lib.servers[i], ...s };
      return route.fulfill({ json: { ...lib, result: { changed: ["claude"] } } });
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
  test(engine + ": Keep running for a server the MCP router runs", async (t) => {
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
          const w = (s) => tr(lang, s);
          if (lang !== "en") for (const s of WORDS) assert.ok(I18N[lang][s], `${lang}: ${s}`);
          for (const hubOn of [true, false]) {
            const calls = [];
            const ctx = await browser.newContext({ viewport: { width, height: 800 } });
            await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
            const page = await ctx.newPage();
            page.setDefaultTimeout(5000);
            page.setDefaultNavigationTimeout(60000);
            page.on("pageerror", (e) => errors.push(e.message));
            await page.route("**/*", server(lang, calls, hubOn));
            await page.goto("http://magpie.test/");
            await page.locator('button[data-view="library"]').click();
            const rowOf = (name) => page.locator("#view-library .lib-server", { has: page.locator(`.lib-health[data-server="${name}"]`) });
            const ed = page.locator("#modal .lib-editor");
            const keep = ed.getByRole("switch", { name: w("Keep running"), exact: true });
            const router = ed.getByRole("switch", { name: tr(lang, "Through the MCP router"), exact: true });
            const open = async (name) => { const r = rowOf(name); await reach(page, r.locator(".name")); await r.locator(".name").click({ position: { x: 4, y: 4 } }); await ed.waitFor(); };
            const save = async () => { await ed.locator(":scope > .bar button.primary").click(); await ed.waitFor({ state: "detached" }); };
            const cancel = async () => { await ed.locator(":scope > .bar button", { hasText: tr(lang, "Cancel") }).click(); await ed.waitFor({ state: "detached" }); };
            await rowOf("devtools").waitFor();

            if (!hubOn) {
              await open("devtools");
              assert.equal(await keep.count(), 0, "Keep running with the router off");
              await save();
              assert.deepEqual(calls, ["devtools keepAlive=false idleTimeout=600"]);
              await ctx.close();
              continue;
            }
            // off by default, its hint says what it is for; on saves keepAlive
            // and keeps the idleTimeout the CLI set
            await open("devtools");
            await keep.waitFor();
            assert.equal(await keep.getAttribute("aria-checked"), "false");
            await ed.locator(".hint", { hasText: w(WORDS[1]) }).waitFor();
            await keep.click();
            assert.equal(await keep.getAttribute("aria-checked"), "true");
            await save();
            assert.deepEqual(calls, ["devtools keepAlive=true idleTimeout=600"]);
            await open("devtools");
            assert.equal(await keep.getAttribute("aria-checked"), "true", "kept on after the save");
            // out of the router: nothing of the router's to keep running
            await router.click();
            assert.equal(await keep.isVisible(), false, "Keep running for a server out of the router");
            await router.click();
            assert.equal(await keep.isVisible(), true);
            await cancel();
            // a URL's server and one kept out of the router have none
            for (const name of ["web", "own"]) {
              await open(name);
              assert.equal(await keep.isVisible(), false, `Keep running for ${name}`);
              await cancel();
            }
            assert.equal(calls.length, 1);
            assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
            await ctx.close();
          }
        });
      }
    }
    assert.deepEqual(errors, []);
  });
}
