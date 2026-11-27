// Run with Node's test runner and Playwright on the module path; see README.md.
// xuanbo1889245 on X: a subagent left to the agent runs at the agent's
// default effort and spends more tokens than it needs. A subagent's
// effort is an app menu (never a <select>) under its model: the agent's
// pick at first, then low to max. Set, its hint names the agents picked
// that run it at their own pick (only Claude Code and Codex take one),
// and, when the model Codex runs it on lacks the level, that Codex won't
// start it so. Save posts it, the row says it, and it opens again with
// it. At 420 wide in en, zh, zh-TW, ja and de: no sideways scroll.
// No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/chuan";
const agent = (id, name, more = {}) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json`, ...more });
const LONG = "审阅改动过的代码，只报告真正的 bug：逻辑错误、边界条件、并发问题和资源泄漏；不评论代码风格，也不提出与本次改动无关的重构建议。改完代码后使用。";
const sa = (name, description, prompt, skills, mcp, agents) => ({ name, description, prompt, skills, mcp, agents });
const WSL = "claude@wsl:Ubuntu";
const SUB_AGENTS = [
  { id: "claude", dir: `${HOME}/.claude/agents`, skills: true, mcp: true, effort: true },
  { id: "codex", dir: `${HOME}/.codex/agents`, model: true, effort: true },
  { id: "cursor", dir: `${HOME}/.cursor/agents` },
  { id: "droid", dir: `${HOME}/.factory/droids`, mcp: true },
  { id: "opencode", dir: `${HOME}/.config/opencode/agents` },
  { id: WSL, wsl: true, no: "magpie gives no subagents to an agent in WSL yet" },
];
const MODELS = [{ id: "openai/gpt-6-sol", name: "GPT-6 Sol", note: "OpenAI", provider: "openai", providerName: "OpenAI", efforts: ["low", "medium", "high"] }, { id: "group/review", name: "review", group: true }];
const lib = (subagents, held = "") => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex"), agent("cursor", "Cursor"), agent("droid", "Droid"), agent("opencode", "OpenCode"), agent(WSL, "Claude Code (Ubuntu)")],
  instructions: { agents: [] }, servers: [{ name: "github", transport: "stdio", command: "npx", agents: ["claude"] }],
  skills: [{ name: "tdd", agents: ["claude"] }], foundServers: [], projects: [], foundSkills: [], problems: [],
  hooks: [], hookAgents: [], subagents, subagentAgents: SUB_AGENTS,
  subagentModels: MODELS, ...(held ? { codexSubagentModel: held } : {}),
});

let I18N;
// the words the page says in lang; a string missing from a language fails
function tr(lang, key, vars = {}) {
  let s = key;
  if (lang !== "en") {
    assert.equal(typeof I18N[lang][key], "string", `${lang} has no ${JSON.stringify(key)}`);
    s = I18N[lang][key];
  }
  return s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m));
}

const FILE = `${HOME}/.magpie/library/subagents/reviewer.md`, OWN = `${HOME}/code/agents/test-writer.md`;
const BAD = `magpie can't read ${FILE}: it has no front matter. The agents keep what it said before.`;

function server(lang, state, posts, reveals = []) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(lib(state.subagents, state.held));
    if (url.pathname === "/api/library/reveal") { reveals.push(req.postDataJSON().path); return json({}); }
    if (url.pathname.startsWith("/api/library/") && req.method() === "POST") {
      const body = req.postDataJSON(), what = url.pathname.replace("/api/library/", "");
      posts.push({ what, body });
      if (what === "subagents/agents") state.subagents = state.subagents.map((x) => (x.name === body.name ? { ...x, agents: body.agents.filter((a) => a !== WSL) } : x));
      if (what === "subagents/save") state.subagents = [...state.subagents.filter((x) => x.name !== body.old), { ...body.subagent, path: body.subagent.file || `${HOME}/.magpie/library/subagents/${body.subagent.name}.md` }];
      if (what === "subagents/remove") state.subagents = state.subagents.filter((x) => x.name !== body.name);
      return json({ ...lib(state.subagents, state.held), result: { changed: ["claude"] } });
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

// a click where the button is, as the reader's, and what was clicked stays
// where it was on the screen while the click redraws around it
const press = async (page, loc) => {
  await page.waitForFunction(() => !document.querySelector("#view-library").getAnimations({ subtree: true }).some((a) => a.effect?.getComputedTiming().endTime !== Infinity));
  const b = await loc.boundingBox();
  const sel = String(loc);
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
  await page.waitForTimeout(250);
  if (!(await loc.count())) return;
  const a = await loc.boundingBox();
  if (a && a.width) assert(Math.abs(a.y - b.y) <= 1, `${sel} moved on the screen from ${Math.round(b.y)} to ${Math.round(a.y)}`);
};
const settled = (page) => page.waitForFunction(() => !document.querySelector("#modal").getAnimations({ subtree: true }).length);
// nothing wider than the window: the page, the Library, its rows and dialog
const sideways = (page) => page.evaluate(() => {
  const over = [];
  const w = document.documentElement.clientWidth;
  if (document.documentElement.scrollWidth > w) over.push("page " + document.documentElement.scrollWidth);
  const v = document.querySelector("#view-library");
  if (v.scrollWidth > v.clientWidth + 1) over.push("library " + v.scrollWidth + ">" + v.clientWidth);
  for (const e of document.querySelectorAll("#view-library .lib-subagent, #view-library .lib-aside, #view-library .lib-tabs, #modal .lib-subagentedit *")) {
    const r = e.getBoundingClientRect();
    if (r.width && (r.right > w + 0.5 || r.left < -0.5)) over.push((e.className || e.tagName) + " " + Math.round(r.left) + "–" + Math.round(r.right));
  }
  return over;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a Library subagent's effort", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    I18N = strings().I18N;
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());

    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(lang, async () => {
        const w = (key, vars) => tr(lang, key, vars);
        const state = { subagents: [{ ...sa("reviewer", "Reviews a diff.", "You review code.", [], [], ["claude", "codex"]), path: FILE }] };
        const posts = [], errors = [];
        const ctx = await browser.newContext({ viewport: { width: 420, height: 1000 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "subagents"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
        await page.route("**/*", server(lang, state, posts));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        await v.locator(".lib-subagent").first().waitFor();
        const row = (name) => v.locator(".lib-subagent").filter({ has: page.locator(".name", { hasText: new RegExp("^" + name + "$") }) });
        // no effort set: the row says nothing of one
        assert.equal(await row("reviewer").locator(".lib-subagentuses").count(), 0);

        await press(page, row("reviewer"));
        const ed = page.locator("#modal .lib-subagentedit");
        await ed.waitFor();
        await settled(page);
        assert.equal(await page.locator("#modal select").count(), 0, "a native <select>");
        const pick = ed.locator(".lib-subagenteffort"), hint = ed.locator(".lib-subagenthint-effort");
        // under the model, the agent's pick at first, with nothing to say
        const fields = await ed.evaluate((e) => [...e.querySelectorAll(".lib-subagentmodel, .lib-subagenteffort, .lib-subagentskills")].map((x) => x.className.match(/lib-subagent\w+/)[0]));
        assert.deepEqual(fields, ["lib-subagentmodel", "lib-subagenteffort", "lib-subagentskills"]);
        assert.equal((await pick.textContent()).trim(), w("Picked by the agent"));
        assert.equal((await hint.textContent()).trim(), "");

        // the menu: the agent's pick, then the levels low to max
        await pick.click();
        const menu = page.locator(".proto-menu");
        await menu.waitFor();
        assert.equal((await menu.locator(".pm-head").textContent()).trim(), w("Effort"));
        assert.deepEqual((await menu.locator(".pm-name").allTextContents()).map((x) => x.trim()), [w("Picked by the agent"), ...["low", "medium", "high", "xhigh", "max"].map((x) => w(x))]);
        await menu.locator(".pm-item").nth(3).click();
        await menu.waitFor({ state: "detached" });
        assert.equal((await pick.textContent()).trim(), w("high"));
        // Claude Code and Codex take it: nothing to say
        assert.equal((await hint.textContent()).trim(), "");
        // given to Cursor too, which runs it at its own pick
        await ed.locator('.lib-ag[data-agent="cursor"]').click();
        assert.equal((await hint.textContent()).trim(), w("Not set for {agents}: theirs runs at the effort the agent picks", { agents: "Cursor" }));

        // on a model Codex runs at low to high, max: Codex won't start it
        await ed.locator(".lib-subagentmodel").click();
        const mpop = page.locator("#pop.model-picker:not([hidden])");
        await mpop.waitFor();
        await mpop.locator("#list li[data-i]", { hasText: "GPT-6 Sol" }).click();
        await mpop.waitFor({ state: "hidden" });
        await pick.click();
        await menu.waitFor();
        await menu.locator(".pm-item").nth(5).click();
        await menu.waitFor({ state: "detached" });
        assert.equal((await hint.textContent()).trim(), w("Not set for {agents}: theirs runs at the effort the agent picks", { agents: "Cursor" }) + " · " +
          w("Codex runs {model} at {levels} only, and won't start it at this one", { model: "GPT-6 Sol", levels: ["low", "medium", "high"].map((x) => w(x)).join(", ") }));
        assert.deepEqual(await sideways(page), [], "sideways scroll in the editor");
        // back to one it has
        await pick.click();
        await menu.waitFor();
        await menu.locator(".pm-item").nth(2).click();
        await menu.waitFor({ state: "detached" });
        assert.equal((await hint.textContent()).trim(), w("Not set for {agents}: theirs runs at the effort the agent picks", { agents: "Cursor" }));

        await ed.locator(".bar button.primary").click();
        await ed.waitFor({ state: "detached" });
        assert.deepEqual(posts.shift(), { what: "subagents/save", body: { old: "reviewer", subagent: { name: "reviewer", description: "Reviews a diff.", prompt: "You review code.", model: "openai/gpt-6-sol", effort: "medium", skills: [], mcp: [], agents: ["claude", "codex", "cursor"], file: "" } } });
        assert.equal((await row("reviewer").locator(".lib-subagentuses").textContent()).trim(), w("Model: {name}", { name: "GPT-6 Sol" }) + " · " + w("Effort: {level}", { level: w("medium") }));
        assert.deepEqual(await sideways(page), [], "sideways scroll in the list");

        // opened again with it; back to the agent's pick posts none
        await press(page, row("reviewer"));
        await ed.waitFor();
        await settled(page);
        assert.equal((await pick.textContent()).trim(), w("medium"));
        await pick.click();
        await menu.waitFor();
        await menu.locator(".pm-item").nth(0).click();
        await menu.waitFor({ state: "detached" });
        assert.equal((await hint.textContent()).trim(), "");
        await ed.locator(".bar button.primary").click();
        await ed.waitFor({ state: "detached" });
        assert.equal(posts.shift().body.subagent.effort, "");

        assert.deepEqual(posts, []);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
