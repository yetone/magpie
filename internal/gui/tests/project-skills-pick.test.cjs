// Run with Node's test runner and Playwright on the module path; see README.md.
// POM_JUMPING on X: "skill加入本地项目是能否可选？之前添加到本地项目会把所有skill添加进去".
// An open project's card listed every library skill, one row each, as
// though all were in the project. It now lists the skills the project has,
// and its Skills menu (the app menu, the project's skills ticked) picks which
// go in: one ticked posts projects/skills for the agents the project's skills
// are for already (every agent that reads a project's skills, in a project
// with none yet), one unticked posts it with no agents. The skills it had
// keep their agents. In English and Chinese. No backend: the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/emo";
const APP = HOME + "/code/app", WEB = HOME + "/code/web";
const agent = (id, name, more = {}) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, ...more });
const sk = (name) => ({ name, description: name + " things", agents: ["claude", "codex"] });

function server(lang, posts) {
  const projects = {
    [APP]: { pdf: ["codex"] },
    [WEB]: {},
  };
  const lib = () => ({
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
    agents: [
      agent("claude", "Claude Code", { projectSkills: ".claude/skills" }),
      agent("codex", "Codex", { projectSkills: ".agents/skills" }),
      agent("goose", "Goose"),
    ],
    instructions: { agents: [] }, servers: [], skills: [sk("pdf"), sk("docx"), sk("xlsx")], foundServers: [], foundSkills: [],
    projects: Object.entries(projects).map(([dir, skills]) => ({ dir, name: path.basename(dir), copy: false, skills: { ...skills }, placed: [], servers: {}, wrote: {} })),
  });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib() });
    if (url.pathname === "/api/library/projects/skills") {
      const b = req.postDataJSON();
      posts.push(b);
      for (const n of b.names) {
        if (b.agents.length) projects[b.dir][n] = b.agents;
        else delete projects[b.dir][n];
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

const words = {
  en: { menu: "Skills", head: "Pick the skills it gets", none: "No skills yet", in: "docx is in app for Codex", out: "docx is out of app", both: "docx, xlsx are in web for Claude Code, Codex" },
  zh: { menu: "技能", head: "选择它要用的技能", none: "还没有技能", in: "docx 已放入 app，供 Codex 使用", out: "docx 已从 app 移除", both: "docx, xlsx 已放入 web，供 Claude Code, Codex 使用" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a project's skills, picked from its menu", async (t) => {
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
        const ctx = await browser.newContext({ viewport: { width: 860, height: 1300 } });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posts));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const app = page.locator("#view-library .lib-project").filter({ hasText: "~/code/app" });
        await app.waitFor();
        await app.locator(".lib-projhead").click();
        const rows = app.locator(".lib-projskill");
        await rows.first().waitFor();
        const names = async (loc) => loc.locator(".who > .name").evaluateAll((ns) => ns.map((n) => n.firstChild.textContent.trim()));
        assert.deepEqual(await names(rows), ["pdf"], "the open card lists the skills the project has, not every library skill");

        const status = (s) => page.waitForFunction((s) => document.querySelector("#status").textContent.trim() === s, s);
        const menu = page.locator(".proto-menu");
        const pick = app.locator(".lib-projpick");
        assert.equal((await pick.textContent()).trim(), w.menu);
        await pick.click();
        await menu.waitFor();
        assert.equal((await menu.locator(".pm-head").textContent()).trim(), w.head);
        assert.deepEqual(await menu.locator(".pm-item").evaluateAll((bs) => bs.map((b) => [b.textContent.trim(), b.getAttribute("aria-checked")])), [["pdf", "true"], ["docx", "false"], ["xlsx", "false"]]);
        await menu.locator(".pm-item", { hasText: "docx" }).click();
        assert.equal(posts.length, 0, "nothing is written while the menu is open");
        await page.keyboard.press("Escape");
        await status(w.in);
        assert.deepEqual(posts, [{ dir: APP, names: ["docx"], agents: ["codex"] }], "a skill ticked goes to the agents the project's skills are for");
        await page.waitForFunction(() => document.querySelectorAll("#view-library .lib-project.list .lib-projskill").length === 2);
        assert.deepEqual(await names(rows), ["pdf", "docx"]);

        // unticked: out of the project, the rest kept as they were
        await pick.click();
        await menu.waitFor();
        await menu.locator(".pm-item", { hasText: "docx" }).click();
        await page.keyboard.press("Escape");
        await status(w.out);
        assert.deepEqual(posts.at(-1), { dir: APP, names: ["docx"], agents: [] });
        await page.waitForFunction(() => document.querySelectorAll("#view-library .lib-projskill").length === 1);
        assert.deepEqual(await names(rows), ["pdf"]);
        assert.equal(posts.length, 2, "pdf was never written");

        // a project with no skills yet: its card says so, and two ticked go
        // in together, to every agent that reads a project's skills
        const web = page.locator("#view-library .lib-project").filter({ hasText: "~/code/web" });
        await web.locator(".lib-projhead").click();
        await web.locator(".lib-projnone").waitFor();
        assert.equal((await web.locator(".lib-projnone").textContent()).trim(), w.none);
        assert.equal(await web.locator(".lib-projskill").count(), 0);
        await web.locator(".lib-projpick").click();
        await menu.waitFor();
        await menu.locator(".pm-item", { hasText: "docx" }).click();
        await menu.locator(".pm-item", { hasText: "xlsx" }).click();
        await page.mouse.click(5, 5);
        await status(w.both);
        assert.deepEqual(posts.at(-1), { dir: WEB, names: ["docx", "xlsx"], agents: ["claude", "codex"] });
        await page.waitForFunction(() => [...document.querySelectorAll("#view-library .lib-project")].some((c) => c.querySelectorAll(".lib-projskill").length === 2 && c.textContent.includes("~/code/web")));
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
