// Run with Node's test runner and Playwright on the module path; see README.md.
// #7280 (jinpwng): Skills, Hooks and Subagents show only what one plugin
// brought when its "In code-kit" tag is clicked, under a bar naming it;
// Show all brings the rest back, and nothing is given or taken from an
// agent on the way. The skills' filter matches the plugin's name too.
// Every language, both engines, at 420 and 1100. No backend: the API is
// faked, and any write to it fails the test.
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
  skills: [{ name: "code-kit-tdd", description: "Test first", kind: "", agents: ["claude"], ...kit }, { name: "own-skill", description: "Mine", kind: "", agents: ["claude"] },
    ...[1, 2, 3, 4, 5, 6, 7, 8].map((n) => ({ name: "own-skill-" + n, description: "Mine too", kind: "", agents: ["claude"] }))],
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

function server(lang, writes) {
  return async (route) => {
    const url = new URL(route.request().url());
    if (route.request().method() !== "GET" && url.pathname.startsWith("/api/library")) writes.push(url.pathname);
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

const TABS = [["skills", ".lib-skill", "code-kit-tdd", "own-skill"],
  ["hooks", ".lib-hook", "code-kit-lint", "own-hook"], ["subagents", ".lib-subagent", "code-kit-reviewer", "own-agent"]];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a plugin's tag shows only what it brought", async (t) => {
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
        const errors = [], writes = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, writes));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        const row = (name) => v.locator(cls).filter({ has: page.locator(".name", { hasText: new RegExp("^" + name + "(?!-)") }) }).first();
        const bar = v.locator(".lib-bundleonly");
        await row(part).waitFor();
        await row(own).waitFor();
        assert.equal(await bar.count(), 0);

        await row(part).locator(".lib-bundle").click();
        await bar.waitFor();
        assert.equal((await bar.locator(".label").textContent()).trim(), tr(lang, "Only what {plugin} brings", { plugin: "code-kit" }));
        await row(own).waitFor({ state: "detached" });
        assert.equal(await v.locator(cls).count(), 1, "only the plugin's row is left");
        assert.equal(await row(part).count(), 1);
        // the tag picks; it doesn't open the row's editor
        assert.equal(await page.locator(".modal, .sheet, dialog[open]").filter({ visible: true }).count(), 0);

        await bar.locator(".lib-bundleall").click();
        await row(own).waitFor();
        assert.equal(await bar.count(), 0);
        assert.equal(await row(part).count(), 1);

        if (tab === "skills") {
          const q = v.locator(".lib-skillq");
          await q.fill("code-kit");
          await row(own).waitFor({ state: "detached" });
          assert.equal(await row(part).count(), 1, "the filter matches the plugin's name");
          await q.fill("");
          await row(own).waitFor();
        }
        assert.deepEqual(writes, [], "a pick wrote to the library");
        assert.deepEqual(errors, []);
      });
    }
  });
}
