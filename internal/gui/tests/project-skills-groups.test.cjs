// Run with Node's test runner and Playwright on the module path; see README.md.
// Elorynth on X: adding skills to a project, show them grouped by source as
// the Library does, with a click on a group's heading for the whole group.
// The project's Skills menu lists the library's skills by source, a heading
// over each group that ticks (and unticks) all of its skills in one click,
// half-ticked while some are in. Typing a repository finds its skills. With
// one group only, the menu is the flat list it was. In every language, at
// phone width. No backend: the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/emo";
const APP = HOME + "/code/app", WEB = HOME + "/code/web";
const agent = (id, name, more = {}) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, ...more });
const sk = (name, repo) => ({ name, description: name + " things", agents: ["claude", "codex"], ...(repo ? { kind: "github", source: "https://github.com/" + repo + "/tree/main/" + name } : {}) });

function server(lang, posts) {
  const projects = {
    [APP]: { "kit-a": ["codex"] },
    [WEB]: {},
  };
  const lib = () => ({
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
    agents: [
      agent("claude", "Claude Code", { projectSkills: ".claude/skills" }),
      agent("codex", "Codex", { projectSkills: ".agents/skills" }),
      agent("goose", "Goose"),
    ],
    instructions: { agents: [] }, servers: [], skills: [sk("pdf"), sk("l1"), sk("l2"), sk("l3"), sk("l4"), sk("l5"), sk("kit-a", "acme/kit"), sk("kit-b", "acme/kit"), sk("tool-c", "beta/tools")], foundServers: [], foundSkills: [],
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

const heads = { en: "On this computer", zh: "本机", "zh-TW": "本機", ja: "このコンピュータ", de: "Auf diesem Computer" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a project's Skills menu is grouped by source", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => { if (errors.length) console.log(errors); await browser.close(); });
    for (const lang of Object.keys(heads)) {
      await t.test(lang, async () => {
        const posts = [];
        const ctx = await browser.newContext({ viewport: { width: 400, height: 2400 } });
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
        await app.locator(".lib-projskill").first().waitFor();
        const menu = page.locator(".proto-menu");
        await app.locator(".lib-projpick").click();
        await menu.waitFor();
        const state = () => menu.locator(".pm-item").evaluateAll((bs) => bs.map((b) => [b.textContent.trim(), b.getAttribute("aria-checked"), b.classList.contains("mixed")]));
        assert.deepEqual((await state()).slice(0, 5), [
          ["acme/kit", "false", true], ["kit-a", "true", false], ["kit-b", "false", false],
          ["beta/tools", "false", false], ["tool-c", "false", false],
        ], "a heading over each source, half-ticked while some of its skills are in");
        assert.equal((await state())[5][0], heads[lang], "this computer's skills last");
        const box = await menu.boundingBox();
        assert(box.x >= 0 && box.x + box.width <= 400, "the menu fits a phone");

        // one click ticks the whole group, the next takes it out
        await menu.locator(".pm-group", { hasText: "acme/kit" }).click();
        assert.deepEqual((await state()).slice(0, 3), [["acme/kit", "true", false], ["kit-a", "true", false], ["kit-b", "true", false]]);
        await menu.locator(".pm-group", { hasText: "beta/tools" }).click();
        await menu.locator(".pm-group", { hasText: "beta/tools" }).click();
        assert.equal(posts.length, 0, "nothing is written while the menu is open");
        await menu.locator(".pm-group", { hasText: "acme/kit" }).click();
        assert.deepEqual((await state()).slice(0, 3), [["acme/kit", "false", false], ["kit-a", "false", false], ["kit-b", "false", false]]);
        await menu.locator(".pm-group", { hasText: "acme/kit" }).click();

        // the repository typed finds its skills, under their heading
        const q = menu.locator(".sc-q");
        await q.fill("beta");
        assert.deepEqual(await menu.locator(".pm-item:not([hidden])").evaluateAll((bs) => bs.map((b) => b.textContent.trim())), ["beta/tools", "tool-c"]);
        await q.fill("");
        await page.keyboard.press("Escape");
        await page.waitForFunction(() => document.querySelector("#status")?.textContent.trim());
        assert.deepEqual(posts, [{ dir: APP, names: ["kit-b"], agents: ["codex"] }], "the group's other skill goes in for the project's agents");
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
