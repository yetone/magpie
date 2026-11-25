// Run with Node's test runner and Playwright on the module path; see README.md.
// MetalHqa on X: Library › Skills listed every agent's skills in one list,
// with no way to see which skills one agent has. The heading's agent pick
// lists All agents and each agent that has some, with how many; picking
// one shows the library's skills it has (ticked, read from
// ~/.agents/skills itself) and those only in its own folder, and All
// agents brings the rest back. A skill two agents have shows under both.
// Nothing is given or taken on the way. Every language, both engines, at
// 1100 and 420. No backend: the API is faked, and any write fails the test.
// ARTIFACT_DIR keeps screenshots of the menu and of one agent's list, in
// THEME (light by default).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const shots = process.env.ARTIFACT_DIR, theme = process.env.THEME || "light";
const assets = path.resolve(__dirname, process.env.MAGPIE_ASSETS || "../assets");
const HOME = "/Users/chuan";
const agent = (id, name, icon) => ({ id, name, icon, skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const skill = (name, agents, extra = {}) => ({ name, description: "Does " + name, kind: "", agents, ...extra });
const lib = () => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code", "claudecode-color"), agent("codex", "Codex", "codex-color"), agent("gemini", "Gemini CLI", "geminicli-color"), agent("qwen", "Qwen Code", "qwen-color")],
  instructions: { agents: [] }, foundServers: [], projects: [], problems: [], servers: [],
  skills: [skill("both-skill", ["claude", "codex"]), ...[1, 2, 3, 4].map((n) => skill("claude-" + n, ["claude"])),
    ...[1, 2, 3].map((n) => skill("codex-" + n, ["codex"])), skill("shared-skill", [], { always: ["codex"] }), skill("nobody-skill", [])],
  foundSkills: [{ name: "claude-own", description: "Claude's own", agents: ["claude"] }, { name: "codex-own", description: "Codex's own", agents: ["codex"] },
    { name: "gemini-own", description: "Gemini's own", agents: ["gemini"] }],
});
const ALL = ["both-skill", "claude-1", "claude-2", "claude-3", "claude-4", "codex-1", "codex-2", "codex-3", "shared-skill", "nobody-skill"];

let I18N;
function tr(lang, key, vars = {}) {
  let s = key;
  if (lang !== "en") {
    assert.equal(typeof I18N[lang][key], "string", `${lang} has no ${JSON.stringify(key)}`);
    s = I18N[lang][key];
  }
  return s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m));
}

function server(lang, writes) {
  return async (route) => {
    const url = new URL(route.request().url());
    if (route.request().method() !== "GET" && url.pathname.startsWith("/api/library")) writes.push(url.pathname);
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme } });
    if (url.pathname === "/api/library") return json(lib());
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

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Skills shows one agent's skills when it is picked", async (t) => {
    I18N = strings().I18N;
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1100, 420]) {
      await t.test(`${lang} ${width}`, async () => {
        // tall enough that every row of the list is drawn (it draws those near the view)
        const ctx = await browser.newContext({ viewport: { width, height: 2600 }, reducedMotion: "reduce" });
        t.after(() => ctx.close());
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], writes = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, writes));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        const names = async (sel) => (await v.locator(sel + " > .who > .name").evaluateAll((ns) => ns.map((n) => n.firstChild.textContent.trim()))).sort();
        const rows = () => names(".lib-skill");
        const found = () => names(".lib-found");
        await v.locator(".lib-skill").first().waitFor();
        assert.deepEqual(await rows(), [...ALL].sort());

        const pick = v.locator(".lib-skillshead .lib-skillagent");
        assert.equal((await pick.textContent()).trim(), tr(lang, "All agents"));
        const box = await pick.boundingBox();
        assert.ok(box.x >= 0 && box.x + box.width <= width, `the pick is in the window: ${JSON.stringify(box)}`);

        // the menu: All agents, then each agent with some and how many;
        // Qwen Code has none and isn't offered
        const menu = page.locator(".lib-agentmenu");
        const open = async () => { await pick.click(); await menu.waitFor(); };
        await open();
        const items = await menu.locator(".pm-item").evaluateAll((bs) => bs.map((b) => [b.querySelector(".pm-name").textContent, b.querySelector(".pm-note").textContent]));
        assert.deepEqual(items, [[tr(lang, "All agents"), "13"], ["Claude Code", "6"], ["Codex", "6"], ["Gemini CLI", "1"]]);
        assert.equal(await menu.locator(".pm-item .ic").count(), 4, "each agent's icon, and All's room for one");
        const shot = async (what) => {
          if (!shots) return;
          const h = await v.locator(".lib-skillshead").boundingBox();
          await page.screenshot({ path: path.join(shots, `${engine}-${lang}-${width}-${theme}-${what}.png`), clip: { x: 0, y: Math.max(0, h.y - 80), width, height: 820 } });
        };
        await shot("menu");

        await menu.locator(".pm-item", { hasText: "Codex" }).click();
        await menu.waitFor({ state: "detached" });
        assert.equal((await pick.textContent()).trim(), "Codex");
        assert.match(await pick.getAttribute("class"), /\bset\b/);
        assert.deepEqual(await rows(), ["both-skill", "codex-1", "codex-2", "codex-3", "shared-skill"]);
        assert.deepEqual(await found(), ["codex-own"]);
        await shot("codex");

        // the filter narrows within the agent's
        const q = v.locator(".lib-skillq");
        await q.fill("codex-2");
        await v.locator(".lib-skill", { hasText: "codex-1" }).waitFor({ state: "detached" });
        assert.deepEqual(await rows(), ["codex-2"]);
        await q.fill("");
        await v.locator(".lib-skill", { hasText: "codex-1" }).waitFor();

        // a skill two agents have shows under each
        await open();
        await menu.locator(".pm-item", { hasText: "Claude Code" }).click();
        await v.locator(".lib-skill", { hasText: "codex-1" }).waitFor({ state: "detached" });
        assert.deepEqual(await rows(), ["both-skill", "claude-1", "claude-2", "claude-3", "claude-4"]);
        assert.deepEqual(await found(), ["claude-own"]);

        // one with none of the library's says so, and shows its own
        await open();
        await menu.locator(".pm-item", { hasText: "Gemini CLI" }).click();
        await v.locator(".lib-none").waitFor();
        assert.equal((await v.locator(".lib-none").textContent()).trim(), tr(lang, "{agent} has none of the library's skills.", { agent: "Gemini CLI" }));
        assert.deepEqual(await found(), ["gemini-own"]);

        await open();
        await menu.locator(".pm-item").first().click();
        await v.locator(".lib-skill", { hasText: "nobody-skill" }).waitFor();
        assert.deepEqual(await rows(), [...ALL].sort());
        assert.deepEqual(await found(), ["claude-own", "codex-own", "gemini-own"]);
        assert.equal((await pick.textContent()).trim(), tr(lang, "All agents"));

        assert.deepEqual(writes, [], "a pick wrote to the library");
        assert.deepEqual(errors, []);
      });
    }
  });
}
