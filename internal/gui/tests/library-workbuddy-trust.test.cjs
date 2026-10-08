// Run with Node's test runner and Playwright on the module path; see README.md.
// #1266: WorkBuddy now takes the library's MCP servers, but connects one
// only once it is trusted in WorkBuddy itself (mcp-approvals.json), and
// again after its command or address changes. The servers page says so
// while a server is given to WorkBuddy, and not otherwise. In en, zh,
// zh-tw, ja and de at 440px, with no sideways scroll.
// No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/emo";
const agent = (id, name, more = {}) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json`, ...more });
const lib = (servers) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("workbuddy", "WorkBuddy")],
  instructions: { agents: [] }, servers, skills: [], foundServers: [], projects: [], foundSkills: [], problems: [],
});

let I18N;
async function loadI18N() {
  const src = await fs.readFile(path.join(assets, "i18n.js"), "utf8");
  I18N = new Function(src.slice(0, src.indexOf("\n};") + 3) + "; return I18N;")();
}
function tr(lang, key, vars = {}) {
  let s = key;
  if (lang !== "en") {
    assert.equal(typeof I18N[lang][key], "string", `${lang} has no ${JSON.stringify(key)}`);
    s = I18N[lang][key];
  }
  return s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m));
}

function server(lang, servers) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(lib(servers));
    if (url.pathname === "/api/library/mcp/check") return json({ servers: Object.fromEntries(req.postDataJSON().names.map((n) => [n, { state: "ok", tools: 2 }])) });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}


for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": WorkBuddy's trust step is said while it is given a server", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    await loadI18N();
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());

    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(lang, async () => {
        const note = tr(lang, "{agent} connects a server only once you trust it: switch it on in {agent}'s MCP settings, and again after its command or address changes.", { agent: "WorkBuddy" });
        for (const [servers, shown] of [
          [[{ name: "fs", transport: "stdio", command: "fs-mcp", args: [], agents: ["claude", "workbuddy"] }], true],
          [[{ name: "fs", transport: "stdio", command: "fs-mcp", args: [], agents: ["claude"] }], false],
        ]) {
          const errors = [];
          const ctx = await browser.newContext({ viewport: { width: 440, height: 1000 }, reducedMotion: "reduce" });
          await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
          const page = await ctx.newPage();
          page.setDefaultTimeout(5000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang, servers));
          await page.goto("http://magpie.test/");
          await page.locator('button[data-view="library"]').click();
          const v = page.locator("#view-library");
          await v.locator(".lib-server").first().waitFor();
          const p = v.locator(".lib-wb-trust");
          if (shown) {
            assert.equal((await p.textContent()).trim(), note);
            assert(await p.isVisible());
            const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
            assert(over <= 0, "sideways scroll " + over);
          } else {
            assert.equal(await p.count(), 0, "the note shows with no server given to WorkBuddy");
          }
          assert.deepEqual(errors, []);
          await ctx.close();
        }
      });
    }
  });
}
