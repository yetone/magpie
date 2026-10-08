// Run with Node's test runner and Playwright on the module path; see README.md.
// Skills installed from GitHub are above, this computer's below, and one
// author's repositories above are one group (#1031, mintonight: two skills
// of oil-oil, a repository each, installed from the Library's search, were
// a group each among the local ones, and a group of local skills she made
// sat above them all). A skill only traced to a repository and a group of
// the user's with a local skill in it are with On this computer; a group
// of the user's of skills from GitHub alone is above. An author's many
// skills are in parts by repository. Chromium and WebKit, en, zh, ja and
// de, at 1100px and 440px; the API is faked here.
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

const gh = (name, repo) => ({ name, kind: "github", source: `https://github.com/${repo}/tree/main/${name}`, repo, description: "d", agents: ["claude"] });
const SKILLS = [
  gh("oil-cover", "oil-oil/oil-cover"),
  gh("oil-ppt", "oil-oil/oil-ppt"),
  gh("tdd", "mattpocock/skills"),
  gh("grill-me", "mattpocock/skills"),
  { name: "humanizer-zh", kind: "folder", source: `${HOME}/.agents/skills/humanizer-zh`, repo: "op7418/Humanizer-zh", description: "d", agents: ["claude"] },
  { name: "lark-doc", kind: "", source: "", description: "d", agents: ["claude"] },
  { name: "lark-im", kind: "", source: "", description: "d", agents: [] },
  { name: "notes", kind: "", source: "", description: "d", agents: ["claude"] },
];
const GROUPS = [{ name: "Lark", skills: ["lark-doc", "lark-im"] }, { name: "Daily", skills: ["grill-me"] }];
// one author with many skills, for its parts
const MANY = Array.from({ length: 10 }, (_, i) => gh("acme-" + i, "acme/" + (i < 6 ? "one" : "two")));
const L = {
  en: { here: "On this computer", gh: "Open oil-oil on GitHub" },
  zh: { here: "本机", gh: "在 GitHub 上打开 oil-oil" },
  ja: { here: "このコンピュータ", gh: "GitHub で oil-oil を開く" },
  de: { here: "Auf diesem Computer", gh: "oil-oil auf GitHub öffnen" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": GitHub's skills above, this computer's below, an author's one group", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "ja", "de"]) for (const width of [1100, 440]) {
      await t.test(lang + " " + width, async () => {
        const w = L[lang];
        const state = {
          lib: {
            dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
            agents: [agent("claude", "Claude Code", "claudecode-color"), agent("codex", "Codex", "openai")],
            instructions: { agents: [], sets: [] }, servers: [], foundServers: [], projects: [], foundSkills: [], problems: [],
            skills: structuredClone([...SKILLS, ...MANY]), skillGroups: structuredClone(GROUPS),
          },
        };
        const ctx = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
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
        assert.deepEqual(heads, [
          ["Daily", "1"], ["acme", "10"], ["mattpocock/skills", "1"], ["oil-oil", "2"],
          ["Lark", "2"], ["op7418/Humanizer-zh", "1"], [w.here, "1"],
        ]);
        const card = (name) => v.locator(".lib-group").filter({ has: page.locator(".lib-grouphead .name", { hasText: new RegExp("^" + name + "$") }) });
        const names = async (name) => (await card(name).locator(".lib-row:not(.lib-grouphead):not(.lib-subhead) .name").allTextContents()).map((n) => n.trim());
        assert.deepEqual((await names("oil-oil")).filter((n) => n.startsWith("oil-")), ["oil-cover", "oil-ppt"]);
        // an author's many skills, a part for each repository
        const subs = await card("acme").locator(".lib-subhead .name").allTextContents();
        assert.deepEqual(subs.map((s) => s.trim()), ["acme/one", "acme/two"]);
        const o = card("oil-oil").locator(".lib-grouphead .lib-icon");
        assert.equal(await o.getAttribute("title"), w.gh);
        // an author's group opens the author on GitHub
        await o.evaluate((b) => b.click());
        assert.deepEqual(await page.evaluate(() => window.__opened), ["https://github.com/oil-oil"]);
        // nothing wider than the window
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "no sideways scroll");
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
