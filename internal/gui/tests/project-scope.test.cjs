// Run with Node's test runner and Playwright on the module path; see README.md.
// WhyULose0 on X: a project should be the Library's global set ± its own,
// a global server or skill turned off for one project included. An open
// project's card lights a global server's chip for each agent the Library
// gives it; a click on Claude Code or Codex posts projects/server-off with
// the agents it is off for in that project, and a click on a server the
// Library gives no one posts projects/server as before. Gemini CLI can't
// have one turned off in a project, so its chip stays lit and says why. A
// global skill's Claude Code chip posts projects/skill-off; one that goes
// through the skill router can't be turned off and says so. In English
// and Chinese. No backend: the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/emo";
const DIR = HOME + "/code/design";
const agent = (id, name, more = {}) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json`, ...more });
const sv = (name, agents) => ({ name, transport: "stdio", command: name, url: "", agents });
const sk = (name, agents, more = {}) => ({ name, description: name + " things", agents, ...more });

function server(lang, posts) {
  const st = { serversOff: {}, servers: {}, skillsOff: {}, skills: { pdf: ["codex"] } };
  const lib = () => ({
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME, skillRouter: true,
    agents: [
      agent("claude", "Claude Code", { projectSkills: ".claude/skills", projectMCP: ".mcp.json", projectOff: true, projectSkillOff: true }),
      agent("codex", "Codex", { projectSkills: ".agents/skills", projectMCP: ".codex/config.toml", projectOff: true }),
      agent("gemini", "Gemini CLI", { projectSkills: ".agents/skills", projectMCP: ".gemini/settings.json" }),
    ],
    instructions: { agents: [] }, servers: [sv("search", ["claude", "codex", "gemini"]), sv("erp", [])],
    skills: [sk("novel", ["claude"], { router: "direct" }), sk("pdf", ["claude"])], foundServers: [], foundSkills: [],
    projects: [{ dir: DIR, name: "design", copy: false, skills: { ...st.skills }, placed: [], servers: { ...st.servers }, serversOff: { ...st.serversOff }, skillsOff: { ...st.skillsOff }, wrote: {} }],
  });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib() });
    const kind = { "/api/library/projects/server-off": "serversOff", "/api/library/projects/server": "servers", "/api/library/projects/skill-off": "skillsOff" }[url.pathname];
    if (kind) {
      const b = req.postDataJSON();
      posts.push({ path: url.pathname.slice("/api/library/".length), ...b });
      for (const n of b.names || [b.name]) {
        st[kind] = { ...st[kind], [n]: b.agents };
        if (!b.agents.length) delete st[kind][n];
      }
      return route.fulfill({ json: { ...lib(), result: { changed: [], problems: [] } } });
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

// a chip: the chips fan out under the pointer first, as a reader's hand
// does, and the chip is pressed where it is then
const chip = async (page, loc) => {
  const a = await loc.boundingBox();
  await page.mouse.move(a.x + a.width / 2, a.y + a.height / 2);
  await page.waitForTimeout(350);
  const b = await loc.boundingBox();
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
};
const lit = (row) => row.locator(".lib-ag.on").evaluateAll((cs) => cs.map((c) => c.dataset.agent));
const said = (page, s) => page.waitForFunction((s) => document.querySelector("#status").textContent.trim() === s, s);

const words = {
  en: { off: "search is off in design for Claude Code", off2: "search is off in design for Claude Code, Codex", off3: "search is off in design for Codex", erp: "erp is in design for Claude Code", skill: "novel is off in design for Claude Code", back: "novel is back on in design", cant: "Gemini CLI has it from the Library, and can't have it turned off in one project", routed: "skill router", tab: "Skills" },
  zh: { off: "search 已在 design 中对 Claude Code 关闭", off2: "search 已在 design 中对 Claude Code, Codex 关闭", off3: "search 已在 design 中对 Codex 关闭", erp: "erp 已写入 design，供 Claude Code 使用", skill: "novel 已在 design 中对 Claude Code 关闭", back: "novel 已在 design 中重新开启", tab: "技能" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a project is the global set, some of it turned off there", async (t) => {
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
        const ctx = await browser.newContext({ viewport: { width: 860, height: 1100 } });
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
        await card.locator(".lib-projhead").click();
        const row = (name) => card.locator(".lib-projserver").filter({ has: page.locator(".name", { hasText: new RegExp("^" + name) }) }).first();
        await row("search").waitFor();
        assert.deepEqual(await lit(row("search")), ["claude", "codex", "gemini"], "the global set is on in the project");
        assert.deepEqual(await lit(row("erp")), []);
        const gem = row("search").locator('.lib-ag[data-agent="gemini"]');
        assert.match(await gem.getAttribute("class"), /\balways\b/);
        if (w.cant) assert.equal(await gem.getAttribute("title"), w.cant);

        await chip(page, row("search").locator('.lib-ag[data-agent="claude"]'));
        await said(page, w.off);
        assert.deepEqual(await lit(row("search")), ["codex", "gemini"]);
        await chip(page, row("search").locator('.lib-ag[data-agent="codex"]'));
        await said(page, w.off2);
        await chip(page, row("search").locator('.lib-ag[data-agent="claude"]'));
        await said(page, w.off3);
        assert.deepEqual(await lit(row("search")), ["claude", "gemini"]);
        await chip(page, row("erp").locator('.lib-ag[data-agent="claude"]'));
        await said(page, w.erp);
        assert.deepEqual(posts, [
          { path: "projects/server-off", dir: DIR, name: "search", agents: ["claude"] },
          { path: "projects/server-off", dir: DIR, name: "search", agents: ["claude", "codex"] },
          { path: "projects/server-off", dir: DIR, name: "search", agents: ["codex"] },
          { path: "projects/server", dir: DIR, name: "erp", agents: ["claude"] },
        ]);

        // skills: novel goes direct and can be turned off; pdf goes
        // through the skill router and can't
        posts.length = 0;
        await page.locator(":is(#view-library .lib-tabs .opt, .nav-s):visible", { hasText: w.tab }).click();
        await card.locator(".lib-projskill, .lib-projnone").first().waitFor();
        const srow = (name) => card.locator(".lib-projskill").filter({ has: page.locator(".name", { hasText: new RegExp("^" + name) }) }).first();
        await srow("pdf").waitFor();
        const pdf = srow("pdf").locator('.lib-ag[data-agent="claude"]');
        assert.match(await pdf.getAttribute("class"), /\balways\b/);
        if (w.routed) assert.match(await pdf.getAttribute("title"), new RegExp(w.routed));
        // the Skills menu ticks a skill the project has from the Library;
        // unticking it turns it off there
        const menu = page.locator(".proto-menu");
        await card.locator(".lib-projpick").click();
        await menu.waitFor();
        assert.deepEqual(await menu.locator(".pm-item").evaluateAll((bs) => bs.map((b) => [b.textContent.trim(), b.getAttribute("aria-checked")])), [["novel", "true"], ["pdf", "true"]]);
        await menu.locator(".pm-item", { hasText: "novel" }).click();
        await page.keyboard.press("Escape");
        await said(page, w.skill);
        await srow("novel").waitFor();
        assert.deepEqual(await lit(srow("novel")), []);
        await chip(page, srow("novel").locator('.lib-ag[data-agent="claude"]'));
        await said(page, w.back);
        assert.deepEqual(await lit(srow("novel")), ["claude"]);
        assert.deepEqual(posts, [
          { path: "projects/skill-off", dir: DIR, names: ["novel"], agents: ["claude"] },
          { path: "projects/skill-off", dir: DIR, name: "novel", agents: [] },
        ]);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
