// Run with Node's test runner and Playwright on the module path; see README.md.
// (VictorHong1022 on X) The Instructions tab lists the projects under the
// shared instructions: a card opens to the project's own text and chips for
// the agents that read a project's instructions file. The page's Save sends
// the text, to the agents the shared instructions are on for when the
// project has none yet; a chip sends the project's agents. An agent with no
// project file has no chip. The clicks scroll nothing; in English and
// Chinese. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/emo";
const DIR = HOME + "/code/app";
const agent = (id, name, more = {}) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json`, instructions: `${HOME}/.${id}/AGENTS.md`, ...more });
const lib = (pr) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [
    agent("claude", "Claude Code", { projectInstructions: "CLAUDE.md" }),
    agent("codex", "Codex", { projectInstructions: "AGENTS.md" }),
    agent("cursor", "Cursor", { projectInstructions: "AGENTS.md" }),
    agent("goose", "Goose"),
  ],
  instructions: {
    shared: "Be brief.",
    sets: [{ id: "default", name: "", text: "Be brief.", active: true }],
    agents: [
      { agent: "claude", name: "Claude Code", icon: "", path: `${HOME}/.claude/CLAUDE.md`, on: true, extra: "", own: 0 },
      { agent: "codex", name: "Codex", icon: "", path: `${HOME}/.codex/AGENTS.md`, on: false, extra: "", own: 0 },
    ],
  },
  servers: [], subagents: [], skills: [], foundServers: [], foundSkills: [],
  projects: [{ dir: DIR, name: "app", copy: false, skills: {}, placed: [], servers: {}, subagents: {}, wrote: {}, ...pr }],
});

function server(lang, posts) {
  let pr = { instr: "", instrAgents: [], instrFiles: [] };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib(pr) });
    if (url.pathname === "/api/library/projects/instructions") {
      const b = req.postDataJSON();
      posts.push(b);
      pr = { ...pr };
      if (b.instr !== undefined) pr.instr = b.instr;
      if (b.agents) pr.instrAgents = b.agents;
      pr.instrFiles = [...new Set(pr.instrAgents.map((id) => (id === "claude" ? "CLAUDE.md" : "AGENTS.md")))].sort();
      return route.fulfill({ json: { ...lib(pr), result: { changed: [], problems: [] } } });
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
const press = async (page, loc) => {
  const b = await loc.boundingBox();
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
};
const chip = async (page, loc) => {
  const a = await loc.boundingBox();
  await page.mouse.move(a.x + a.width / 2, a.y + a.height / 2);
  await page.waitForTimeout(350);
  await press(page, loc);
};

const words = {
  en: { head: "Project instructions", none: "No instructions yet", saved: "app's instructions are in its files for Claude Code", on: "app's instructions are in its files for Claude Code, Codex" },
  zh: { head: "项目指令", none: "还没有指令", saved: "app 的指令已写入 Claude Code 读取的文件", on: "app 的指令已写入 Claude Code, Codex 读取的文件" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a project's own instructions", async (t) => {
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
        const ctx = await browser.newContext({ viewport: { width: 860, height: 1000 } });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "instructions"); } catch {} });
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
        const ta = card.locator("textarea.lib-text");
        await ta.waitFor();
        assert.deepEqual(await card.locator(".lib-ag").evaluateAll((cs) => cs.map((c) => c.dataset.agent)), ["claude", "codex", "cursor"], "Goose reads no project file");
        assert.equal(await card.locator(".lib-ag.on").count(), 0);

        const bar = page.locator("#view-library .lib-savebar");
        assert.equal(await bar.isHidden(), true);
        await ta.fill("Use pnpm here.");
        await bar.waitFor();
        // the page's Save: to the agents the shared instructions are on for
        await press(page, bar.locator("button.primary"));
        await page.waitForFunction((s) => document.querySelector("#status").textContent.trim() === s, w.saved);
        await page.waitForFunction(() => document.querySelectorAll('#view-library .lib-project .lib-ag.on[data-agent="claude"]').length === 1);
        assert.equal(await bar.isHidden(), true);
        assert.equal(await card.locator(".lib-projinstr-agents .sub").textContent(), "CLAUDE.md");
        // Cursor reads Codex's AGENTS.md: through it, once Codex has it
        await chip(page, card.locator('.lib-ag[data-agent="codex"]'));
        await page.waitForFunction((s) => document.querySelector("#status").textContent.trim() === s, w.on);
        await page.waitForFunction(() => document.querySelector('#view-library .lib-project .lib-ag[data-agent="cursor"]')?.classList.contains("via"));
        assert.equal(await card.locator(".lib-projinstr-agents .sub").textContent(), "AGENTS.md, CLAUDE.md");
        assert.deepEqual(posts, [{ dir: DIR, instr: "Use pnpm here.", agents: ["claude"] }, { dir: DIR, name: "", agents: ["claude", "codex"] }]);
        assert.equal(await scrolled(page), before, "the clicks scroll nothing");

        // closed, the card shows the text's first line
        await press(page, card.locator(".lib-projhead"));
        await page.waitForFunction(() => document.querySelector("#view-library .lib-project .lib-tags")?.textContent.includes("Use pnpm here."));
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
