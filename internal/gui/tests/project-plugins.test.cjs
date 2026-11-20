// Run with Node's test runner and Playwright on the module path; see README.md.
// (jinpwng on X) The Claude plugins tab lists the projects: a card opens to
// each library plugin with chips for the agents that take some part of a
// plugin in a project (skills, servers, subagents or hooks), and a chip posts
// that project's agents for the plugin to projects/plugin; no chip left takes
// it out. An agent that takes none in a project has no chip, and the note on
// Codex's /hooks review shows once Codex gets a plugin with hooks. The click
// scrolls nothing; in English and Chinese. No backend: the API is faked here.
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
  plugins: [plugin("acme-kit", { skills: ["tdd"], mcp: ["acme-db"], hooks: ["acme-kit:Stop"], subagents: ["reviewer"] }), plugin("tidy", { skills: ["tidy"] }), plugin("pi-guard", { extensions: ["extension.ts"] })],
  projects: [{ dir: DIR, name: "app", copy: false, skills: {}, placed: [], servers: {}, subagents: {}, plugins: given, wrote: {} }],
});

function server(lang, posts) {
  let given = {};
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib(given) });
    if (url.pathname === "/api/library/projects/plugin") {
      const b = req.postDataJSON();
      posts.push(b);
      if (posts.hold) await posts.hold;
      given = { ...given, [b.name]: b.agents };
      if (!b.agents.length) delete given[b.name];
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
  en: { head: "Project plugins", none: "No Claude plugins yet", one: "acme-kit is in app for Claude Code", on: "acme-kit is in app for Claude Code, Codex", out: "acme-kit is out of app", codex: "Codex runs a project's hooks only in a project you trust, once you've reviewed them in /hooks." },
  zh: { head: "项目插件", none: "还没有 Claude 插件", one: "acme-kit 已在 app 中对 Claude Code 开启", on: "acme-kit 已在 app 中对 Claude Code, Codex 开启", out: "acme-kit 已从 app 中移除", codex: "Codex 只在你信任的项目里运行项目的 hooks，并且要先在 /hooks 中审阅。" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a project's plugins, agent by agent", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
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
        assert.equal((await card.locator(".lib-tags").textContent()).trim(), w.none);
        assert.ok((await page.locator("#view-library .row-head .label").allTextContents()).includes(w.head));

        await card.locator(".lib-projhead").scrollIntoViewIfNeeded();
        const before = await scrolled(page);
        await press(page, card.locator(".lib-projhead"));
        const rows = card.locator(".lib-projplugin");
        await rows.first().waitFor();
        assert.equal(await rows.count(), 3);
        // #7267: a Pi package with only an extension says so on its row
        assert.match((await card.locator(".lib-projplugin").filter({ hasText: "pi-guard" }).locator(".lib-plparts").textContent()).trim(), /^\S.* 1$/);
        const row = () => card.locator(".lib-projplugin").filter({ hasText: "acme-kit" }).first();
        assert.deepEqual(await row().locator(".lib-ag").evaluateAll((cs) => cs.map((c) => c.dataset.agent)), ["claude", "codex", "pi"], "Goose takes nothing in a project");
        assert.equal(await card.locator(".lib-projhooks-codex").count(), 0);

        await chip(page, row().locator('.lib-ag[data-agent="claude"]'));
        await page.waitForFunction(() => document.querySelectorAll('#view-library .lib-projplugin .lib-ag.on[data-agent="claude"]').length === 1);
        await page.waitForFunction((s) => document.querySelector("#status").textContent.trim() === s, w.one);
        await chip(page, row().locator('.lib-ag[data-agent="codex"]'));
        await page.waitForFunction(() => document.querySelectorAll('#view-library .lib-projplugin .lib-ag.on').length === 2);
        await page.waitForFunction((s) => document.querySelector("#status").textContent.trim() === s, w.on);
        assert.equal((await card.locator(".lib-projhooks-codex").textContent()).trim(), w.codex);
        // no chip left takes the plugin out of the project
        await chip(page, row().locator('.lib-ag[data-agent="claude"]'));
        await page.waitForFunction(() => document.querySelectorAll('#view-library .lib-projplugin .lib-ag.on').length === 1);
        await chip(page, row().locator('.lib-ag[data-agent="codex"]'));
        await page.waitForFunction(() => document.querySelectorAll('#view-library .lib-projplugin .lib-ag.on').length === 0);
        await page.waitForFunction((s) => document.querySelector("#status").textContent.trim() === s, w.out);
        assert.deepEqual(posts, [
          { dir: DIR, name: "acme-kit", agents: ["claude"] }, { dir: DIR, name: "acme-kit", agents: ["claude", "codex"] },
          { dir: DIR, name: "acme-kit", agents: ["codex"] }, { dir: DIR, name: "acme-kit", agents: [] },
        ]);
        assert.equal(await scrolled(page), before, "the clicks scroll nothing");
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
