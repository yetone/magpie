// Run with Node's test runner and Playwright on the module path; see README.md.
// @jiahao_jayden on X: every skill's row had a chip for each agent magpie
// knew of, WorkBuddy among them though it wasn't installed (一排有点密恐了).
// The library lists an agent gone from this computer (gone: true) only
// while it still has something magpie gave it, so that can be taken out:
// its chip is on the rows of what it has, lit, and a click takes it away;
// it is on no other row, All leaves it out, and By agent lists it as Not
// installed with nothing to turn on. Chromium and WebKit, 1100px and
// 390px, in en, zh, zh-TW, ja and de. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/me";
const agent = (id, name, more = {}) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json`, ...more });
const local = (name, agents) => ({ name, description: name + " does a thing", kind: "local", agents });
// as Read lists them: one gone from here only while it has something
const lib = (skills) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex"),
    ...(skills.some((s) => s.agents.includes("workbuddy")) ? [agent("workbuddy", "WorkBuddy", { gone: true })] : [])],
  instructions: { agents: [] }, servers: [], skills, foundServers: [], projects: [], foundSkills: [], problems: [],
});

let I18N;
function tr(lang, key, vars = {}) {
  let s = key;
  if (lang !== "en") {
    assert.equal(typeof I18N[lang][key], "string", `${lang} has no ${JSON.stringify(key)}`);
    s = I18N[lang][key];
  }
  return s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m));
}

function server(lang, state, posts) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(lib(state.skills));
    if (url.pathname.startsWith("/api/library/") && req.method() === "POST" && !url.pathname.endsWith("/reveal")) {
      const body = req.postDataJSON(), what = url.pathname.replace("/api/library/", "");
      posts.push({ what, body });
      if (what === "skills/agents") {
        state.skills = state.skills.map((s) => (s.name === body.name ? { ...s, agents: [...body.agents].sort() } : s));
        return json({ ...lib(state.skills), result: { changed: ["claude", "workbuddy"] } });
      }
      return json(lib(state.skills));
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const sideways = (page) => page.evaluate(() => {
  const over = [];
  const w = document.documentElement.clientWidth;
  if (document.documentElement.scrollWidth > w) over.push("page " + document.documentElement.scrollWidth);
  for (const e of document.querySelectorAll("#view-library .lib-skill, #view-library .lib-agents, #modal .lib-editor, #modal .lib-byagent-row")) {
    const r = e.getBoundingClientRect();
    if (r.width && (r.right > w + 0.5 || r.left < -0.5)) over.push((e.className || e.tagName) + " " + Math.round(r.left) + "–" + Math.round(r.right));
  }
  return over;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an agent not installed is only on the rows of what it still has", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    I18N = strings().I18N;
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const width of [1100, 390]) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(`${lang} at ${width}px`, async () => {
        const w = (key, vars) => tr(lang, key, vars);
        const state = { skills: [local("pdf", ["claude", "workbuddy"]), local("csv", ["claude"]), local("notes", ["claude", "codex"])] };
        const posts = [], errors = [];
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
        await page.route("**/*", server(lang, state, posts));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        const row = (name) => v.locator(".lib-skill").filter({ has: page.locator(".name", { hasText: new RegExp("^" + name) }) });
        const chips = (name) => row(name).locator(":scope > .lib-agents > .lib-ag[data-agent]").evaluateAll((cs) => cs.map((c) => c.dataset.agent));
        await row("notes").waitFor();

        // WorkBuddy is on pdf's row alone, lit, saying it isn't installed
        assert.deepEqual(await chips("pdf"), ["claude", "codex", "workbuddy"]);
        assert.deepEqual(await chips("csv"), ["claude", "codex"], "a chip for an agent not installed on a row it has nothing on");
        assert.deepEqual(await chips("notes"), ["claude", "codex"]);
        const wb = row("pdf").locator('.lib-ag[data-agent="workbuddy"]');
        assert.match(await wb.getAttribute("class"), /\bgone\b/);
        assert.equal(await wb.getAttribute("aria-pressed"), "true");
        assert.equal(await wb.getAttribute("title"), w("{agent} isn't installed here — click to take it away", { agent: "WorkBuddy" }));
        // All counts the two installed, not WorkBuddy
        const all = row("pdf").locator(".lib-ag.all");
        assert.equal(await all.getAttribute("title"), w("Give it to all {n} agents that can take it", { n: 2 }));
        assert.deepEqual(await sideways(page), [], "sideways with the rows");

        // By agent: WorkBuddy as Not installed, nothing to turn on, its one
        // skill to take out
        const sheet = page.locator("#modal .lib-editor");
        await v.locator(".lib-skillshead .lib-barmore").click(); // By agent waits under ⋯ (library-bar-more)
        await page.locator(".row-menu .rm-item.lib-byagent").click();
        await sheet.waitFor();
        const ids = await sheet.locator(".lib-byagent-row").evaluateAll((rs) => rs.map((r) => r.dataset.agent));
        assert.deepEqual(ids, ["claude", "codex", "workbuddy"]);
        const wbRow = sheet.locator('.lib-byagent-row[data-agent="workbuddy"]');
        await wbRow.locator(".name", { hasText: w("Not installed here") }).waitFor();
        assert.equal(await wbRow.locator(".lib-agenton").isDisabled(), true, "Turn all on for an agent not installed");
        assert.equal(await wbRow.locator(".lib-agentoff").isDisabled(), false);
        assert.deepEqual(await sideways(page), [], "sideways with By agent");
        await sheet.locator(".bar button.primary").click();
        await sheet.waitFor({ state: "detached" });

        // its chip clicked: pdf is taken from it, the others keep theirs
        await row("pdf").locator(":scope > .lib-agents").hover();
        await wb.click();
        await page.waitForTimeout(300);
        assert.deepEqual(posts, [{ what: "skills/agents", body: { name: "pdf", agents: ["claude"] } }]);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
