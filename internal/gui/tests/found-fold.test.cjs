// Run with Node's test runner and Playwright on the module path; see README.md.
// The owner: 52 skills "In your agents", three lines each, took the whole
// Skills page. A long list shows its first 5 rows and "Show 47 more"; the
// click shows them all and "Show less" folds them again, without scrolling.
// A short one (7 or fewer) is shown whole, Select shows every row to pick,
// and "Bring in all" still brings in every one, folded or not. MCP servers
// "In your agents" fold the same way. In English and Chinese. No backend.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/jason";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/config.toml` });
const skills = (n) => Array.from({ length: n }, (_, i) => ({ name: "skill-" + String(i).padStart(2, "0"), description: "Skill " + i, agents: ["codex"], shared: `${HOME}/.agents/skills/skill-${i}` }));
const servers = (n) => Array.from({ length: n }, (_, i) => ({ server: { name: "srv-" + i, transport: "stdio", command: "srv-" + i + "-mcp", agents: ["codex"] } }));
let found = skills(52), foundServers = [];
const lib = (foundSkills) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("codex", "Codex"), agent("pi", "Pi")],
  instructions: { agents: [] }, servers: [], skills: [], foundServers, projects: [], foundSkills,
});

function server(lang, posts) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib(found) });
    if (url.pathname === "/api/library/skills/import-all") {
      const { names } = req.postDataJSON();
      posts.push(names);
      found = found.filter((f) => !names.includes(f.name));
      return route.fulfill({ json: { ...lib(found), result: { changed: ["codex"] } } });
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

const rows = (page) => page.locator("#view-library .lib-list > .lib-row.lib-found");

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a long In your agents list is folded", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      await browser.close();
    });
    const open = async (lang, posts, tab = "skills") => {
      const ctx = await browser.newContext({ viewport: { width: 980, height: 800 } });
      await ctx.addInitScript((tab) => { try { localStorage.setItem("magpie.libTab", tab); } catch {} }, tab);
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, posts));
      await page.goto("http://magpie.test/");
      await page.locator('button[data-view="library"]').click();
      await page.locator("#view-library .lib-body:not(.lib-skel) .lib-row").first().waitFor();
      return page;
    };

    await t.test("in English: 5 rows, Show 47 more, Show less", async () => {
      found = skills(52);
      const page = await open("en", []);
      assert.equal(await rows(page).count(), 5);
      const more = page.locator("#view-library button.lib-foundmore");
      assert.equal((await more.textContent()).trim(), "Show 47 more");
      assert.equal(await more.getAttribute("aria-expanded"), "false");
      const before = await scrolled(page);
      await more.click();
      await page.waitForTimeout(200);
      assert.equal(await rows(page).count(), 52);
      assert.equal((await more.textContent()).trim(), "Show less");
      assert.equal(await more.getAttribute("aria-expanded"), "true");
      assert.equal(await scrolled(page), before, "the click scrolls nothing");
      await more.click();
      await page.waitForTimeout(200);
      assert.equal(await rows(page).count(), 5);
    });

    await t.test("in Chinese, Select shows every row, Bring in all takes all 52", async () => {
      found = skills(52);
      const posts = [];
      const page = await open("zh", posts);
      assert.equal((await page.locator("#view-library button.lib-foundmore").textContent()).trim(), "显示其余 47 个");
      await page.locator("#view-library button.lib-foundselect").click();
      await page.waitForTimeout(200);
      assert.equal(await rows(page).count(), 52, "picking, every row is there");
      assert.equal(await page.locator("#view-library button.lib-foundmore").count(), 0);
      await page.locator("#view-library .lib-pickdone").click();
      await page.waitForTimeout(200);
      assert.equal(await rows(page).count(), 5);
      await page.locator("#view-library button.lib-importall").click();
      await page.waitForTimeout(300);
      assert.equal(posts[0].length, 52);
    });

    await t.test("7 or fewer are shown whole", async () => {
      found = skills(7);
      const page = await open("en", []);
      assert.equal(await rows(page).count(), 7);
      assert.equal(await page.locator("#view-library button.lib-foundmore").count(), 0);
      found = skills(8);
      const p8 = await open("en", []);
      assert.equal(await rows(p8).count(), 5);
      assert.equal((await p8.locator("#view-library button.lib-foundmore").textContent()).trim(), "Show 3 more");
    });

    await t.test("MCP servers in the agents fold the same way", async () => {
      found = [];
      foundServers = servers(20);
      const page = await open("en", [], "mcp");
      const srows = page.locator("#view-library .lib-list > .lib-row");
      await page.locator("#view-library button.lib-foundmore").waitFor();
      assert.equal(await srows.count(), 5);
      assert.equal((await page.locator("#view-library button.lib-foundmore").textContent()).trim(), "Show 15 more");
      foundServers = [];
    });

    assert.deepEqual(errors, []);
  });
}
