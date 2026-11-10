// Run with Node's test runner and Playwright on the module path; see README.md.
// FakeshadowzZ on X: the Library's Claude plugins tab. A Claude Code plugin
// is one row: its parts (skills, MCP servers, hooks, subagents), the agents
// it is given to, a switch that turns it on or off as a whole, and a dialog
// with what it holds, what magpie doesn't carry, Update and Remove (which
// asks first). Find plugins posts plugins/probe and lists what it found
// with an Install that posts plugins/install; a chip posts plugins/agents,
// the switch plugins/on. No native <select>; nothing wider than the window
// at 420 and 1100 in en, zh, zh-TW, ja and de. No backend: the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/chuan";
const agent = (id, name, more = {}) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json`, ...more });
const WSL = "claude@wsl:Ubuntu";
const SUB_AGENTS = [
  { id: "claude", dir: `${HOME}/.claude/agents`, skills: true, mcp: true },
  { id: "codex", dir: `${HOME}/.codex/agents`, model: true },
  { id: "cursor", dir: `${HOME}/.cursor/agents` },
  { id: "droid", dir: `${HOME}/.factory/droids`, mcp: true },
  { id: "opencode", dir: `${HOME}/.config/opencode/agents` },
  { id: WSL, wsl: true, no: "magpie gives no subagents to an agent in WSL yet" },
];
const MODELS = [{ id: "openai/gpt-6-sol", name: "GPT-6 Sol", note: "OpenAI" }, { id: "group/review", name: "review", group: true }];
const LONG = "Reviews code a change touched and reports only real bugs: logic errors, edge cases, races and leaks, never style, and never refactors that have nothing to do with the change.";
const PLUGIN = { name: "code-kit", version: "1.2.0", description: LONG, agents: ["claude", "codex"], skills: ["code-kit-tdd"], mcp: ["code-kit-docs"], hooks: ["code-kit-lint"], subagents: ["code-kit-reviewer"], left: [{ what: "commands/ship.md", why: "slash commands aren't carried" }], source: "/Users/chuan/code-kit", installed: "2026-11-11" };
const FOUND = { source: "/Users/chuan/market", candidates: [{ name: "fresh-kit", description: "A new one", skills: 2, mcp: 1, hooks: 0, subagents: 1 }, { name: "code-kit", description: "Have it", skills: 1, mcp: 0, hooks: 0, subagents: 0, have: true }] };
const lib = (plugins) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex")],
  instructions: { agents: [] }, servers: [], skills: [], foundServers: [], projects: [], foundSkills: [], problems: [],
  hooks: [], hookAgents: [], subagents: [], subagentAgents: [], plugins,
});

let I18N;
// the words the page says in lang; a string missing from a language fails
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
    if (url.pathname === "/api/library") return json(lib(state.plugins));
    if (url.pathname.startsWith("/api/library/") && req.method() === "POST" && !url.pathname.endsWith("/reveal")) {
      const body = req.postDataJSON(), what = url.pathname.replace("/api/library/", "");
      posts.push({ what, body });
      if (what === "plugins/probe") return json(FOUND);
      if (what === "plugins/agents") state.plugins = state.plugins.map((x) => (x.name === body.name ? { ...x, agents: body.agents } : x));
      if (what === "plugins/on") state.plugins = state.plugins.map((x) => (x.name === body.name ? { ...x, off: !body.on } : x));
      if (what === "plugins/remove") state.plugins = state.plugins.filter((x) => x.name !== body.name);
      if (what === "plugins/install") state.plugins = [...state.plugins, { name: body.names[0], agents: body.agents, skills: ["x"] }];
      return json({ ...lib(state.plugins), result: { changed: ["claude"] } });
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

// a click where the button is, as the reader's, and what was clicked stays
// where it was on the screen while the click redraws around it
const press = async (page, loc) => {
  await page.waitForFunction(() => !document.querySelector("#view-library").getAnimations({ subtree: true }).some((a) => a.effect?.getComputedTiming().endTime !== Infinity));
  const b = await loc.boundingBox();
  const sel = String(loc);
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
  await page.waitForTimeout(250);
  if (!(await loc.count())) return;
  const a = await loc.boundingBox();
  if (a && a.width) assert(Math.abs(a.y - b.y) <= 1, `${sel} moved on the screen from ${Math.round(b.y)} to ${Math.round(a.y)}`);
};
const settled = (page) => page.waitForFunction(() => !document.querySelector("#modal").getAnimations({ subtree: true }).length);
// nothing wider than the window: the page, the Library, its rows and dialog
const sideways = (page) => page.evaluate(() => {
  const over = [];
  const w = document.documentElement.clientWidth;
  if (document.documentElement.scrollWidth > w) over.push("page " + document.documentElement.scrollWidth);
  const v = document.querySelector("#view-library");
  if (v.scrollWidth > v.clientWidth + 1) over.push("library " + v.scrollWidth + ">" + v.clientWidth);
  for (const e of document.querySelectorAll("#view-library .lib-plugin, #view-library .lib-plpick, #view-library .lib-tabs, #modal .lib-pldetail *")) {
    const r = e.getBoundingClientRect();
    if (r.width && (r.right > w + 0.5 || r.left < -0.5)) over.push((e.className || e.tagName) + " " + Math.round(r.left) + "–" + Math.round(r.right));
  }
  return over;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Library's Claude plugins tab", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    I18N = strings().I18N;
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());

    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(lang, async () => {
        const w = (key, vars) => tr(lang, key, vars);
        const state = { plugins: [{ ...PLUGIN }] };
        const posts = [], errors = [];
        const open = async (width) => {
          const ctx = await browser.newContext({ viewport: { width, height: 1000 }, reducedMotion: "reduce" });
          await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "plugins"); } catch {} });
          const page = await ctx.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
          await page.route("**/*", server(lang, state, posts));
          await page.goto("http://magpie.test/");
          await page.locator('button[data-view="library"]').click();
          await page.locator("#view-library .lib-plugin").first().waitFor();
          return { ctx, page };
        };

        {
          const { ctx, page } = await open(1100);
          assert.deepEqual(await sideways(page), [], "sideways scroll at 1100");
          await press(page, page.locator("#view-library .lib-plugin").first());
          await page.locator("#modal .lib-pldetail").waitFor();
          await settled(page);
          assert.deepEqual(await sideways(page), [], "sideways scroll in the dialog at 1100");
          await ctx.close();
        }

        const { ctx, page } = await open(420);
        const v = page.locator("#view-library");
        const row = (name) => v.locator(".lib-plugin").filter({ has: page.locator(".name", { hasText: name }) });
        // the tab, counted, and the row's parts
        const tabs = (await v.locator(".lib-tabs button").allTextContents()).map((x) => x.trim());
        assert(tabs.includes(w("Claude plugins") + " · 1"), tabs.join("|"));
        assert.equal((await row("code-kit").locator(".lib-plparts").textContent()).trim(),
          [w("Skills: {names}", { names: "code-kit-tdd" }), w("MCP servers: {names}", { names: "code-kit-docs" }), w("Hooks: {names}", { names: "code-kit-lint" }), w("Subagents: {names}", { names: "code-kit-reviewer" })].join(" · "));
        assert.deepEqual(await sideways(page), [], "sideways scroll at 420");
        assert.equal(await page.locator("select").count(), 0, "a native <select>");

        // the switch turns every part off at once
        await press(page, row("code-kit").locator(".lib-switch"));
        await page.waitForFunction(() => document.querySelector("#view-library .lib-plugin.off"));
        assert.deepEqual(posts.shift(), { what: "plugins/on", body: { name: "code-kit", on: false } });
        await press(page, row("code-kit").locator(".lib-switch"));
        await page.waitForFunction(() => !document.querySelector("#view-library .lib-plugin.off"));
        assert.deepEqual(posts.shift(), { what: "plugins/on", body: { name: "code-kit", on: true } });

        // a chip changes its agents
        await press(page, row("code-kit").locator('.lib-ag[data-agent="codex"]'));
        await page.waitForTimeout(250);
        assert.deepEqual(posts.shift(), { what: "plugins/agents", body: { name: "code-kit", agents: ["claude"] } });

        // find: probe, pick the new one, install
        await v.locator('input[data-lib="plsource"]').fill("/Users/chuan/market");
        await v.locator(".lib-plfind .lib-find button").click();
        await v.locator(".lib-plpick").first().waitFor();
        assert.deepEqual(posts.shift(), { what: "plugins/probe", body: { source: "/Users/chuan/market" } });
        assert.equal(await v.locator(".lib-plpick input:checked").count(), 1);
        assert.equal(await v.locator(".lib-plpick input:disabled").count(), 0);
        assert.deepEqual(await sideways(page), [], "sideways scroll with the finds at 420");
        await v.locator(".lib-plfind .lib-probefoot button.primary", { hasText: w("Install 1 plugin") }).click();
        await row("fresh-kit").waitFor();
        const inst = posts.shift();
        assert.equal(inst.what, "plugins/install");
        assert.deepEqual(inst.body.names, ["fresh-kit"]);
        assert.equal(inst.body.source, "/Users/chuan/market");

        // the dialog: parts, what isn't carried; Remove asks first
        await press(page, row("code-kit"));
        const d = page.locator("#modal .lib-pldetail");
        await d.waitFor();
        await settled(page);
        assert.equal((await d.locator(".lib-plleft").textContent()).trim(), "commands/ship.md — slash commands aren't carried");
        await d.locator(".bar button.danger").click();
        const sheet = page.locator("dialog.action-confirm");
        await sheet.waitFor();
        await sheet.locator("button", { hasText: w("Cancel") }).click();
        await sheet.waitFor({ state: "detached" });
        await page.waitForTimeout(150);
        assert.deepEqual(posts, [], "Cancel posts nothing");
        await d.locator(".bar button.danger").click();
        await sheet.waitFor();
        await sheet.locator("button.danger-fill").click();
        await d.waitFor({ state: "detached" });
        assert.deepEqual(posts.shift(), { what: "plugins/remove", body: { name: "code-kit" } });
        assert.equal(await row("code-kit").count(), 0);

        assert.deepEqual(posts, []);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
