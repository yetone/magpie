// Run with Node's test runner and Playwright on the module path; see README.md.
// (#7253, jinpwng on X) A project's Hooks tab lists the library's hooks, each
// with chips for the agents that read a project's hooks file, and a chip posts to
// projects/hooks; no chip left takes it out. In English and Chinese.
// No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/emo";
const DIR = HOME + "/code/app";
const agent = (id, name, more = {}) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json`, ...more });
const plugin = (name, more) => ({ name, version: "1.0.0", description: "", agents: [], skills: [], mcp: [], hooks: [], subagents: [], ...more });
const lib = (given) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [
    agent("claude", "Claude Code", { projectSkills: ".claude/skills", projectMCP: ".mcp.json", projectSubagents: ".claude/agents", projectHooks: ".claude/settings.local.json" }),
    agent("codex", "Codex", { projectMCP: ".codex/config.toml", projectSubagents: ".codex/agents", projectHooks: ".codex/hooks.json" }),
    agent("pi", "Pi", { projectSkills: ".agents/skills", projectMCP: ".pi/mcp.json" }),
    agent("goose", "Goose"),
  ],
  instructions: { agents: [] }, servers: [], subagents: [], skills: [], foundServers: [], foundSkills: [],
  hooks: [{ name: "bark", event: "turn-end", command: "say bark", agents: [] }], hookAgents: [{ id: "claude", file: `${HOME}/.claude/settings.json`, events: ["Stop"] }, { id: "codex", file: `${HOME}/.codex/hooks.json`, events: ["Stop"] }], plugins: [], claudeInstalled: [{ id: "fmt@acme", on: false }, { id: "lint@acme", on: true }],
  projects: [{ dir: DIR, name: "app", copy: false, skills: {}, placed: [], servers: {}, subagents: {}, plugins: {}, hooks: given, wrote: {} }],
});

function server(lang, posts) {
  let given = {};
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib(given) });
    if (url.pathname === "/api/library/projects/hooks") {
      const b = req.postDataJSON();
      posts.push(b);
      if (posts.hold) await posts.hold;
      given = { ...given, [b.name]: b.agents };
      if (!b.agents.length) delete given[b.name];
      return route.fulfill({ json: { ...lib(given), result: { changed: [], problems: [] } } });
    }
    if (url.pathname === "/api/library/native") return route.fulfill({ json: [] });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const scrolled = (page) => page.evaluate(() => [window.scrollX, window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop || e.scrollLeft).map((e) => `${e.className}:${e.scrollTop},${e.scrollLeft}`)].join(" "));
// a click where the element is, as the reader's: Playwright's own click
// first scrolls it into view
const press = async (page, loc) => {
  const b = await loc.boundingBox();
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
};
// a chip: the chips fan out under the pointer first, as a reader's hand
// does, and the chip is pressed where it is then
// (a render after the last write may replace it under the pointer, so it
// is found again until it stays)
const box = async (loc) => {
  for (;;) {
    await loc.waitFor({ state: "visible" });
    const b = await loc.boundingBox();
    if (b) return b;
  }
};
const chip = async (page, loc) => {
  const a = await box(loc);
  await page.mouse.move(a.x + a.width / 2, a.y + a.height / 2);
  await page.waitForTimeout(350);
  const b = await box(loc);
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
};


const words = {
  en: { codex: "Codex runs a project's hooks only in a project you trust, once you've reviewed them in /hooks.", one: "bark is in app for Claude Code", two: "bark is in app for Claude Code, Codex", out: "bark is out of app" },
  zh: { codex: "Codex 只在你信任的项目里运行项目的 hooks，并且要先在 /hooks 中审阅。", one: "bark 已在 app 中对 Claude Code 开启", two: "bark 已在 app 中对 Claude Code, Codex 开启", out: "bark 已从 app 中移除" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a project turns a library hook on for the agents that read a project hooks file", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = words[lang], posts = [];
        const ctx = await browser.newContext({ viewport: { width: 860, height: 700 } });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "hooks"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posts));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const card = page.locator("#view-library .lib-project");
        await card.waitFor();
        await press(page, card.locator(".lib-projhead"));
        const row = card.locator(".lib-projhook").filter({ hasText: "bark" });
        await row.waitFor();
        assert.deepEqual(await row.locator(".lib-ag").evaluateAll((cs) => cs.map((c) => c.dataset.agent)), ["claude", "codex"], "Pi and Goose read no project hooks file");
        assert.equal((await card.locator(".lib-projhooks-codex").textContent()).trim(), w.codex);
        const on = (n) => page.waitForFunction((k) => document.querySelectorAll("#view-library .lib-projhook .lib-ag.on").length === k, n);
        const said = (m) => page.waitForFunction((x) => document.querySelector("#status").textContent.trim() === x, m);
        await chip(page, row.locator('.lib-ag[data-agent="claude"]'));
        await on(1); await said(w.one);
        await chip(page, row.locator('.lib-ag[data-agent="codex"]'));
        await on(2); await said(w.two);
        await chip(page, row.locator('.lib-ag[data-agent="claude"]'));
        await on(1);
        await chip(page, row.locator('.lib-ag[data-agent="codex"]'));
        await on(0); await said(w.out);
        assert.deepEqual(posts.map((b) => [b.dir, b.name, b.agents]), [[DIR, "bark", ["claude"]], [DIR, "bark", ["claude", "codex"]], [DIR, "bark", ["codex"]], [DIR, "bark", []]]);
        await ctx.close();
      });
    }
  });
}
