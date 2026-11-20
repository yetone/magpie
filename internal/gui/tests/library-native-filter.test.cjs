// Run with Node's test runner and Playwright on the module path; see README.md.
// jinpwng on X: what Claude Code, Codex and Pi installed themselves says
// which plugin or package it came with, and clicking that name shows only
// what it declares, under the bar an imported plugin's "In" tag gives
// (#7280); Show all brings the rest back. The pick is the agent's plugin:
// a same-named one of another agent isn't shown with it. The library's
// own items stay as they are, and nothing is written. Every language, both
// engines, at 1100 and 380. No backend: the API is faked, and any write
// to it fails the test.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/me";
const lib = () => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [{ id: "codex", name: "Codex", icon: "", skills: `${HOME}/.codex/skills`, mcp: true, hooks: true }, { id: "claude", name: "Claude Code", icon: "", skills: `${HOME}/.claude/skills`, mcp: true, hooks: true }],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], problems: [],
  skills: [{ name: "own-skill", description: "Mine", kind: "", agents: ["claude"] }], servers: [], hooks: [],
});
const native = [
  {
    agent: "codex", agentName: "Codex", name: "chrome", version: "26.1029.61003", source: "openai-bundled", scope: "user", loaded: "unknown",
    parts: [{ kind: "skill", name: "chrome", detail: "Drive Chrome" }, { kind: "skill", name: "chrome-tabs", detail: "Tabs" }, { kind: "hook", name: "Stop", detail: "mcp_tool node_repl/turn_ended" }, { kind: "mcp", name: "chrome_mcp", detail: "stdio" }],
  },
  {
    agent: "codex", agentName: "Codex", name: "codex-app-tools", version: "26.1029.61003", source: "openai-bundled", scope: "user", loaded: "unknown",
    parts: [{ kind: "mcp", name: "codex_app", detail: "stdio" }],
  },
  {
    agent: "claude", agentName: "Claude Code", name: "fixture@fixmarket", version: "0.1.0", source: "fixmarket", scope: "project", project: `${HOME}/work/app`, enabled: true, loaded: "unknown",
    parts: [{ kind: "skill", name: "fix-skill", detail: "A fixture skill" }, { kind: "hook", name: "SessionStart", detail: "/usr/bin/true" }, { kind: "subagent", name: "fix-agent", detail: "A fixture agent" }],
  },
  {
    agent: "claude", agentName: "Claude Code", name: "proj-kit", version: "9.0.0", source: "fixmarket", scope: "user", enabled: true, loaded: "unknown",
    parts: [{ kind: "skill", name: "claude-review", detail: "Claude's own" }, { kind: "subagent", name: "kit-agent", detail: "Another agent" }],
  },
  {
    agent: "pi", agentName: "Pi", name: "proj-kit", version: "2.0.0", source: "npm:proj-kit", scope: "project", project: `${HOME}/work/app`, loaded: "unknown",
    parts: [{ kind: "skill", name: "review", detail: "Review code" }, { kind: "skill", name: "review-deep", detail: "Review harder" }],
  },
];

let I18N;
function tr(lang, key, vars = {}) {
  let s = key;
  if (lang !== "en") {
    assert.equal(typeof I18N[lang][key], "string", `${lang} has no ${JSON.stringify(key)}`);
    s = I18N[lang][key];
  }
  return s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m));
}

function server(lang, tab, writes) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (req.method() !== "GET" && url.pathname.startsWith("/api/library")) writes.push(url.pathname);
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};try{localStorage.setItem("magpie.libTab","${tab}")}catch{}` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/library") return json(lib());
    if (url.pathname === "/api/library/native") return json(native);
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

// the tab, every native row it lists, the row whose name is clicked, the
// plugin or package that is, and the rows left
const TABS = [
  ["skills", ["chrome", "chrome-tabs", "fix-skill", "claude-review", "review", "review-deep"], "review", "proj-kit", ["review", "review-deep"]],
  ["skills", ["chrome", "chrome-tabs", "fix-skill", "claude-review", "review", "review-deep"], "chrome", "chrome", ["chrome", "chrome-tabs"]],
  ["mcp", ["chrome_mcp", "codex_app"], "codex_app", "codex-app-tools", ["codex_app"]],
  ["hooks", ["Stop", "SessionStart"], "SessionStart", "fixture@fixmarket", ["SessionStart"]],
  ["subagents", ["fix-agent", "kit-agent"], "kit-agent", "proj-kit", ["kit-agent"]],
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an agent's own plugin or package shows only what it declares", async (t) => {
    I18N = strings().I18N;
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1100, 380]) for (const [tab, all, at, pkg, left] of TABS) {
      await t.test(`${lang} ${width} ${tab} ${pkg}`, async () => {
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        t.after(() => ctx.close());
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], writes = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, tab, writes));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        const box = v.locator(".lib-nativebox");
        const rows = box.locator(".lib-native");
        const bar = box.locator(".lib-nativeonly");
        await rows.first().waitFor();
        assert.deepEqual(await rows.locator(".name").allTextContents(), all);
        assert.equal(await bar.count(), 0);
        const own = tab === "skills" ? await v.locator(".lib-skill").count() : 0;

        const row = rows.filter({ has: page.locator(".name", { hasText: new RegExp("^" + at + "$") }) });
        const pick = row.locator(".lib-srcpick");
        assert.equal(await pick.count(), 1);
        assert((await pick.textContent()).startsWith(pkg + " "), await pick.textContent());
        assert.equal(await pick.getAttribute("title"), tr(lang, "Show only what {plugin} brings", { plugin: pkg }));
        // the origin still reads as it did: agent · plugin version · scope
        assert.match(await row.locator(".lib-src").textContent(), new RegExp(" · " + pkg.replace(/[.@]/g, "\\$&") + " [0-9.]+ · "));
        await pick.click();
        await bar.waitFor();
        assert.equal((await bar.locator(".label").textContent()).trim(), tr(lang, "Only what {plugin} brings", { plugin: pkg }));
        assert.deepEqual(await rows.locator(".name").allTextContents(), left);
        // the library's own items are left as they are
        if (tab === "skills") assert.equal(await v.locator(".lib-skill").count(), own);
        // the name picks; it opens nothing
        assert.equal(await page.locator(".modal, .sheet, dialog[open]").filter({ visible: true }).count(), 0);
        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert(over <= 0, `the page scrolls sideways by ${over}px`);

        await bar.locator(".lib-bundleall").click();
        await bar.waitFor({ state: "detached" });
        assert.deepEqual(await rows.locator(".name").allTextContents(), all);
        assert.deepEqual(writes, [], "a pick wrote to the library");
        assert.deepEqual(errors, []);
      });
    }
  });
}
