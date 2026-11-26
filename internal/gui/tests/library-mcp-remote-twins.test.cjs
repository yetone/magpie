// Run with Node's test runner and Playwright on the module path; see README.md.
// starballl on X ("这个是网关magpie,同步后工具重复了"): a hub's servers synced
// into this Library, and the hub's own router added for its Remote
// magpie, gave an agent every hub tool twice. The router now leaves out
// its copies of the Library's own servers (twinTool in bridge_mcp.go),
// and the Remote magpie's row says which: those it lists under a Library
// server's name (<server>__<tool>) and that share an agent with it. A
// server with no such tool, one given to no agent of the router's, the
// router off, or the Remote magpie's server kept out of it, says nothing.
// Every language, Chromium and WebKit, 1100 and 390 wide; nothing scrolls
// sideways. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = require("./strings.cjs").strings(); // i18n.js's and the plugins'
const tr = (lang, s, vars = {}) => ((lang === "en" ? s : I18N[lang][s]) || s).replace(/\{(\w+)\}/g, (_, k) => vars[k]);
const HOME = "/Users/xv";
const agents = (on) => [
  { id: "claude", name: "Claude Code", icon: "", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude.json`, ...(on ? { hub: "on" } : {}) },
  { id: "codex", name: "Codex", icon: "", skills: `${HOME}/.codex/skills`, mcp: `${HOME}/.codex/config.toml`, ...(on ? { hub: "on" } : {}) },
];
const LINE = "Agents get {servers} from this Library, not again through {name}";
const TIP = "{name} lists the same servers, run on the other magpie. Its copies are left out, and given only while this Library's own can't list its tools";

function server(lang, opts) {
  const lib = {
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
    agents: agents(opts.hub), mcpHub: opts.hub, hubPath: "/mcp/magpie/hub/", hubOnDemandQuery: "tools=on-demand", hubOnDemandOver: 10,
    instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [], problems: [],
    servers: [
      { name: "anysearch", transport: "stdio", command: "npx", args: ["anysearch-mcp"], agents: ["claude"] },
      { name: "web-search", transport: "stdio", command: "npx", args: ["web-search-mcp"], agents: ["claude"] },
      // given to no agent the router is: codex has it from the router alone
      { name: "context7", transport: "stdio", command: "npx", args: ["context7"], agents: ["codex"] },
      // a tool of its own, none of the router's
      { name: "mine", transport: "stdio", command: "/opt/mine/mcp", agents: ["claude"] },
      { name: "magpie-44", transport: "http", url: "http://192.168.168.44:3425/mcp/magpie/hub", headers: { Authorization: "Bearer sk-x" }, agents: ["claude"], local: true, bridge: "m44", ...(opts.direct ? { hub: "direct" } : {}) },
    ],
  };
  const tools = (...names) => ({ state: "ok", tools: names.length, toolList: names.map((name) => ({ name })), at: 1 });
  const checks = {
    anysearch: tools("batch_search", "extract"), "web-search": tools("find"), context7: tools("docs"), mine: tools("x"),
    "magpie-44": tools("anysearch__batch_search", "anysearch__extract", "web-search__find", "context7__docs", "exa__search", "mine_x"),
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib });
    if (url.pathname === "/api/library/mcp/check") {
      const names = req.postDataJSON()?.names || Object.keys(checks);
      return route.fulfill({ json: { servers: Object.fromEntries(names.filter((n) => checks[n]).map((n) => [n, checks[n]])) } });
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
  test(engine + ": a Remote magpie's row names the Library's own servers it leaves out", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    const open = async (lang, width, opts) => {
      const ctx = await browser.newContext({ viewport: { width, height: 800 } });
      await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, opts));
      await page.goto("http://magpie.test/");
      await page.locator('button[data-view="library"]').click();
      const row = page.locator("#view-library .lib-row", { has: page.locator('.lib-health[data-server="magpie-44"]') });
      await row.waitFor();
      // its tools are known once it is checked
      await row.locator('.lib-health:not(.checking):not(.idle)').waitFor();
      return { ctx, page, row };
    };
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1100, 390]) {
        await t.test(`${lang} ${width}`, async () => {
          if (lang !== "en") for (const s of [LINE, TIP]) assert.ok(I18N[lang][s], `${lang}: ${s}`);
          const { ctx, page, row } = await open(lang, width, { hub: true });
          const line = row.locator(".lib-twins");
          await line.waitFor();
          // anysearch and web-search, not context7
          // (codex's alone), mine (no tool of the router's) or exa (not here)
          assert.equal((await line.textContent()).trim(), tr(lang, LINE, { servers: "anysearch, web-search", name: "magpie-44" }));
          assert.equal(await line.getAttribute("title"), tr(lang, TIP, { name: "magpie-44" }));
          // the Library's own rows say nothing of it
          assert.equal(await page.locator("#view-library .lib-twins").count(), 1);
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
          await ctx.close();
        });
      }
    }
    await t.test("the router off, or the Remote magpie kept out of it: nothing left out", async () => {
      for (const opts of [{ hub: false }, { hub: true, direct: true }]) {
        const { ctx, row } = await open("en", 1100, opts);
        assert.equal(await row.locator(".lib-twins:not([hidden])").count(), 0, JSON.stringify(opts));
        await ctx.close();
      }
    });
    assert.deepEqual(errors, []);
  });
}
