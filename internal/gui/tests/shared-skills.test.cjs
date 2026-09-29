// Run with Node's test runner and Playwright on the module path; see README.md.
// #227: the Library's Skills tab lists skills found in the user-wide shared
// ~/.agents/skills beside the agents' own: one row for a skill the agents
// only link (or junction) to, marked "shared in ~/.agents/skills" and with
// the agents that link to it, no "differs in" on it; a copy of the agents'
// own still flagged; Bring in saying it stays where it is and posting the
// name; in English and Chinese. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/aimer";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills` });
const lib = {
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("codex", "Codex"), agent("pi", "Pi"), agent("zcode", "ZCode")],
  instructions: { agents: [] }, servers: [], skills: [], foundServers: [], projects: [],
  foundSkills: [
    { name: "grilling", description: "Grill a plan", agents: [], shared: `${HOME}/.agents/skills/grilling`, link: "D:/aimer-skills/grilling" },
    { name: "orca-cli", description: "Orca", agents: [], others: ["zcode"], shared: `${HOME}/.agents/skills/orca-cli` },
    { name: "orchestration", description: "Orchestrate", agents: ["pi", "zcode"], shared: `${HOME}/.agents/skills/orchestration` },
    { name: "notes", description: "Mine", agents: ["codex"] },
  ],
};

function server(lang, posts) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib });
    if (url.pathname === "/api/library/skills/import") {
      posts.push(req.postDataJSON());
      return route.fulfill({ json: { ...lib, foundSkills: lib.foundSkills.filter((f) => f.name !== req.postDataJSON().name), result: { changed: [] } } });
    }
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
  test(engine + ": skills in ~/.agents/skills", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    const open = async (lang, posts) => {
      const ctx = await browser.newContext({ viewport: { width: 980, height: 800 } });
      await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, posts));
      await page.goto("http://magpie.test/");
      await page.locator('button[data-view="library"]').click();
      await page.locator("#view-library .lib-row").first().waitFor();
      return page;
    };
    const row = (page, name) => page.locator("#view-library .lib-row").filter({ has: page.locator(".name", { hasText: new RegExp("^" + name + "$") }) });

    await t.test("in English", async () => {
      const posts = [];
      const page = await open("en", posts);
      assert.equal(await page.locator("#view-library .lib-row").count(), 4);
      const orch = row(page, "orchestration");
      assert.equal(await orch.count(), 1, "one row for the shared folder and the agents' links to it");
      assert.match(await orch.locator(".lib-src").first().textContent(), /shared in\s*~\/\.agents\/skills\/orchestration/);
      assert.deepEqual(await orch.locator(".lib-have [title]").evaluateAll((es) => es.map((e) => e.title)), ["Pi", "ZCode"]);
      assert.equal(await orch.locator(".lib-tag.warn").count(), 0, "a link to the same folder isn't another skill");
      assert.match(await orch.locator("button.action").getAttribute("title"), /Keeps it where it is in the shared skills folder/);
      // a link in the shared folder to a folder elsewhere says both
      const src = await row(page, "grilling").locator(".lib-src").allTextContents();
      assert.equal(src.length, 2);
      assert.match(src[0], /shared in/);
      assert.match(src[1], /linked from\s*D:\/aimer-skills\/grilling/);
      // a copy of ZCode's own is still another
      assert.match(await row(page, "orca-cli").locator(".lib-tag.warn").textContent(), /differs in ZCode/);
      // an agent's own, as before
      const notes = row(page, "notes");
      assert.equal(await notes.locator(".lib-src").count(), 0);
      assert.match(await notes.locator("button.action").getAttribute("title"), /Moves it into the library/);
      await orch.locator("button.action").click();
      await page.waitForTimeout(300);
      assert.deepEqual(posts, [{ name: "orchestration" }]);
      assert.equal(await row(page, "orchestration").count(), 0);
    });

    await t.test("in Chinese", async () => {
      const page = await open("zh", []);
      const orch = row(page, "orchestration");
      assert.match(await orch.locator(".lib-src").first().textContent(), /共享于\s*~\/\.agents\/skills\/orchestration/);
      assert.match(await orch.locator("button.action").getAttribute("title"), /保留在共享技能文件夹中原处/);
    });

    assert.deepEqual(errors, []);
  });
}
