// Run with Node's test runner and Playwright on the module path; see README.md.
// (jinpwng on X) The Subagents tab lists the projects: a card opens to each
// library subagent with chips for the agents that read a project's own
// subagent folder, and a chip posts that project's agents for the subagent to
// projects/subagents. An agent with no project folder has no chip. The click
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
const sv = (name, transport) => ({ name, transport, command: transport === "stdio" ? name : "", url: transport === "stdio" ? "" : "http://localhost:9/" + name, agents: [] });
const lib = (servers) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [
    agent("claude", "Claude Code", { projectSubagents: ".claude/agents" }),
    agent("codex", "Codex", { projectSubagents: ".codex/agents" }),
    agent("goose", "Goose"),
  ],
  instructions: { agents: [] }, servers: [], subagents: [{ name: "reviewer", description: "Reviews", agents: [] }, { name: "tester", description: "Tests", agents: [] }], skills: [], foundServers: [], foundSkills: [],
  projects: [{ dir: DIR, name: "app", copy: false, skills: {}, placed: [], servers: {}, subagents: servers, wrote: {} }],
});

function server(lang, posts) {
  let servers = {};
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib(servers) });
    if (url.pathname === "/api/library/projects/subagents") {
      const b = req.postDataJSON();
      posts.push(b);
      if (posts.hold) await posts.hold;
      servers = { ...servers, [b.name]: b.agents };
      if (!b.agents.length) delete servers[b.name];
      return route.fulfill({ json: { ...lib(servers), result: { changed: [], problems: [] } } });
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
const chip = async (page, loc) => {
  const a = await loc.boundingBox();
  await page.mouse.move(a.x + a.width / 2, a.y + a.height / 2);
  await page.waitForTimeout(350);
  await press(page, loc);
};

const words = {
  en: { head: "Project subagents", none: "No subagents yet", one: "reviewer is in app for Claude Code", on: "reviewer is in app for Claude Code, Codex" },
  zh: { head: "项目子代理", none: "还没有子代理", one: "reviewer 已在 app 中对 Claude Code 开启", on: "reviewer 已在 app 中对 Claude Code, Codex 开启" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a project's subagents, agent by agent", async (t) => {
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
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "subagents"); } catch {} });
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
        const rows = card.locator(".lib-projsubagent");
        await rows.first().waitFor();
        assert.equal(await rows.count(), 2);
        const row = rows.filter({ hasText: "reviewer" }).first();
        assert.deepEqual(await row.locator(".lib-ag").evaluateAll((cs) => cs.map((c) => c.dataset.agent)), ["claude", "codex"], "Goose reads no project folder");

        await chip(page, row.locator('.lib-ag[data-agent="claude"]'));
        await page.waitForFunction(() => document.querySelectorAll('#view-library .lib-projsubagent .lib-ag.on[data-agent="claude"]').length === 1);
        await page.waitForFunction((s) => document.querySelector("#status").textContent.trim() === s, w.one);
        await chip(page, card.locator(".lib-projsubagent").first().locator('.lib-ag[data-agent="codex"]'));
        await page.waitForFunction(() => document.querySelectorAll('#view-library .lib-projsubagent .lib-ag.on').length === 2);
        await page.waitForFunction((s) => document.querySelector("#status").textContent.trim() === s, w.on);
        assert.deepEqual(posts, [{ dir: DIR, name: "reviewer", agents: ["claude"] }, { dir: DIR, name: "reviewer", agents: ["claude", "codex"] }]);
        assert.equal(await scrolled(page), before, "the clicks scroll nothing");
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
