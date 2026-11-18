// Run with Node's test runner and Playwright on the module path; see README.md.
// A part a Claude plugin brought says so on its own row, under Skills, MCP
// servers, Hooks and Subagents: "In code-kit", with why its agents follow
// the plugin's when those change (library/plugin.go givePlugin). A part of
// no plugin has no such tag. Every language, both engines, at 420 and 1100;
// nothing wider than the window. No backend: the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/chuan";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const kit = { bundle: "code-kit" };
const lib = () => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex")],
  instructions: { agents: [] }, foundServers: [], projects: [], foundSkills: [], problems: [],
  skills: [{ name: "code-kit-tdd", description: "Test first", kind: "", agents: ["claude"], ...kit }, { name: "own-skill", description: "Mine", kind: "", agents: ["claude"] }],
  servers: [{ name: "code-kit-docs", transport: "stdio", command: "/bin/docs", agents: ["claude"], ...kit }, { name: "own-server", transport: "stdio", command: "/bin/own", agents: ["claude"] }],
  hooks: [{ name: "code-kit-lint", event: "turn-end", command: "lint", agents: ["claude"], ...kit }, { name: "own-hook", event: "turn-end", command: "own", agents: ["claude"] }],
  hookAgents: [{ id: "claude", events: ["turn-end"] }],
  subagents: [{ name: "code-kit-reviewer", description: "Reviews", agents: ["claude"], ...kit }, { name: "own-agent", description: "Mine", agents: ["claude"] }],
  subagentAgents: [{ id: "claude", dir: `${HOME}/.claude/agents` }],
  plugins: [{ name: "code-kit", agents: ["claude"], skills: ["code-kit-tdd"], mcp: ["code-kit-docs"], hooks: ["code-kit-lint"], subagents: ["code-kit-reviewer"] }],
});

let I18N;
function tr(lang, key, vars = {}) {
  let s = key;
  if (lang !== "en") {
    assert.equal(typeof I18N[lang][key], "string", `${lang} has no ${JSON.stringify(key)}`);
    s = I18N[lang][key];
  }
  return s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m));
}

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(lib());
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

const TABS = [["skills", ".lib-skill", "code-kit-tdd", "own-skill"], ["mcp", ".lib-server", "code-kit-docs", "own-server"],
  ["hooks", ".lib-hook", "code-kit-lint", "own-hook"], ["subagents", ".lib-subagent", "code-kit-reviewer", "own-agent"]];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a plugin's part says which plugin it came with", async (t) => {
    I18N = strings().I18N;
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1100, 420]) for (const [tab, cls, part, own] of TABS) {
      await t.test(`${lang} ${width} ${tab}`, async () => {
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        t.after(() => ctx.close());
        await ctx.addInitScript((tab) => { try { localStorage.setItem("magpie.libTab", tab); } catch {} }, tab);
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        const row = (name) => v.locator(cls).filter({ has: page.locator(".name", { hasText: name }) }).first();
        await row(part).waitFor();
        const badge = row(part).locator(".lib-bundle");
        assert.equal(await badge.count(), 1, `${part} has no plugin tag`);
        assert.equal((await badge.textContent()).trim(), tr(lang, "In {plugin}", { plugin: "code-kit" }));
        assert.equal(await badge.getAttribute("title"), tr(lang, "Comes with the Claude plugin {plugin}. Changing the plugin's agents, or turning it off and on, gives this to the plugin's agents again.", { plugin: "code-kit" }));
        assert.equal(await row(own).locator(".lib-bundle").count(), 0, `${own} is tagged as a plugin's`);
        const over = await page.evaluate(() => {
          const w = document.documentElement.clientWidth, out = [];
          for (const e of document.querySelectorAll("#view-library .lib-bundle")) {
            const r = e.getBoundingClientRect();
            if (r.right > w + 0.5 || r.left < -0.5) out.push(Math.round(r.left) + "–" + Math.round(r.right));
          }
          if (document.documentElement.scrollWidth > w) out.push("page " + document.documentElement.scrollWidth);
          return out;
        });
        assert.deepEqual(over, []);
        assert.deepEqual(errors, []);
      });
    }
  });
}
