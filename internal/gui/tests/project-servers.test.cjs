// Run with Node's test runner and Playwright on the module path; see README.md.
// #500: the library's MCP servers could only be given to agents user-wide.
// The MCP tab now lists the projects (the same ones Skills has) under "In
// projects": a project's card opens to each server with chips for the agents
// that read a project's own servers (.mcp.json, .codex/config.toml…), and a
// chip posts that project's agents for the server to projects/server. An
// agent with no project file has no chip; Codex's is greyed out for an SSE
// server. The clicks scroll nothing; in English and Chinese. No backend: the
// API is faked here.
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
    agent("claude", "Claude Code", { projectSkills: ".claude/skills", projectMCP: ".mcp.json" }),
    agent("codex", "Codex", { noSSE: true, projectSkills: ".agents/skills", projectMCP: ".codex/config.toml" }),
    agent("goose", "Goose"),
  ],
  instructions: { agents: [] }, servers: [sv("fs", "stdio"), sv("web", "sse")], skills: [], foundServers: [], foundSkills: [],
  projects: [{ dir: DIR, name: "app", copy: false, skills: {}, placed: [], servers, wrote: {} }],
});

function server(lang, posts) {
  let servers = {};
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib(servers) });
    if (url.pathname === "/api/library/projects/server") {
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
  en: { head: "Project servers", none: "No servers yet", one: "fs is in app for Claude Code", on: "fs is in app for Claude Code, Codex", off: "fs is out of app", foot: "Written into .mcp.json, .codex/config.toml of the project" },
  zh: { head: "项目服务器", none: "还没有服务器", one: "fs 已写入 app，供 Claude Code 使用", on: "fs 已写入 app，供 Claude Code, Codex 使用", off: "fs 已从 app 移除", foot: "写入项目的 .mcp.json, .codex/config.toml" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a project's MCP servers, agent by agent", async (t) => {
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
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
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
        // the section says what it holds, never "In projects" (the owner, 11-12)
        assert.ok((await page.locator("#view-library .row-head .label").allTextContents()).includes(w.head));

        await card.locator(".lib-projhead").scrollIntoViewIfNeeded();
        const before = await scrolled(page);
        await press(page, card.locator(".lib-projhead"));
        const rows = card.locator(".lib-projserver");
        await rows.first().waitFor();
        assert.equal(await rows.count(), 2);
        const fsRow = rows.filter({ hasText: "fs" }).first(), webRow = rows.filter({ hasText: "web" }).first();
        assert.deepEqual(await fsRow.locator(".lib-ag").evaluateAll((cs) => cs.map((c) => c.dataset.agent)), ["claude", "codex"], "Goose reads no project file");
        assert.equal(await webRow.locator('.lib-ag[data-agent="codex"]').isDisabled(), true, "Codex can't reach SSE");
        assert.match(await card.textContent(), new RegExp(w.foot.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));

        await chip(page, fsRow.locator('.lib-ag[data-agent="claude"]'));
        await page.waitForFunction(() => document.querySelectorAll('#view-library .lib-projserver .lib-ag.on[data-agent="claude"]').length === 1);
        // the next chip is pressed once the row is drawn again
        await page.waitForFunction((s) => document.querySelector("#status").textContent.trim() === s, w.one);
        await chip(page, card.locator('.lib-projserver').first().locator('.lib-ag[data-agent="codex"]'));
        await page.waitForFunction(() => document.querySelectorAll('#view-library .lib-projserver .lib-ag.on').length === 2);
        await page.waitForFunction((s) => document.querySelector("#status").textContent.trim() === s, w.on);
        assert.deepEqual(posts, [{ dir: DIR, name: "fs", agents: ["claude"] }, { dir: DIR, name: "fs", agents: ["claude", "codex"] }]);

        // two clicks before magpie answers the first: the second counts
        // from the first, and the last wins
        const at = async (id) => { const b = await card.locator('.lib-projserver').first().locator(`.lib-ag[data-agent="${id}"]`).boundingBox(); return [b.x + b.width / 2, b.y + b.height / 2]; };
        // the chips fan out under the pointer: they're measured fanned
        await page.mouse.move(...(await at("claude")));
        await page.waitForTimeout(350);
        const [cl, cx] = [await at("claude"), await at("codex")];
        // magpie holds its answer to the first till the second is in
        let answer;
        posts.hold = new Promise((r) => { answer = r; });
        const n = posts.length;
        await page.mouse.click(...cl);
        await page.waitForFunction(() => !document.querySelector('#view-library .lib-projserver .lib-ag.on[data-agent="claude"]'));
        await page.mouse.click(...cx);
        assert.equal(posts.length, n + 1, "one write at a time");
        posts.hold = null;
        answer();
        await page.waitForFunction(() => !document.querySelector('#view-library .lib-projserver .lib-ag.on'));
        await page.waitForFunction((s) => document.querySelector("#status").textContent.trim() === s, w.off);
        assert.deepEqual(posts.at(-1), { dir: DIR, name: "fs", agents: [] });
        assert.equal(await scrolled(page), before, "the clicks scroll nothing");
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
