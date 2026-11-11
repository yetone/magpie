// Run with Node's test runner and Playwright on the module path; see README.md.
// What memory suggests, over the Library's Memory list (memory.Suggest):
// a memory learned in two projects, named by the projects, made to hold
// everywhere; a procedure searched for often made a skill, which the
// library then has; and one turned down. Shown only on the In use list
// unsearched. Every language, wide and narrow, Chromium and WebKit; no
// backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/mem";
const at = (d) => `2026-11-0${d}T10:00:00Z`;
function library() {
  return {
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
    agents: [{ id: "codex", name: "Codex", icon: "", instructions: `${HOME}/.codex/AGENTS.md`, skills: `${HOME}/.codex/skills` }],
    instructions: { shared: "", sets: [{ id: "default", name: "", active: true, text: "" }], agents: ["codex"] },
    foundServers: [], projects: [], foundSkills: [], skills: [], servers: [],
  };
}
function memories() {
  return {
    memories: [
      { id: "m1", text: "Use pnpm, not npm", kind: "preference", scope: "project:a", origin: "inferred", status: "active", created: at(1), updated: at(2) },
      { id: "m2", text: "use pnpm, not npm", kind: "preference", scope: "project:b", origin: "inferred", status: "active", created: at(1), updated: at(1) },
      { id: "m3", text: "To release: tag, wait for the assets, then post", kind: "procedure", scope: "global", origin: "inferred", status: "active", uses: 7, created: at(1), updated: at(3) },
      { id: "m4", text: "Dates are ISO 8601", kind: "rule", scope: "project:a", origin: "inferred", status: "active", created: at(1), updated: at(1) },
      { id: "m5", text: "Dates are ISO 8601", kind: "rule", scope: "project:b", origin: "inferred", status: "active", created: at(1), updated: at(1) },
    ],
    projects: { a: { key: "a", name: "magpie", roots: [`${HOME}/magpie`] }, b: { key: "b", name: "site", roots: [`${HOME}/site`] } },
    digest: true, agents: ["codex"],
    suggestions: [
      { key: "global:m1,m2", to: "global", ids: ["m1", "m2"], text: "Use pnpm, not npm", projects: ["a", "b"] },
      { key: "global:m4,m5", to: "global", ids: ["m4", "m5"], text: "Dates are ISO 8601", projects: ["a", "b"] },
      { key: "skill:m3", to: "skill", ids: ["m3"], text: "To release: tag, wait for the assets, then post", uses: 7 },
    ],
  };
}

function server(lang, state, lib, sent) {
  return async (route) => {
    const url = new URL(route.request().url());
    const post = route.request().method() === "POST";
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib });
    if (url.pathname.startsWith("/api/memory/distill") || url.pathname.startsWith("/api/memory/dream")) return route.fulfill({ json: { on: false, models: [], ranges: [7], efforts: [] } });
    if (url.pathname === "/api/memory/semantic") return route.fulfill({ json: { on: false } });
    if (url.pathname === "/api/memory/rank") return route.fulfill({ json: { ids: null } });
    if (url.pathname.startsWith("/api/memory")) {
      if (post) {
        const body = route.request().postDataJSON();
        sent.push({ path: url.pathname, body });
        const drop = (key) => { state.suggestions = state.suggestions.filter((s) => s.key !== key); };
        if (url.pathname === "/api/memory/promote") {
          const kept = state.memories.find((m) => m.id === body.ids[0]);
          kept.scope = "global";
          for (const id of body.ids.slice(1)) state.memories.find((m) => m.id === id).status = "superseded";
          drop("global:" + body.ids.join(","));
        } else if (url.pathname === "/api/memory/skill") {
          Object.assign(state.memories.find((m) => m.id === body.id), { status: "archived", skill: "to-release-tag-wait-for" });
          lib.skills.push({ name: "to-release-tag-wait-for", agents: ["codex"] });
          drop("skill:" + body.id);
        } else if (url.pathname === "/api/memory/decline") drop(body.key);
      }
      return route.fulfill({ json: state });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const view = "#view-library";
const words = {
  en: { learned: "Learned in", everywhere: "Use everywhere", skill: "Make a skill", searched: /Searched 7 times/, later: "Not now" },
  zh: { learned: "在 magpie, site 中学到", everywhere: "在所有项目中使用", skill: "做成 Skill", searched: /被搜索 7 次/, later: "暂不" },
  "zh-TW": { learned: "magpie, site", everywhere: null, skill: null, searched: /7/, later: "暫不" },
  ja: { learned: "magpie, site", everywhere: null, skill: null, searched: /7/, later: "後で" },
  de: { learned: "magpie, site", everywhere: null, skill: null, searched: /7/, later: null },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": memory's suggestions are taken or turned down", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of Object.keys(words)) for (const width of [980, 420]) {
      await t.test(`${lang} ${width}`, async () => {
        const w = words[lang];
        const ctx = await browser.newContext({ viewport: { width, height: 860 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "memory"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        const sent = [];
        const state = memories();
        const lib = library();
        let libGets = 0;
        await page.route("**/*", server(lang, state, lib, sent));
        page.on("request", (r) => { if (new URL(r.url()).pathname === "/api/library") libGets++; });
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const sugg = page.locator(`${view} .lib-memsugg [data-sugg]`);
        await sugg.first().waitFor();
        assert.equal(await sugg.count(), 3);
        // above the list, named by the projects it was learned in, in the reader's language
        const pnpm = page.locator(`${view} [data-sugg="global:m1,m2"]`);
        assert.ok(lang === "en" ? (await pnpm.locator(".sub").textContent()).includes("Learned in magpie, site") : (await pnpm.locator(".sub").textContent()).includes(w.learned));
        if (lang !== "en") assert.doesNotMatch(await pnpm.locator(".sub").textContent(), /Learned in/);
        const rel = page.locator(`${view} [data-sugg="skill:m3"]`);
        assert.match(await rel.locator(".sub").textContent(), w.searched);
        if (w.everywhere) assert.equal((await pnpm.locator("button").first().textContent()).trim(), w.everywhere);
        if (w.skill) assert.equal((await rel.locator("button").first().textContent()).trim(), w.skill);
        if (w.later) assert.equal((await pnpm.locator("button").last().textContent()).trim(), w.later);
        const listBox = await page.locator(`${view} .lib-mems`).boundingBox();
        assert.ok((await page.locator(`${view} .lib-memsugg`).boundingBox()).y < listBox.y, "suggestions not over the list");
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), "the page scrolls sideways");
        const body = await page.locator(`${view} .lib-body`).boundingBox();
        for (const b of await page.locator(`${view} .lib-memsugg button`).all()) {
          const r = await b.boundingBox();
          assert.ok(r.x + r.width <= body.x + body.width + 1, "a suggestion's button is cut off");
        }

        // taken: held everywhere, the other superseded, the suggestion gone
        await pnpm.locator("button").first().click();
        await pnpm.waitFor({ state: "detached" });
        assert.deepEqual(sent.findLast((x) => x.path === "/api/memory/promote").body, { ids: ["m1", "m2"] });
        assert.equal(await page.locator(`${view} .lib-mems .lib-mem`, { hasText: /use pnpm, not npm/ }).count(), 0, "the superseded one still listed");

        // made a skill: the library is read again, and it has it
        const before = libGets;
        await rel.locator("button").first().click();
        await rel.waitFor({ state: "detached" });
        assert.deepEqual(sent.findLast((x) => x.path === "/api/memory/skill").body, { id: "m3" });
        for (let i = 0; i < 50 && libGets <= before; i++) await page.waitForTimeout(50);
        assert.ok(libGets > before, "the library wasn't read again");

        // turned down
        const iso = page.locator(`${view} [data-sugg="global:m4,m5"]`);
        await iso.locator("button").last().click();
        await page.locator(`${view} .lib-memsugg`).waitFor({ state: "detached" });
        assert.deepEqual(sent.findLast((x) => x.path === "/api/memory/decline").body, { key: "global:m4,m5" });
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });

  // not over a search, another list, or a project's memories
  test(engine + ": suggestions show on the In use list only", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const ctx = await browser.newContext({ viewport: { width: 980, height: 860 }, reducedMotion: "reduce" });
    await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "memory"); } catch {} });
    const page = await ctx.newPage();
    page.setDefaultTimeout(5000);
    await page.route("**/*", server("en", memories(), library(), []));
    await page.goto("http://magpie.test/");
    await page.locator('button[data-view="library"]').click();
    await page.locator(`${view} .lib-memsugg`).waitFor();
    await page.locator(`${view} .lib-memstatus .opt`, { hasText: "Archived" }).click();
    await page.locator(`${view} .lib-memsugg`).waitFor({ state: "detached" });
    await page.locator(`${view} .lib-memstatus .opt`).first().click();
    await page.locator(`${view} .lib-memsugg`).waitFor();
    await page.locator(`${view} .lib-memq`).first().fill("pnpm");
    await page.locator(`${view} .lib-memsugg`).waitFor({ state: "detached" });
    await ctx.close();
  });
}
