// Run with Node's test runner and Playwright on the module path; see README.md.
// With several Hermes profiles a repository's heading in the Library's
// skills drew an icon for each of them, and the one chip a row made of them
// sat out of line with the others (Yanzhongixuy on X, 0.1.3926: "显示很长且
// 错位"). Both are one chip with a count that opens a menu of the profiles,
// in line with the hand of chips. A tick in the menu gives the repository's
// skills to that profile alone. 420px and 980px, English, Chinese,
// Traditional Chinese, Japanese and German; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/me";
const agent = (id, name, icon) => ({ id, name, icon, skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const gh = (repo, name, agents) => ({ name, kind: "github", source: `https://github.com/${repo}/tree/main/skills/${name}`, description: "d", agents });

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


const NAMES = ["coder", "work", "play", "ops", "docs", "a", "b", "c", "d", "e", "f", "g"];
const PROFILES = NAMES.map((n) => agent("hermes#" + n, "Hermes · " + n, "hermes"));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a repository's heading folds an agent's profiles into one chip", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const width of [420, 980]) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(lang + " " + width, async () => {
        const state = {
          lib: {
            dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
            agents: [agent("claude", "Claude Code", "claudecode-color"), agent("hermes", "Hermes Agent", "hermes"), ...PROFILES, agent("codex", "Codex", "openai")],
            instructions: { agents: [], sets: [] }, servers: [], foundServers: [], projects: [], foundSkills: [], problems: [],
            skills: [gh("acme/kit", "mango", ["claude", "hermes", "hermes#coder"]), gh("acme/kit", "cherry", ["claude", "hermes", "hermes#coder", "hermes#work"]), gh("zed/tools", "pear", [])],
          },
        };
        const posts = [];
        const ctx = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, state, posts));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const head = page.locator("#view-library .lib-grouphead").filter({ has: page.locator(".name", { hasText: /^acme\/kit$/ }) });
        await head.waitFor();
        if (!(await head.evaluate((h) => h.classList.contains("open")))) await head.locator(".name").click();
        const row = page.locator("#view-library .lib-skill > .lib-agents").first();
        await row.waitFor();
        const strip = head.locator(".lib-groupchips");

        // the heading: claude, one Hermes chip, codex; not a chip per profile
        assert.equal(await strip.locator('.lib-ag[data-agent^="hermes"]').count(), 0);
        assert.equal(await strip.locator(".lib-ag.profiles").count(), 1);
        assert.equal(await strip.locator(".lib-ag[data-agent]").count(), 2);
        const chip = strip.locator(".lib-ag.profiles");
        // hermes and #coder have both skills; #work has one
        assert.equal(await chip.locator(".cnt").textContent(), "2");
        assert(await chip.evaluate((c) => c.classList.contains("some")));

        // a row's chips are one hand: each the same step from the last
        for (const box of [strip, row]) {
          await page.mouse.move(2, 2);
          await page.waitForTimeout(400);
          const lefts = await box.locator(".lib-ag:not(.all)").evaluateAll((cs) => cs.map((c) => c.getBoundingClientRect().left));
          const steps = lefts.slice(1).map((x, i) => Math.round((x - lefts[i]) * 2) / 2);
          assert(steps.length >= 2);
          assert(steps.every((d) => Math.abs(d - steps[0]) <= 0.5), "chips are out of line: " + steps);
        }

        // a tick gives that profile every skill of the repository
        await chip.click();
        const menu = page.locator(".proto-menu");
        await menu.waitFor();
        await menu.locator(".pm-item", { hasText: "play" }).click();
        await page.waitForTimeout(150);
        assert.deepEqual(posts.shift(), { names: ["cherry", "mango"], agents: ["hermes#play"], on: true });
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
