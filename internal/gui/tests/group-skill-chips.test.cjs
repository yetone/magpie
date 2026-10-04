// Run with Node's test runner and Playwright on the module path; see README.md.
// A repository's skills are given to an agent, or taken from it, at once
// from its group's heading (#787, mintonight: 我记得昨天还能直接一次性开关
// 一个作者的所有技能包): each agent's chip there is lit when it has all of
// them, half lit when it has some, and a click gives it every one or takes
// them all; All does it for every agent. A click doesn't fold the group.
// English and Chinese; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/me";
const agent = (id, name, icon) => ({ id, name, icon, skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const gh = (repo, name, agents) => ({ name, kind: "github", source: `https://github.com/${repo}/tree/main/skills/${name}`, description: "d", agents });

function server(lang, state, posts) {
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(state.lib);
    if (url.pathname === "/api/library/skills/agents-some") {
      const b = req.postDataJSON();
      posts.push(b);
      for (const s of state.lib.skills) {
        if (!b.names.includes(s.name)) continue;
        const kept = s.agents.filter((a) => !b.agents.includes(a));
        s.agents = (b.on ? [...kept, ...b.agents] : kept).sort();
      }
      return json({ ...state.lib, result: { changed: b.agents } });
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { on: "acme/kit's skills are on for Codex", off: "acme/kit's skills are off for Claude Code", all: "acme/kit's skills are on for Claude Code", some: "Codex has 1 of them — click to give it the rest" },
  zh: { on: "已为 Codex 启用 acme/kit 的全部技能", off: "已从 Claude Code 中关闭 acme/kit 的全部技能", all: "已为 Claude Code 启用 acme/kit 的全部技能", some: "Codex 已有其中 1 个，点击补齐其余的" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a repository's skills on or off for an agent at once", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = L[lang];
        const state = {
          lib: {
            dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
            agents: [agent("claude", "Claude Code", "claudecode-color"), agent("codex", "Codex", "openai")],
            instructions: { agents: [], sets: [] }, servers: [], foundServers: [], projects: [], foundSkills: [], problems: [],
            skills: [gh("acme/kit", "mango", ["claude", "codex"]), gh("acme/kit", "cherry", ["claude"]), gh("zed/tools", "pear", [])],
          },
        };
        const posts = [];
        const ctx = await browser.newContext({ viewport: { width: 980, height: 760 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, state, posts));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const head = page.locator("#view-library .lib-grouphead").filter({ has: page.locator(".name", { hasText: /^acme\/kit$/ }) });
        await head.waitFor();
        const open = () => head.evaluate((h) => h.classList.contains("open"));
        const folded = await open();
        const chip = (id) => head.locator(`.lib-groupchips .lib-ag[data-agent="${id}"]`);
        const lit = (id) => chip(id).getAttribute("aria-pressed");
        const some = (id) => chip(id).evaluate((c) => c.classList.contains("some"));
        const agentsOf = (name) => state.lib.skills.find((s) => s.name === name).agents;

        // Claude Code has both, Codex one
        assert.equal(await lit("claude"), "true");
        assert.equal(await lit("codex"), "false");
        assert.equal(await some("codex"), true);
        assert.equal(await some("claude"), false);
        assert.equal(await chip("codex").getAttribute("title"), w.some);

        // Codex is given the rest; the group stays as it was
        await chip("codex").click();
        await page.locator("#status").filter({ hasText: w.on }).waitFor();
        assert.deepEqual(posts.shift(), { names: ["cherry", "mango"], agents: ["codex"], on: true });
        assert.deepEqual(agentsOf("cherry"), ["claude", "codex"]);
        assert.deepEqual(agentsOf("pear"), [], "another repository's skill is left alone");
        await page.waitForFunction(() => document.querySelector('#view-library .lib-grouphead .lib-groupchips .lib-ag[data-agent="codex"]')?.getAttribute("aria-pressed") === "true");
        assert.equal(await some("codex"), false);
        assert.equal(await open(), folded, "the click folded the group");
        assert.equal(await head.locator(".lib-groupchips .lib-ag.all").getAttribute("aria-pressed"), "true");

        // Claude Code's are taken away
        await chip("claude").click();
        await page.locator("#status").filter({ hasText: w.off }).waitFor();
        assert.deepEqual(posts.shift(), { names: ["cherry", "mango"], agents: ["claude"], on: false });
        assert.deepEqual(agentsOf("mango"), ["codex"]);
        await page.waitForFunction(() => document.querySelector('#view-library .lib-grouphead .lib-groupchips .lib-ag[data-agent="claude"]')?.getAttribute("aria-pressed") === "false");

        // All gives them back to every agent, then takes them from every one
        await head.locator(".lib-groupchips .lib-ag.all").click();
        await page.locator("#status").filter({ hasText: w.all }).waitFor();
        assert.deepEqual(posts.shift(), { names: ["cherry", "mango"], agents: ["claude"], on: true });
        await page.waitForFunction(() => document.querySelector("#view-library .lib-grouphead .lib-groupchips .lib-ag.all")?.getAttribute("aria-pressed") === "true");
        await head.locator(".lib-groupchips .lib-ag.all").click();
        await page.waitForFunction(() => document.querySelector("#view-library .lib-grouphead .lib-groupchips .lib-ag.all")?.getAttribute("aria-pressed") === "false");
        assert.deepEqual(posts.shift(), { names: ["cherry", "mango"], agents: ["claude", "codex"], on: false });
        assert.deepEqual(agentsOf("mango"), []);
        assert.equal(await open(), folded);
        assert.equal(await head.count(), 1);

        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
