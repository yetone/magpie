// Run with Node's test runner and Playwright on the module path; see README.md.
// JasonLeeForOnly on Discord: the skills found in the agents (~/.agents/skills
// and the agents' own) were brought into the Library one click at a time.
// "Bring in all" beside "In your agents" posts every found skill's name at
// once, the rows go, and the toast says how many are in the library now, or
// the one that couldn't be brought in; the click scrolls nothing; in English
// and Chinese. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/jason";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills` });
const found = [
  { name: "grilling", description: "Grill a plan", agents: ["codex"], shared: `${HOME}/.agents/skills/grilling` },
  { name: "orca-cli", description: "Orca", agents: ["pi"], shared: `${HOME}/.agents/skills/orca-cli` },
  { name: "notes", description: "Mine", agents: ["codex"] },
];
const lib = (foundSkills) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("codex", "Codex"), agent("pi", "Pi")],
  instructions: { agents: [] }, servers: [], skills: [], foundServers: [], projects: [], foundSkills,
});

// fail, when given, is a skill the fake magpie can't bring in
function server(lang, posts, fail) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib(found) });
    if (url.pathname === "/api/library/skills/import-all") {
      const { names } = req.postDataJSON();
      posts.push(names);
      const left = found.filter((f) => !names.includes(f.name) || f.name === fail);
      const unimported = fail ? [{ what: "skill:" + fail, error: "no skill called " + fail + " in the agents" }] : undefined;
      return route.fulfill({ json: { ...lib(left), result: { changed: ["codex", "pi"], unimported } } });
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

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": bring in all the skills found", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    const open = async (lang, posts, fail) => {
      const ctx = await browser.newContext({ viewport: { width: 980, height: 800 } });
      await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, posts, fail));
      await page.goto("http://magpie.test/");
      await page.locator('button[data-view="library"]').click();
      await page.locator("#view-library .lib-body:not(.lib-skel) .lib-row").first().waitFor();
      return page;
    };

    await t.test("in English", async () => {
      const posts = [];
      const page = await open("en", posts);
      assert.equal(await page.locator("#view-library .lib-row:not(.lib-hub)").count(), 3);
      const all = page.locator("#view-library .row-head button.lib-importall");
      assert.equal((await all.textContent()).trim(), "Bring in all");
      assert.match(await all.getAttribute("title"), /Brings the 3 skills into the library/);
      const before = await scrolled(page);
      await all.click();
      await page.waitForTimeout(300);
      assert.deepEqual(posts, [["grilling", "orca-cli", "notes"]]);
      assert.equal(await page.locator("#view-library .lib-row:not(.lib-hub)").count(), 0, "the rows found are gone");
      assert.equal(await page.locator("#view-library button.lib-importall").count(), 0);
      assert.equal((await page.locator("#status").textContent()).trim(), "3 skills are in the library now");
      assert.equal(await scrolled(page), before, "the click scrolls nothing");
    });

    await t.test("in Chinese, one not brought in", async () => {
      const posts = [];
      const page = await open("zh", posts, "notes");
      const all = page.locator("#view-library .row-head button.lib-importall");
      assert.equal((await all.textContent()).trim(), "全部导入");
      assert.match(await all.getAttribute("title"), /将这 3 个技能导入资源库/);
      const before = await scrolled(page);
      await all.click();
      await page.waitForTimeout(300);
      assert.deepEqual(posts, [["grilling", "orca-cli", "notes"]]);
      assert.equal(await page.locator("#view-library .lib-row:not(.lib-hub)").count(), 1, "the one not brought in is still listed");
      assert.equal(await page.locator("#view-library button.lib-importall").count(), 0, "no Bring in all for a single row");
      assert.equal((await page.locator("#status").textContent()).trim(), "notes 未能导入：no skill called notes in the agents");
      assert.equal(await scrolled(page), before, "the click scrolls nothing");
    });

    assert.deepEqual(errors, []);
  });
}
