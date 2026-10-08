// Run with Node's test runner and Playwright on the module path; see README.md.
// char1eslu (#1217): skills from majiayu000/claude-arsenal,
// anthropics/claude-plugins-community and typesafe-ai/skills were on no
// agent, and magpie sent the skills a check found beside them with
// "agents": null. Drawing one threw ("null is not an object (evaluating
// 'id of n.agents')"): on load the page said so in red and stayed empty,
// and after "Check for updates" (技能库 → 检查更新) the page went blank
// without a word. A find without its agents is drawn as on no agent; a
// reply the page can't draw after a check says why. In English and
// Chinese, wide and narrow, in Chromium and WebKit. No backend: the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/char1eslu";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills` });
const gh = (repo, p) => `https://github.com/${repo}/tree/HEAD/${p}`;
// the reporter's skills on no agent, beside ones that are on some
const sk = (name, repo, p, agents) => ({ name, description: name + " skill", kind: "github", source: gh(repo, p), repo, agents, check: { name, status: "current" } });
const skills = [
  sk("codex-fluent", "majiayu000/claude-arsenal", "skills/codex-fluent", []),
  sk("codex-retrospective", "majiayu000/claude-arsenal", "skills/codex-retrospective", []),
  sk("skill-usage-stats", "majiayu000/claude-arsenal", "skills/skill-usage-stats", []),
  sk("eli5", "anthropics/claude-plugins-community", "plugins/eli5/skills/eli5", []),
  sk("typesafe-ai", "typesafe-ai/skills", "skills/typesafe-ai", []),
  sk("pdf", "anthropics/skills", "skills/pdf", ["claude", "codex"]),
];
// what a check found beside them, as magpie before the fix sent it; the
// reporter didn't name the skill, so this one stands in for it
const REPO = "majiayu000/claude-arsenal";
const found = { id: gh(REPO, "skills/codex-handoff"), name: "codex-handoff", description: "Hands off", repo: REPO, path: "skills/codex-handoff", from: gh(REPO, "skills"), agents: null };
const lib = (st) => ({
  dir: `${HOME}/.config/magpie/library`, backups: `${HOME}/.config/magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex")],
  instructions: { agents: [] }, servers: [], skills, foundServers: [], projects: [], foundSkills: [], skillGroups: [],
  newSkills: st.checked || st.foundAtLoad ? [found] : [],
});

function server(lang, st) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib(st) });
    if (url.pathname === "/api/library/skills/check") {
      st.checked = true;
      const v = lib(st);
      if (st.undrawable) delete v.servers; // a reply the page can't draw
      return route.fulfill({ json: v });
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
  en: { check: "Check for updates", said: "Every skill is up to date · 1 more skill in their repositories", addTip: "Adds it to the library" },
  zh: { check: "检查更新", said: "所有技能都是最新的 · 同一仓库里还有 1 个技能", addTip: "加入资源库" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a found skill with agents null doesn't blank the Library page", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());

    const open = async (lang, width, st, errors) => {
      const ctx = await browser.newContext({ viewport: { width, height: 1100 } });
      await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, st));
      await page.goto("http://magpie.test/");
      await page.locator('button[data-view="library"]').click();
      return { ctx, page, v: page.locator("#view-library") };
    };
    const drawn = async (page, v, w, width) => {
      const row = v.locator(".lib-newskill");
      assert.equal(await row.count(), 1, "the find is listed");
      assert.equal((await row.locator(".name").textContent()).trim(), "codex-handoff");
      assert.equal(await row.locator(".lib-have > *").count(), 0, "on no agent");
      assert.equal(await row.locator("button.action").getAttribute("title"), w.addTip);
      assert(await v.locator(".lib-row", { hasText: "codex-fluent" }).count() > 0, "the library's own skills are drawn too");
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, `no sideways scroll at ${width}px`);
    };

    for (const width of [860, 440]) for (const lang of ["en", "zh"]) {
      const w = words[lang];

      await t.test(`${lang} at ${width}px: Check for updates`, async () => {
        const errors = [], st = {};
        const { ctx, page, v } = await open(lang, width, st, errors);
        await v.locator(".lib-body:not(.lib-skel) .lib-row").first().waitFor();
        await v.locator(".row-head button.lib-updall", { hasText: w.check }).click();
        await page.waitForFunction(() => !document.querySelector("#view-library .lib-updall.busy, #view-library .lib-updall[disabled]"));
        await page.waitForTimeout(200);
        assert.deepEqual(errors, []);
        assert.equal((await page.locator("#status").textContent()).trim(), w.said);
        await drawn(page, v, w, width);
        await ctx.close();
      });

      await t.test(`${lang} at ${width}px: loaded after a check`, async () => {
        const errors = [], st = { foundAtLoad: true };
        const { ctx, page, v } = await open(lang, width, st, errors);
        await v.locator(".lib-body:not(.lib-skel) .lib-row").first().waitFor();
        await page.waitForTimeout(200);
        assert.deepEqual(errors, []);
        assert.notEqual(await page.locator("#status").getAttribute("class"), "status err", "no error said");
        await drawn(page, v, w, width);
        await ctx.close();
      });

      await t.test(`${lang} at ${width}px: a check's reply the page can't draw says so`, async () => {
        const errors = [], st = { undrawable: true };
        const { ctx, page, v } = await open(lang, width, st, errors);
        await v.locator(".lib-body:not(.lib-skel) .lib-row").first().waitFor();
        await v.locator(".row-head button.lib-updall", { hasText: w.check }).click();
        await page.waitForTimeout(500);
        assert.deepEqual(errors, [], "no uncaught error");
        const s = page.locator("#status");
        assert.equal(await s.getAttribute("class"), "status err");
        assert.notEqual((await s.textContent()).trim(), w.said, "not said to have gone well");
        await ctx.close();
      });
    }
  });
}
