// Run with Node's test runner and Playwright on the module path; see README.md.
// A group of the user's made of the skills picked (#791, mintonight: 能不能
// 在选择后自己创建分组呢): Select, pick some, Group… asks a name, and the
// group made is a card of its own, its skills out of theirs: with a skill
// of this computer's in it, it is below those from GitHub, above On this
// computer (#1031). It is renamed in place, and Ungroup lets it go. English and
// Chinese; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/me";
const agent = (id, name, icon) => ({ id, name, icon, skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const local = (name) => ({ name, kind: "folder", source: `${HOME}/.agents/skills/${name}`, description: "d", agents: ["claude"] });
const gh = (name) => ({ name, kind: "github", source: `https://github.com/acme/skills/tree/main/skills/${name}`, description: "d", agents: ["claude"] });

function server(lang, state, posts) {
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(state.lib);
    if (url.pathname === "/api/library/skills/group") {
      const b = req.postDataJSON();
      posts.push({ group: b });
      const gs = state.lib.skillGroups;
      let g = gs.find((x) => x.name === b.name);
      if (b.old && b.old !== b.name) { g = gs.find((x) => x.name === b.old); g.name = b.name; }
      if (!g) gs.push((g = { name: b.name, skills: [] }));
      for (const o of gs) if (o !== g) o.skills = o.skills.filter((n) => !b.names.includes(n));
      for (const n of b.names) if (!g.skills.includes(n)) g.skills.push(n);
      state.lib.skillGroups = gs.filter((x) => x.skills.length);
      return json({ ...state.lib, result: {} });
    }
    if (url.pathname === "/api/library/skills/ungroup") {
      const b = req.postDataJSON();
      posts.push({ ungroup: b });
      for (const g of state.lib.skillGroups) if (g.name === b.name) g.skills = b.names.length ? g.skills.filter((n) => !b.names.includes(n)) : [];
      state.lib.skillGroups = state.lib.skillGroups.filter((x) => x.skills.length);
      return json({ ...state.lib, result: {} });
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
  en: { group: "Group…", made: "2 skills are in Office", renamed: "Office is now Docs", gone: "Docs is no longer a group", local: "On this computer", two: "2 skills", ask: "Group 2 skills as", ungroup: "Ungroup", rename: "Rename" },
  zh: { group: "分组…", made: "2 个技能已在 Office 中", renamed: "Office 已改名为 Docs", gone: "Docs 已不再是分组", local: "本机", two: "2 个技能", ask: "把 2 个技能分组为", ungroup: "取消分组", rename: "重命名" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a group of the user's made of the skills picked", async (t) => {
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
            skills: [...["docx", "notes", "pdf"].map(local), ...["review", "triage"].map(gh)], skillGroups: [],
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
        const row = (name) => v.locator(".lib-skill").filter({ has: page.locator(".name", { hasText: new RegExp("^" + name + "$") }) });
        const cards = () => v.locator(".lib-group .lib-grouphead .who .name").allTextContents();
        await row("pdf").waitFor();
        assert.deepEqual(await cards(), ["acme/skills", w.local]);

        // pick two, one from each source, and group them as Office
        await v.locator(".lib-select").click();
        await row("pdf").locator(".lib-pickbox").click();
        await row("review").locator(".lib-pickbox").click();
        await v.locator(".lib-pickgroup").filter({ hasText: w.group }).click();
        await v.locator(".lib-pickbar .lib-pickn").filter({ hasText: w.ask }).waitFor();
        const name = v.locator(".lib-groupname");
        assert.equal(await v.locator(".lib-groupgo").isDisabled(), true, "Group was on with no name");
        await name.fill("Office");
        await name.press("Enter");
        await page.locator("#status").filter({ hasText: w.made }).waitFor();
        assert.deepEqual(posts.shift(), { group: { name: "Office", names: ["pdf", "review"] } });

        // its card is open, with its two, below GitHub's and above this
        // computer's (pdf is local); theirs are without them
        await page.waitForFunction(() => !!document.querySelector('#view-library .lib-group[data-group="my:Office"]'));
        assert.deepEqual(await cards(), ["acme/skills", "Office", w.local]);
        const office = v.locator('.lib-group[data-group="my:Office"]');
        assert.equal(await office.locator(".lib-grouphead .sub").textContent(), w.two);
        assert.deepEqual((await office.locator(".lib-skill .name").allTextContents()).sort(), ["pdf", "review"]);
        assert.equal(await v.locator('.lib-group[data-group="local"] .lib-skill').filter({ hasText: /^pdf/ }).count(), 0);
        assert.equal(await v.locator(".lib-pickbox").count(), 0, "still picking after the group was made");

        // renamed in place: its fold isn't touched by the click
        await office.locator(".lib-grouprename").filter({ hasText: w.rename }).click();
        const box = office.locator(".lib-grouprename-in");
        assert.equal(await box.inputValue(), "Office");
        await box.fill("Docs");
        await box.press("Enter");
        await page.locator("#status").filter({ hasText: w.renamed }).waitFor();
        assert.deepEqual(posts.shift(), { group: { old: "Office", name: "Docs", names: [] } });
        const docs = v.locator('.lib-group[data-group="my:Docs"]');
        await docs.locator(".lib-skill").first().waitFor();

        // Ungroup lets it go, and its skills are back in their sources'
        await docs.locator(".lib-ungroup").filter({ hasText: w.ungroup }).click();
        await page.locator("#status").filter({ hasText: w.gone }).waitFor();
        assert.deepEqual(posts.shift(), { ungroup: { name: "Docs", names: [] } });
        await page.waitForFunction(() => !document.querySelector('#view-library .lib-group[data-group^="my:"]'));
        assert.deepEqual(await cards(), ["acme/skills", w.local]);

        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
