// Run with Node's test runner and Playwright on the module path; see README.md.
// paopao_baobao on X: Variables, the Library's tab for environment
// variables. One is kept once and given to agents with the chips every other
// tab has; an agent magpie can't write one for has its chip greyed, saying
// why, and a click on it posts nothing. A row's chip posts env/agents; Add
// opens the editor and Save posts env/save; a row's Remove asks first and
// posts env/remove. At 440px wide in en, zh, zh-TW, ja and de: no sideways
// scroll. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/allen";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const ENV_AGENTS = [
  { id: "claude", file: `${HOME}/.claude/settings.json` },
  { id: "codex", file: `${HOME}/.codex/config.toml` },
  { id: "cursor", no: "Cursor has no environment settings magpie can write" },
];
const lib = (env) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex"), agent("cursor", "Cursor")],
  instructions: { agents: [] }, servers: [], skills: [], foundServers: [], projects: [], foundSkills: [], problems: [],
  env, envAgents: ENV_AGENTS,
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

function server(lang, state, posts) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(lib(state.env));
    if (url.pathname.startsWith("/api/library/") && req.method() === "POST" && !url.pathname.endsWith("/reveal")) {
      const body = req.postDataJSON(), what = url.pathname.replace("/api/library/", "");
      posts.push({ what, body });
      if (what === "env/agents") state.env = state.env.map((x) => (x.name === body.name ? { ...x, agents: body.agents.filter((a) => a !== "cursor") } : x));
      if (what === "env/save") state.env = [...state.env.filter((x) => x.name !== body.old), body.env];
      if (what === "env/remove") state.env = state.env.filter((x) => x.name !== body.name);
      return json({ ...lib(state.env), result: { changed: ["claude"] } });
    }
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

const sideways = (page) => page.evaluate(() => {
  const over = [];
  const w = document.documentElement.clientWidth;
  if (document.documentElement.scrollWidth > w) over.push("page " + document.documentElement.scrollWidth);
  for (const e of document.querySelectorAll("#view-library .lib-env, #view-library .lib-tabs, #modal .lib-envedit *")) {
    const r = e.getBoundingClientRect();
    if (r.width && (r.right > w + 0.5 || r.left < -0.5)) over.push((e.className || e.tagName) + " " + Math.round(r.left) + "-" + Math.round(r.right));
  }
  return over;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Library's Variables tab", async (t) => {
    I18N = strings().I18N;
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(lang, async () => {
        const w = (key, vars) => tr(lang, key, vars);
        const state = { env: [{ name: "HTTPS_PROXY", value: "http://127.0.0.1:7890", agents: ["codex"] }] };
        const posts = [], errors = [];
        const ctx = await browser.newContext({ viewport: { width: 440, height: 1000 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "env"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
        await page.route("**/*", server(lang, state, posts));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        await v.locator(".lib-env").first().waitFor();
        const row = (name) => v.locator(".lib-env").filter({ has: page.locator(".name", { hasText: new RegExp("^" + name + "$") }) });

        const tabs = (await v.locator(".lib-tabs button").allTextContents()).map((x) => x.trim());
        assert(tabs.includes(w("Variables") + " · 1"), tabs.join("|"));
        assert.equal((await row("HTTPS_PROXY").locator(".lib-envval").textContent()).trim(), "http://127.0.0.1:7890");
        assert.deepEqual(await sideways(page), []);

        // Cursor's chip is greyed, says why, and a click posts nothing
        const cur = row("HTTPS_PROXY").locator('.lib-ag[data-agent="cursor"]');
        assert.equal(await cur.getAttribute("aria-disabled"), "true");
        assert.equal(await cur.getAttribute("title"), w("{agent} has no environment settings magpie can write", { agent: "Cursor" }));
        await cur.click({ force: true });
        await page.waitForTimeout(150);
        assert.equal(posts.length, 0);

        // a chip posts env/agents
        await row("HTTPS_PROXY").locator('.lib-ag[data-agent="claude"]').click();
        await page.waitForFunction(() => true);
        for (let i = 0; i < 40 && !posts.length; i++) await page.waitForTimeout(50);
        assert.equal(posts[0].what, "env/agents");
        assert.deepEqual([...posts[0].body.agents].sort(), ["claude", "codex"]);

        // Add opens the editor; Save posts env/save with what was typed
        await v.getByRole("button", { name: w("＋ Add variable") }).click();
        const m = page.locator("#modal");
        await m.locator("input").first().fill("NO_PROXY");
        await m.locator("input").nth(1).fill("localhost");
        assert.deepEqual(await sideways(page), []);
        await m.getByRole("button", { name: w("Add"), exact: true }).click();
        for (let i = 0; i < 40 && posts.length < 2; i++) await page.waitForTimeout(50);
        assert.equal(posts[1].what, "env/save");
        assert.equal(posts[1].body.old, "");
        assert.equal(posts[1].body.env.name, "NO_PROXY");
        assert.equal(posts[1].body.env.value, "localhost");
        await row("NO_PROXY").waitFor();

        // a row's Remove asks first, naming it, and posts env/remove
        await row("NO_PROXY").locator(".lib-rm").click();
        await page.locator("#modal .lib-rmone").filter({ hasText: w("Remove the variable {name}?", { name: "NO_PROXY" }) }).waitFor();
        assert.equal(posts.length, 2, "removed before it asked");
        await page.locator("#modal .lib-rmone .primary").click();
        for (let i = 0; i < 40 && posts.length < 3; i++) await page.waitForTimeout(50);
        assert.equal(posts[2].what, "env/remove");
        assert.equal(posts[2].body.name, "NO_PROXY");
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
