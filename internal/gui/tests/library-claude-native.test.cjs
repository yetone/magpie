// Run with Node's test runner and Playwright on the module path; see README.md.
// (jinpwng on X) A library plugin Claude Code loads natively says so where
// it is given: on its row in the Claude plugins tab, user-wide, and on its
// row in a project's card, where Claude Code loads it for that project;
// given to Claude Code user-wide while a project loads it natively too, the
// project's row says it runs twice there. A plugin Claude Code doesn't load
// has no note. Every language, both engines, at 420 and 1100; nothing
// wider than the window. No backend: the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/jin";
const DIR = HOME + "/code/app";
const agent = (id, name, more = {}) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json`, ...more });
const plugin = (name, more) => ({ name, version: "1.0.0", description: "", agents: [], skills: [], mcp: [], hooks: [], subagents: [], ...more });
const lib = () => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [
    agent("claude", "Claude Code", { projectSkills: ".claude/skills", projectMCP: ".mcp.json", projectSubagents: ".claude/agents", projectHooks: ".claude/settings.local.json" }),
    agent("codex", "Codex", { projectMCP: ".codex/config.toml", projectSubagents: ".codex/agents", projectHooks: ".codex/hooks.json" }),
  ],
  instructions: { agents: [] }, servers: [], subagents: [], skills: [], foundServers: [], foundSkills: [], problems: [],
  plugins: [
    plugin("acme-kit", { agents: ["claude", "codex"], hooks: ["acme-kit:Stop"], skills: ["tdd"], claudeNative: "acme-kit@acme-market-with-a-long-name" }),
    plugin("tidy", { agents: ["claude"], hooks: ["tidy:Stop"] }),
    plugin("plain", { skills: ["plain"] }),
  ],
  projects: [{ dir: DIR, name: "app", copy: false, skills: {}, placed: [], servers: {}, subagents: {}, wrote: {},
    plugins: { "acme-kit": ["claude", "codex"], plain: ["codex"] },
    claudeNative: { "acme-kit": "acme-kit@acme-market-with-a-long-name" }, claudeTwice: { tidy: "tidy@acme" } }],
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
const NATIVE = "Claude Code loads {id} itself, so magpie gives it none of this";
const TWICE = "Claude Code loads {id} itself in this project too, so here it runs twice: take Claude Code off the plugin to stop it";

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

const wide = (page) => page.evaluate(() => {
  const w = document.documentElement.clientWidth, out = [];
  for (const e of document.querySelectorAll("#view-library .lib-plnative")) {
    const r = e.getBoundingClientRect();
    if (r.right > w + 0.5 || r.left < -0.5) out.push(Math.round(r.left) + "–" + Math.round(r.right));
  }
  if (document.documentElement.scrollWidth > w) out.push("page " + document.documentElement.scrollWidth);
  return out;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a plugin Claude Code loads itself says so where it is given", async (t) => {
    I18N = strings().I18N;
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1100, 420]) {
      await t.test(`${lang} ${width}`, async () => {
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        t.after(() => ctx.close());
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "plugins"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        const row = (name) => v.locator(".lib-plugin").filter({ has: page.locator(".name", { hasText: name }) }).first();
        await row("acme-kit").waitFor();
        assert.equal((await row("acme-kit").locator(".lib-plnative").textContent()).trim(), tr(lang, NATIVE, { id: "acme-kit@acme-market-with-a-long-name" }));
        for (const n of ["tidy", "plain"]) assert.equal(await row(n).locator(".lib-plnative").count(), 0, `${n} is noted`);

        const card = v.locator(".lib-project");
        await card.locator(".lib-projhead").click();
        const prow = (name) => card.locator(".lib-projplugin").filter({ has: page.locator(".name", { hasText: name }) }).first();
        await prow("acme-kit").waitFor();
        assert.equal((await prow("acme-kit").locator(".lib-plnative").textContent()).trim(), tr(lang, NATIVE, { id: "acme-kit@acme-market-with-a-long-name" }));
        assert.equal((await prow("tidy").locator(".lib-pltwice").textContent()).trim(), tr(lang, TWICE, { id: "tidy@acme" }));
        assert.equal(await prow("plain").locator(".lib-plnative").count(), 0, "plain is noted in the project");
        assert.deepEqual(await wide(page), []);
        assert.deepEqual(errors, []);
      });
    }
  });
}
