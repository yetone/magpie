// Run with Node's test runner and Playwright on the module path; see README.md.
// A skill made in the Library (josephBenchap on X): Skills > New skill takes
// a name and a description and the agents, posts library/skills/create, and
// opens the new skill in the reader. A refusal is said in the dialog and the
// dialog stays. No native select, no sideways scroll. English and Chinese,
// Chromium and WebKit, 1100 and 420px. The API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/emo";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const lib = (skills) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("codex", "Codex"), agent("claude", "Claude Code")],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills, servers: [],
});

function server(lang, posts, state) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib(state.skills) });
    if (url.pathname === "/api/library/skills/create") {
      const b = req.postDataJSON();
      posts.push(b);
      if (b.name === "taken") return route.fulfill({ status: 400, body: "there is a skill called taken already" });
      state.skills = [{ name: b.name, description: b.description, agents: b.agents }];
      return route.fulfill({ json: { ...lib(state.skills), result: { changed: b.agents, problems: [] } } });
    }
    if (url.pathname === "/api/library/skill") return route.fulfill({ json: { text: `---\nname: ${url.searchParams.get("name")}\n---\n`, path: `${HOME}/.magpie/library/skills/${url.searchParams.get("name")}/SKILL.md` } });
    if (url.pathname === "/api/library/rtk") return route.fulfill({ json: {} });
    if (url.pathname === "/api/davsync") return route.fulfill({ json: { on: false } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Library makes a skill of the user's own", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => { if (errors.length) console.log(errors); await browser.close(); });
    for (const lang of ["en", "zh"]) {
      for (const width of [1100, 420]) {
        await t.test(`${lang} ${width}`, async () => {
          const ctx = await browser.newContext({ viewport: { width, height: 800 } });
          await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
          const page = await ctx.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          const posts = [], state = { skills: [] };
          await page.route("**/*", server(lang, posts, state));
          await page.goto("http://magpie.test/");
          await page.locator('button[data-view="library"]').click();
          const v = page.locator("#view-library");
          await v.locator("[data-lib=new-skill]").click();
          const dlg = page.locator(".lib-newskill");
          await dlg.waitFor();
          assert.equal(await dlg.locator("select").count(), 0, "native select");
          const sideways = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
          assert(sideways <= 0, `page scrolls sideways by ${sideways}px`);
          // a refusal stays in the dialog
          await dlg.locator("input").first().fill("taken");
          await dlg.locator("textarea").fill("Does a thing");
          await dlg.locator("button.primary").click();
          await dlg.locator(".editor-error").filter({ hasText: "taken" }).waitFor();
          assert.equal(posts.length, 1);
          // one agent unticked, then created
          await dlg.locator("input").first().fill("my-skill");
          await dlg.locator(".lib-ag[data-agent=claude]").click();
          await dlg.locator("button.primary").click();
          await page.locator(".lib-reader").waitFor();
          assert.deepEqual(posts[1], { name: "my-skill", description: "Does a thing", agents: ["codex"] });
          await page.locator(".lib-reader .lib-md").filter({ hasText: "my-skill" }).waitFor();
          await ctx.close();
        });
      }
    }
  });
}
