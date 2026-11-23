// Run with Node's test runner and Playwright on the module path; see README.md.
// jinpwng: a project with linked worktrees has "Only this checkout" on its
// open card (#7252): a switch that posts projects/main-only {dir, on},
// and a line saying what the other worktrees get. A project with no
// worktree has no such switch. The Pi entries another build kept by
// category, which magpie can't carry (#7282), are rows marked Not carried
// with nothing to press. Chromium and WebKit, every language, at 420 and
// 1100 with nothing wider than the window. No backend: the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/emo";
const DIR = HOME + "/code/app", LONE = HOME + "/code/lone";
const agent = (id, name, more = {}) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json`, ...more });
const proj = (dir, name, more) => ({ dir, name, copy: false, skills: {}, placed: [], servers: {}, subagents: {}, plugins: {}, hooks: {}, wrote: {}, pi: [], own: {}, ...more });

let I18N;
function tr(lang, key, vars = {}) {
  let s = key;
  if (lang !== "en") {
    assert.equal(typeof I18N[lang][key], "string", `${lang} has no ${JSON.stringify(key)}`);
    s = I18N[lang][key];
  }
  return s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m));
}

function server(lang, posts) {
  let mainOnly = false;
  const lib = () => ({
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
    agents: [
      agent("claude", "Claude Code", { projectSkills: ".claude/skills", projectMCP: ".mcp.json" }),
      agent("pi", "Pi", { projectSkills: ".agents/skills", projectMCP: ".pi/mcp.json" }),
    ],
    instructions: { agents: [] }, servers: [], subagents: [], skills: [], foundServers: [], foundSkills: [], hooks: [], hookAgents: [], plugins: [], claudeInstalled: [{ id: "fmt@acme", on: false }],
    projects: [
      proj(DIR, "app", { mainOnly, trees: mainOnly ? [] : [HOME + "/code/app-wt", HOME + "/code/app-fix"], pi: [{ source: "npm:pi-a", name: "pi-a", state: "on", adopted: true }], piNotCarried: [{ kind: "extension", source: "./ext/guard.ts" }] }),
      proj(LONE, "lone"),
    ],
  });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib() });
    if (url.pathname === "/api/library/projects/main-only") {
      const b = req.postDataJSON();
      posts.push(b);
      mainOnly = b.on;
      return route.fulfill({ json: { ...lib(), result: { changed: [], problems: [] } } });
    }
    if (url.pathname === "/api/library/native") return route.fulfill({ json: [] });
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

const press = async (page, loc) => {
  const b = await loc.boundingBox();
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
};
const sideways = (page) => page.evaluate(() => {
  const over = [];
  const w = document.documentElement.clientWidth;
  if (document.documentElement.scrollWidth > w) over.push("page " + document.documentElement.scrollWidth);
  for (const e of document.querySelectorAll("#view-library .lib-project *")) {
    const r = e.getBoundingClientRect();
    if (r.width && (r.right > w + 0.5 || r.left < -0.5)) over.push((e.className || e.tagName) + " " + Math.round(r.left) + "–" + Math.round(r.right));
  }
  return over;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a project with worktrees can keep to its own checkout", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    I18N = strings().I18N;
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1100, 420]) {
      await t.test(`${lang} at ${width}px`, async () => {
        const w = (key, vars) => tr(lang, key, vars);
        const posts = [];
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "plugins"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posts));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const card = page.locator("#view-library .lib-project").filter({ has: page.locator(".lib-projhead", { hasText: "app" }) }).first();
        await card.waitFor();
        await press(page, card.locator(".lib-projhead"));
        const main = card.locator(".lib-projmain");
        await main.waitFor();
        assert.equal((await main.locator("label").textContent()).trim(), w("Only this checkout"));
        assert.equal((await main.locator(".note").textContent()).trim(), w("The repository's {n} other worktrees get what this project has.", { n: 2 }));
        // what isn't carried: a row, a tag, nothing to press
        const left = card.locator(".lib-projpileft");
        assert.equal(await left.count(), 1);
        assert.equal(await left.getAttribute("data-source"), "./ext/guard.ts");
        assert.equal((await left.locator(".lib-tag").textContent()).trim(), w("Not carried"));
        assert.equal(await left.locator("button, .lib-switch").count(), 0);
        assert.deepEqual(await sideways(page), [], "sideways scroll at " + width);

        await main.locator(".lib-switch").click();
        await card.locator(".lib-projmain .note", { hasText: w("The repository's other worktrees get none of what this project has.") }).waitFor();
        assert.deepEqual(posts, [{ dir: DIR, on: true }]);
        await card.locator(".lib-projmain .lib-switch").click();
        await card.locator(".lib-projmain .note", { hasText: w("The repository's {n} other worktrees get what this project has.", { n: 2 }) }).waitFor();
        assert.deepEqual(posts, [{ dir: DIR, on: true }, { dir: DIR, on: false }]);

        // a project with no worktree has no switch for them
        const lone = page.locator("#view-library .lib-project").filter({ has: page.locator(".lib-projhead", { hasText: "lone" }) }).first();
        await press(page, lone.locator(".lib-projhead"));
        await page.waitForTimeout(200);
        assert.equal(await lone.locator(".lib-projmain").count(), 0);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
