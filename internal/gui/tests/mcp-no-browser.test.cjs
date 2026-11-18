// Run with Node's test runner and Playwright on the module path; see README.md.
// Passings_z on X: in magpie's Docker image, Chrome DevTools' MCP server
// listed its tools and each one failed, for want of a Chrome the image
// doesn't ship. Its row says there is no Chrome there, in red, and its
// tooltip says what works instead: Magpie Browser, or the server's option
// for a Chrome it can reach. A server beside it that is fine is unchanged.
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
const HOME = "/config/home";
const agents = [
  { id: "claude", name: "Claude Code", icon: "", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude.json` },
];
const WORDS = [
  "no Chrome here",
  "It lists its {n} tools, but they drive a Chrome it starts itself, and there is none where magpie runs. Magpie Browser, in the Library, uses your own Chrome instead; or start it with {opt} at a Chrome it can reach",
];
const found = {
  "chrome-devtools": { state: "error", why: "nobrowser", detail: "--browserUrl", tools: 33 },
  memory: { state: "ok", tools: 9 },
};

function server(lang, calls) {
  const lib = {
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
    agents, mcpHub: false, hubPath: "/mcp/magpie/hub/",
    instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [],
    servers: [
      { name: "chrome-devtools", transport: "stdio", command: "npx", args: ["-y", "chrome-devtools-mcp@latest"], agents: ["claude"] },
      { name: "memory", transport: "stdio", command: "npx", args: ["-y", "@modelcontextprotocol/server-memory"], agents: ["claude"] },
    ],
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light", gateway: "http://127.0.0.1:3425" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib });
    if (url.pathname === "/api/library/mcp/check") {
      const names = req.postDataJSON().names || Object.keys(found);
      return route.fulfill({ json: { servers: Object.fromEntries(names.map((n) => [n, found[n]])) } });
    }
    if (url.pathname === "/api/library/servers/save") {
      const { old, server: s } = req.postDataJSON();
      calls.push(`${s.name} ${JSON.stringify(s.hub)}`);
      const i = lib.servers.findIndex((x) => x.name === old);
      lib.servers[i] = { ...lib.servers[i], ...s };
      if (!s.hub) delete lib.servers[i].hub;
      return route.fulfill({ json: { ...lib, result: { changed: ["codex"] } } });
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
  test(engine + ": a browser server with no Chrome", async (t) => {
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
          if (lang !== "en") for (const s of WORDS) assert.ok(I18N[lang][s], `${lang}: ${s}`);
          const ctx = await browser.newContext({ viewport: { width, height: 800 } });
          await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
          const page = await ctx.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang, []));
          await page.goto("http://magpie.test/");
          await page.locator('button[data-view="library"]').click();
          const status = (name) => page.locator(`#view-library .lib-health[data-server="${name}"]`);
          await page.waitForFunction(() => document.querySelectorAll("#view-library .lib-health").length === 2 && !document.querySelector("#view-library .lib-health.checking"));

          const cd = status("chrome-devtools");
          assert.equal(await cd.textContent(), w(WORDS[0]));
          assert.ok((await cd.getAttribute("title")).startsWith(w(WORDS[1], { n: 33, opt: "--browserUrl" })), await cd.getAttribute("title"));
          assert.equal(await cd.evaluate((b) => b.classList.contains("err")), true);
          assert.equal(await status("memory").textContent(), w("{n} tools", { n: 9 }));
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
          await ctx.close();
        });
      }
    }
    assert.deepEqual(errors, []);
  });
}
