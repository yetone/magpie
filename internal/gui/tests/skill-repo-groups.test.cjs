// Run with Node's test runner and Playwright on the module path; see README.md.
// A repository's skills are one group however each came in (White Immortal
// on Discord: the skills of github.com/mattpocock/skills together). One
// installed from GitHub, one the skills CLI installed and one linked from a
// clone, each with the repository magpie traced (repo), are one group, its
// name's case aside; one from a git host other than GitHub is a group
// opened at that host; the rest are On this computer. English and Chinese;
// the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/me";
const agent = (id, name, icon) => ({ id, name, icon, skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });

function server(lang, state) {
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
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

const SKILLS = [
  { name: "tdd", kind: "github", source: "https://github.com/mattpocock/skills/tree/main/skills/tdd", repo: "mattpocock/skills", description: "d", agents: ["claude"] },
  { name: "grill-me", kind: "folder", source: `${HOME}/.agents/skills/grill-me`, repo: "mattpocock/skills", description: "d", agents: ["claude"] },
  { name: "write-a-prd", kind: "folder", source: `${HOME}/code/skills/skills/write-a-prd`, repo: "MattPocock/skills", description: "d", agents: [] },
  { name: "review", kind: "", source: "", repo: "gitlab.com/me/review", description: "d", agents: ["claude"] },
  { name: "notes", kind: "", source: "", description: "d", agents: ["claude"] },
];
const L = { en: { here: "On this computer", open: "Open gitlab.com/me/review", gh: "Open mattpocock/skills on GitHub" },
  zh: { here: "本机", open: "打开 gitlab.com/me/review", gh: "在 GitHub 上打开 mattpocock/skills" } };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a repository's skills are one group however each came in", async (t) => {
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
            skills: structuredClone(SKILLS),
          },
        };
        const ctx = await browser.newContext({ viewport: { width: 1100, height: 760 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => {
          try { localStorage.setItem("magpie.libTab", "skills"); } catch {}
          window.__opened = [];
          window.open = (u) => { window.__opened.push(u); return null; };
        });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, state));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        await v.locator(".lib-grouphead").first().waitFor();
        const heads = await v.locator(".lib-grouphead").evaluateAll((hs) => hs.map((h) => [h.querySelector(".name").textContent, h.querySelector(".sub").textContent.replace(/\D+/g, "")]));
        // the first spelling met names the group; one only traced to a
        // repository is with this computer's, below those from GitHub (#1031)
        assert.deepEqual(heads, [["mattpocock/skills", "3"], ["gitlab.com/me/review", "1"], [w.here, "1"]]);
        const card = (name) => v.locator(".lib-group").filter({ has: page.locator(".lib-grouphead .name", { hasText: name }) });
        assert.equal(await card("mattpocock/skills").locator(".lib-grouphead .lib-icon").getAttribute("title"), w.gh);
        const gl = card("gitlab.com/me/review").locator(".lib-grouphead .lib-icon");
        assert.equal(await gl.getAttribute("title"), w.open);
        // the page in a browser opens it in a tab of its own
        await gl.click();
        assert.deepEqual(await page.evaluate(() => window.__opened), ["https://gitlab.com/me/review"]);
        // its three skills under it, none in a part of a folder
        const names = await card("mattpocock/skills").locator(".lib-row:not(.lib-grouphead) .name").allTextContents();
        assert.deepEqual(names.map((n) => n.trim()).filter((n) => SKILLS.some((s) => s.name === n)).sort(), ["grill-me", "tdd", "write-a-prd"]);
        assert.equal(await card("mattpocock/skills").locator(".lib-subhead").count(), 0);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
