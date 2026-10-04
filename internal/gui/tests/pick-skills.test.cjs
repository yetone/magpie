// Run with Node's test runner and Playwright on the module path; see README.md.
// Many of the library's skills on or off at once (#791, mintonight: 能不能
// 支持多选分组，或者支持多选来开启或关闭 … lark-* 我希望把他们归到一起统一来
// 管理). Skills whose names start alike are a part of their list with a
// heading, and that heading's chips give an agent all of them or take them
// away; Select puts a box on every row and heading, and a bar's chips turn
// the ones picked on or off; the bar's box picks every one the filter
// shows. English and Chinese; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/me";
const agent = (id, name, icon) => ({ id, name, icon, skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const local = (name, agents) => ({ name, kind: "folder", source: `${HOME}/.agents/skills/${name}`, description: "d", agents });

function server(lang, state, posts) {
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(state.lib);
    if (url.pathname === "/api/library/skills/agents-some") {
      const b = req.postDataJSON();
      posts.push(b);
      for (const s of state.lib.skills) {
        if (!b.names.includes(s.name)) continue;
        const kept = s.agents.filter((a) => !b.agents.includes(a));
        s.agents = (b.on ? [...kept, ...b.agents] : kept).sort();
      }
      return json({ ...state.lib, result: { changed: b.agents } });
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { part: "4 skills are on for Codex", one: "1 selected", two: "2 selected", six: "6 selected", off: "2 skills are off for Claude Code", match: "All 4 that match", select: "Select", done: "Done" },
  zh: { part: "已为 Codex 启用 4 个技能", one: "已选 1 个", two: "已选 2 个", six: "已选 6 个", off: "已从 Claude Code 中关闭 2 个技能", match: "全部 4 个匹配项", select: "选择", done: "完成" },
};
const LARK = ["lark-apps", "lark-base", "lark-doc", "lark-drive"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": many skills on or off at once", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = L[lang];
        const state = {
          lib: {
            dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
            agents: [agent("claude", "Claude Code", "claudecode-color"), agent("codex", "Codex", "openai")],
            instructions: { agents: [], sets: [] }, servers: [], foundServers: [], projects: [], foundSkills: [], problems: [],
            skills: [...LARK.map((n) => local(n, ["claude"])), ...["docx", "notes", "pdf", "web-search", "xlsx"].map((n) => local(n, ["claude"]))],
          },
        };
        const posts = [];
        const ctx = await browser.newContext({ viewport: { width: 1100, height: 760 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, state, posts));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        const part = v.locator(".lib-subhead").filter({ has: page.locator(".name", { hasText: /^lark$/ }) });
        await part.waitFor();
        const row = (name) => v.locator(".lib-skill").filter({ has: page.locator(".name", { hasText: new RegExp("^" + name + "$") }) });
        const agentsOf = (name) => state.lib.skills.find((s) => s.name === name).agents;

        // the lark part's chips: Codex is given all four, the rest are left alone
        const chip = part.locator('.lib-agents .lib-ag[data-agent="codex"]');
        assert.equal(await chip.getAttribute("aria-pressed"), "false");
        await chip.click();
        await page.locator("#status").filter({ hasText: w.part }).waitFor();
        assert.deepEqual(posts.shift(), { names: LARK, agents: ["codex"], on: true });
        assert.deepEqual(agentsOf("pdf"), ["claude"]);
        await page.waitForFunction(() => [...document.querySelectorAll("#view-library .lib-subhead")].find((h) => h.querySelector(".name")?.textContent === "lark")?.querySelector('.lib-ag[data-agent="codex"]')?.getAttribute("aria-pressed") === "true");
        assert.equal(await part.evaluate((h) => h.classList.contains("open")), true, "the click folded the part");

        // Select: a box on each row; a row's click picks it rather than opening it
        assert.equal(await v.locator(".lib-pickbox").count(), 0);
        await v.locator(".lib-select").click();
        assert.equal((await v.locator(".lib-select").textContent()).trim(), w.done);
        await row("pdf").locator(".lib-pickbox").waitFor();
        const top = await v.evaluate((x) => x.scrollTop);
        await row("pdf").locator(".name").click();
        assert.equal(await row("pdf").locator(".lib-pickbox").isChecked(), true);
        assert.equal(await page.locator(".lib-editor").count(), 0, "the click opened the skill");
        await v.locator(".lib-pickbar .lib-pickn").filter({ hasText: w.one }).waitFor();
        await row("docx").locator(".lib-pickbox").click();
        await v.locator(".lib-pickbar .lib-pickn").filter({ hasText: w.two }).waitFor();
        assert.equal(await v.evaluate((x) => x.scrollTop), top, "a pick scrolled the page");

        // the bar's chips: Claude Code's are taken from the two picked
        await v.locator('.lib-pickbar .lib-ag[data-agent="claude"]').click();
        await page.locator("#status").filter({ hasText: w.off }).waitFor();
        assert.deepEqual(posts.shift(), { names: ["docx", "pdf"], agents: ["claude"], on: false });
        assert.deepEqual(agentsOf("notes"), ["claude"]);
        // still picking, the two still picked
        await page.waitForFunction(() => document.querySelector('#view-library .lib-pickbar .lib-ag[data-agent="claude"]')?.getAttribute("aria-pressed") === "false");
        assert.equal(await row("docx").locator(".lib-pickbox").isChecked(), true);

        // a heading's box picks its part; half ticked with some
        const pbox = part.locator(".lib-pickbox");
        await row("lark-doc").locator(".lib-pickbox").click();
        assert.equal(await pbox.evaluate((b) => b.indeterminate), true);
        await pbox.click();
        assert.equal(await pbox.isChecked(), true);
        for (const n of LARK) assert.equal(await row(n).locator(".lib-pickbox").isChecked(), true, n);
        await v.locator(".lib-pickbar .lib-pickn").filter({ hasText: w.six }).waitFor();
        await v.locator(".lib-pickclear").click();
        await v.locator(".lib-pickbar .lib-pickn.none").waitFor();

        // the bar's box picks every one the filter shows
        await v.locator(".lib-skillq").fill("lark");
        await v.locator(".lib-pickall").filter({ hasText: w.match }).waitFor();
        await v.locator(".lib-pickall input").click();
        await v.locator(".lib-pickbar .lib-pickn").filter({ hasText: lang === "en" ? "4 selected" : "已选 4 个" }).waitFor();
        await v.locator('.lib-pickbar .lib-ag[data-agent="claude"]').click();
        await page.waitForFunction(() => document.querySelector('#view-library .lib-pickbar .lib-ag[data-agent="claude"]')?.getAttribute("aria-pressed") === "false");
        assert.deepEqual(posts.shift(), { names: LARK, agents: ["claude"], on: false });

        // Done takes the boxes away; a row opens its skill again
        await v.locator(".lib-pickdone").click();
        assert.equal(await v.locator(".lib-pickbox").count(), 0);
        assert.equal(await v.locator(".lib-pickbar").count(), 0);
        assert.equal((await v.locator(".lib-select").textContent()).trim(), w.select);

        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
