// Run with Node's test runner and Playwright on the module path; see README.md.
// (#7254, jinpwng on X) A project's Claude plugins tab lists the plugins
// Claude Code has installed itself, each with one Claude chip that posts to
// projects/claude-plugin; no chip left takes it out. In English and Chinese.
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
  plugins: [], claudeInstalled: [{ id: "fmt@acme", on: false }, { id: "lint@acme", on: true }],
  projects: [{ dir: DIR, name: "app", copy: false, skills: {}, placed: [], servers: {}, subagents: {}, plugins: {}, claudePlugins: given, wrote: {} }],
});

function server(lang, posts) {
  let given = [];
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib(given) });
    if (url.pathname === "/api/library/projects/claude-plugin") {
      const b = req.postDataJSON();
      posts.push(b);
      if (posts.hold) await posts.hold;
      given = given.filter((x) => x !== b.name);
      if (b.agents.length) given.push(b.name);
      return route.fulfill({ json: { ...lib(given), result: { changed: [], problems: [] } } });
    }
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
  en: { on: "fmt@acme is in app for Claude Code", out: "fmt@acme is out of app", note: "The plugins above are Claude Code's own, turned on for this project in .claude/settings.local.json. They stay installed through Claude Code." },
  zh: { on: "fmt@acme 已在 app 中对 Claude Code 开启", out: "fmt@acme 已从 app 中移除", note: "上面的插件是 Claude Code 自带的，在 .claude/settings.local.json 里只为这个项目启用，仍由 Claude Code 安装。" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a project turns on a plugin Claude Code has installed", async (t) => {
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
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "plugins"); } catch {} });
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
        const rows = card.locator(".lib-projclaude");
        await rows.first().waitFor();
        assert.equal(await rows.count(), 2);
        assert.deepEqual(await rows.first().locator(".lib-ag").evaluateAll((cs) => cs.map((c) => c.dataset.agent)), ["claude"]);
        assert.equal((await card.locator(".lib-projclaude-note").textContent()).trim(), w.note);
        const row = () => card.locator(".lib-projclaude").filter({ hasText: "fmt@acme" });
        await chip(page, row().locator('.lib-ag[data-agent="claude"]'));
        await page.waitForFunction(() => document.querySelectorAll('#view-library .lib-projclaude .lib-ag.on').length === 1);
        await page.waitForFunction((s) => document.querySelector("#status").textContent.trim() === s, w.on);
        await chip(page, row().locator('.lib-ag[data-agent="claude"]'));
        await page.waitForFunction(() => document.querySelectorAll('#view-library .lib-projclaude .lib-ag.on').length === 0);
        await page.waitForFunction((s) => document.querySelector("#status").textContent.trim() === s, w.out);
        assert.deepEqual(posts.map((b) => [b.dir, b.name, b.agents]), [[DIR, "fmt@acme", ["claude"]], [DIR, "fmt@acme", []]]);
        await ctx.close();
      });
    }
  });
}
