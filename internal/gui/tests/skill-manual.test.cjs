// Run with Node's test runner and Playwright on the module path; see README.md.
// sunset3279 on Discord: Library › Skills shows which skills Claude Code
// runs only when the user types /name, as their SKILL.md's
// disable-model-invocation says (manual, from /api/library). A quiet tag on
// the name, with a tooltip naming the command, on a library skill and on
// one an agent has of its own; none on the others. Five languages, narrow
// and wide; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/me";
const agent = (id, name, icon) => ({ id, name, icon, skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const local = (name, manual) => ({ name, kind: "local", description: "d", agents: ["claude"], ...(manual ? { manual } : {}) });

function server(lang, state, posts) {
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (req.method() === "POST") posts.push(url.pathname);
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(state.lib);
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

const L = { en: "Manual only", zh: "仅手动", "zh-TW": "僅手動", ja: "手動のみ", de: "Nur manuell" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a skill only /name runs is marked", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) for (const width of [420, 1180]) {
      await t.test(lang + " " + width, async () => {
        const state = {
          lib: {
            dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
            agents: [agent("claude", "Claude Code", "claudecode-color"), agent("codex", "Codex", "openai")],
            instructions: { agents: [], sets: [] }, servers: [], foundServers: [], projects: [], problems: [],
            skills: [local("deploy", true), local("lint")],
            foundSkills: [
              { name: "release", description: "Cut a release", agents: ["claude"], manual: true },
              { name: "notes", description: "Notes", agents: ["claude"] },
            ],
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
        await page.locator("#view-library .lib-row.lib-skill").first().waitFor();
        await page.locator("#view-library .lib-row.lib-found").first().waitFor();
        const marked = (sel) => page.locator(sel).evaluateAll((rows) => rows.map((r) => {
          const m = r.querySelector(".name .lib-manual");
          return [r.querySelector(".name").firstChild.textContent, m ? m.textContent : null, m?.title || ""];
        }));
        const lib = await marked("#view-library .lib-row.lib-skill");
        const found = await marked("#view-library .lib-row.lib-found");
        const tag = (rows, name) => rows.find(([n]) => n === name);
        assert.equal(tag(lib, "deploy")[1], L[lang]);
        assert.match(tag(lib, "deploy")[2], /\/deploy\b/);
        assert.match(tag(lib, "deploy")[2], /disable-model-invocation/);
        assert.equal(tag(lib, "lint")[1], null);
        assert.equal(tag(found, "release")[1], L[lang]);
        assert.match(tag(found, "release")[2], /\/release\b/);
        assert.equal(tag(found, "notes")[1], null);
        if (lang !== "en") assert.doesNotMatch(tag(found, "release")[2], /runs it only/, "the tooltip is translated");
        // the tag stays inside its row, a narrow one too
        for (const m of await page.locator("#view-library .lib-manual").all()) {
          const b = await m.boundingBox();
          const r = await m.evaluate((e) => e.closest(".lib-row").getBoundingClientRect().toJSON());
          assert.ok(b && b.x >= r.x && b.x + b.width <= r.x + r.width + 0.5, "the tag is inside its row");
        }
        assert.deepEqual(posts, []);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "the page scrolls sideways");
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
