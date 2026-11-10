// Run with Node's test runner and Playwright on the module path; see README.md.
// #2283 (chuanchinatest): Subagents, a Library tab beside Hooks. A subagent
// (a name, a description, a prompt, the skills and MCP servers it binds)
// is kept once and given to agents with the chips every other tab has.
// Its row says what it is for, wrapped, and what it binds. Asides say
// which agents it is given to can't bind its skills or servers (only
// Claude Code binds skills; Claude Code and Droid bind servers), that
// Cursor's cursor-agent command reads only a project's .cursor/agents,
// and which agents magpie can't write one for (an agent in WSL), whose
// chip is greyed, says why, and posts nothing. A row's chip posts
// subagents/agents. Add opens the editor: the prompt's Enter is a new
// line, not a save; an input method's Enter saves nothing; the skills and
// servers are app menus (never a <select>), with a hint naming the agents
// picked that won't bind them; Save posts subagents/save. A row opens it
// again, and Remove asks first, then posts subagents/remove.
// A subagent's model (luckymanjc on Discord) is an app menu of the agent's
// pick and the models and routing groups Codex lists; only Codex takes
// one, so the hint and an aside name the agents that run it on their own
// pick, and the hint says when Codex's subagent model on the Agents page
// takes its place.
// At 420 and 1100 wide in en, zh, zh-TW, ja and de: no sideways scroll,
// and what each click pressed stays where it was on the screen.
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
  { id: "claude", dir: `${HOME}/.claude/agents`, skills: true, mcp: true },
  { id: "codex", dir: `${HOME}/.codex/agents`, model: true },
  { id: "cursor", dir: `${HOME}/.cursor/agents` },
  { id: "droid", dir: `${HOME}/.factory/droids`, mcp: true },
  { id: "opencode", dir: `${HOME}/.config/opencode/agents` },
  { id: WSL, wsl: true, no: "magpie gives no subagents to an agent in WSL yet" },
];
const MODELS = [{ id: "openai/gpt-6-sol", name: "GPT-6 Sol", note: "OpenAI" }, { id: "group/review", name: "review", group: true }];
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

function server(lang, state, posts) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(lib(state.subagents, state.held));
    if (url.pathname.startsWith("/api/library/") && req.method() === "POST" && !url.pathname.endsWith("/reveal")) {
      const body = req.postDataJSON(), what = url.pathname.replace("/api/library/", "");
      posts.push({ what, body });
      if (what === "subagents/agents") state.subagents = state.subagents.map((x) => (x.name === body.name ? { ...x, agents: body.agents.filter((a) => a !== WSL) } : x));
      if (what === "subagents/save") state.subagents = [...state.subagents.filter((x) => x.name !== body.old), body.subagent];
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
  test(engine + ": the Library's Subagents tab", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    I18N = strings().I18N; // i18n.js's and the plugins'
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());

    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(lang, async () => {
        const w = (key, vars) => tr(lang, key, vars);
        const state = { subagents: [sa("reviewer", LONG, "You review code.\n\nReport bugs only.", ["tdd"], ["github"], ["claude", "codex"])] };
        const posts = [], errors = [];
        const open = async (width) => {
          const ctx = await browser.newContext({ viewport: { width, height: 1000 }, reducedMotion: "reduce" });
          await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "subagents"); } catch {} });
          const page = await ctx.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
          await page.route("**/*", server(lang, state, posts));
          await page.goto("http://magpie.test/");
          await page.locator('button[data-view="library"]').click();
          await page.locator("#view-library .lib-subagent").first().waitFor();
          return { ctx, page };
        };
        const wrapped = async (page, width) => {
          const r = page.locator("#view-library .lib-subagent").first();
          const fit = await r.locator(".lib-subagentdesc").evaluate((e) => ({ clipped: e.scrollWidth > e.clientWidth + 1 || e.scrollHeight > e.clientHeight + 1, lines: Math.round(e.getBoundingClientRect().height / parseFloat(getComputedStyle(e).lineHeight)) }));
          assert.equal(fit.clipped, false, `the description is cut off at ${width}`);
          assert(fit.lines >= 2, `the description isn't wrapped at ${width}: ${fit.lines} line`);
          const gap = await r.evaluate((x) => Math.abs(x.firstElementChild.getBoundingClientRect().top - x.querySelector(".name").getBoundingClientRect().top));
          assert(gap < 12, `the name is ${Math.round(gap)}px off its glyph at ${width}`);
          assert.deepEqual(await sideways(page), [], `sideways scroll with the list at ${width}`);
        };

        // 1100 wide in a window of its own: the list, and the editor
        {
          const { ctx, page } = await open(1100);
          await wrapped(page, 1100);
          await press(page, page.locator("#view-library .lib-subagent").first());
          await page.locator("#modal .lib-subagentedit").waitFor();
          await settled(page);
          assert.deepEqual(await sideways(page), [], "sideways scroll in the editor at 1100");
          await ctx.close();
        }

        const { ctx, page } = await open(420);
        const v = page.locator("#view-library");
        const row = (name) => v.locator(".lib-subagent").filter({ has: page.locator(".name", { hasText: new RegExp("^" + name + "$") }) });
        const agentsOf = (name) => state.subagents.find((s) => s.name === name)?.agents;

        // the tab, with its count, between Hooks and RTK
        const tabs = (await v.locator(".lib-tabs button").allTextContents()).map((x) => x.trim());
        assert.deepEqual(tabs.slice(4, 7), [w("Hooks"), w("Subagents") + " · 1", "RTK"]);
        // the row: what it is for, and what it binds
        assert.equal((await row("reviewer").locator(".lib-subagentdesc").textContent()).trim(), LONG);
        assert.equal((await row("reviewer").locator(".lib-subagentuses").textContent()).trim(), w("Skills: {names}", { names: "tdd" }) + " · " + w("MCP servers: {names}", { names: "github" }));
        await wrapped(page, 420);
        // what the agents it is given to do with what it binds: Codex binds
        // neither; Claude Code both, so it isn't named
        await v.locator(".lib-subagents-noskills").filter({ hasText: w("{agents} can't bind skills to a subagent: theirs gets its prompt, and every skill the agent has.", { agents: "Codex" }) }).waitFor();
        await v.locator(".lib-subagents-nomcp").filter({ hasText: w("{agents} can't bind MCP servers to a subagent: theirs gets its prompt, and every server the agent has.", { agents: "Codex" }) }).waitFor();
        await v.locator(".lib-subagents-no").filter({ hasText: w("magpie can't give subagents to {agents} yet: their chips are greyed, and say why.", { agents: "Claude Code (Ubuntu)" }) }).waitFor();
        assert.equal(await v.locator(".lib-subagents-cursor").count(), 0, "the Cursor aside with no subagent given to Cursor");

        // the agent in WSL: greyed, says why, and a click posts nothing
        const wsl = row("reviewer").locator(`.lib-ag[data-agent="${WSL}"]`);
        assert.equal(await wsl.getAttribute("aria-disabled"), "true");
        assert.equal(await wsl.getAttribute("title"), w("magpie gives subagents only to agents on this computer, not yet to {agent} in WSL", { agent: "Claude Code (Ubuntu)" }));
        await press(page, wsl);
        await page.waitForTimeout(150);
        assert.deepEqual(posts, [], "a greyed chip posted");

        // a row's chip gives it to Cursor, which then has its aside, and
        // is named among those that bind neither
        await press(page, row("reviewer").locator('.lib-ag[data-agent="cursor"]'));
        await page.waitForFunction(() => document.querySelector('#view-library .lib-subagent .lib-ag[data-agent="cursor"][aria-pressed="true"]'));
        await page.waitForTimeout(200);
        assert.deepEqual(posts.shift(), { what: "subagents/agents", body: { name: "reviewer", agents: ["claude", "codex", "cursor"] } });
        assert.deepEqual(agentsOf("reviewer"), ["claude", "codex", "cursor"]);
        await press(page, v.locator(".lib-tabs button").nth(4));
        await press(page, v.locator(".lib-tabs button").nth(5));
        await v.locator(".lib-subagents-cursor").filter({ hasText: w("The Cursor app has these subagents; its cursor-agent command reads only a project's own .cursor/agents.") }).waitFor();
        await v.locator(".lib-subagents-noskills").filter({ hasText: w("{agents} can't bind skills to a subagent: theirs gets its prompt, and every skill the agent has.", { agents: "Codex, Cursor" }) }).waitFor();

        // Add: the editor, every agent that can take it picked
        await press(page, v.locator(".after-list button"));
        const ed = page.locator("#modal .lib-subagentedit");
        await ed.waitFor();
        await settled(page);
        assert.equal(await page.locator("#modal select").count(), 0, "a native <select>");
        assert.equal((await ed.locator(".ehead b").textContent()).trim(), w("New subagent"));
        const edChip = (id) => ed.locator(`.lib-ag[data-agent="${id}"]`);
        for (const id of ["claude", "codex", "cursor", "droid", "opencode"]) assert.equal(await edChip(id).getAttribute("aria-pressed"), "true", id);
        assert.equal(await edChip(WSL).getAttribute("aria-pressed"), "false");
        await ed.locator("input").first().fill("test-writer");
        const desc = ed.locator(".lib-subagentdescin");
        assert.equal(await desc.getAttribute("placeholder"), w("e.g. Reviews a diff for bugs. Use after code changes."));
        await desc.fill("写测试");
        // Enter choosing an input method's word is the IME's: nothing saved
        await desc.evaluate((i) => i.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", isComposing: true, keyCode: 229, bubbles: true })));
        await page.waitForTimeout(150);
        assert.deepEqual(posts, [], "an IME's Enter saved the subagent");
        // the prompt's Enter is a new line, not a save
        const prompt = ed.locator(".lib-subagentprompt");
        await prompt.click();
        await page.keyboard.type("Write tests first.");
        await page.keyboard.press("Enter");
        await page.keyboard.type("Then run them.");
        await page.waitForTimeout(150);
        assert.deepEqual(posts, [], "the prompt's Enter saved");
        assert.equal(await prompt.inputValue(), "Write tests first.\nThen run them.");
        // the hints name the agents picked that won't bind them
        const hint = (kind) => ed.locator(".lib-subagenthint-" + kind);
        assert.equal((await hint("skills").textContent()).trim(), w("Not bound for {agents}: theirs uses every skill the agent has", { agents: "Codex, Cursor, Droid, OpenCode" }));
        assert.equal((await hint("mcp").textContent()).trim(), w("Not bound for {agents}: theirs uses every server the agent has", { agents: "Codex, Cursor, OpenCode" }));
        await edChip("opencode").click();
        assert.equal((await hint("mcp").textContent()).trim(), w("Not bound for {agents}: theirs uses every server the agent has", { agents: "Codex, Cursor" }));
        // the model from an app menu: the agent's pick at first, then a
        // routing group, which only Codex is given
        assert.equal((await ed.locator(".lib-subagentmodel").textContent()).trim(), w("Picked by the agent"));
        assert.equal((await hint("model").textContent()).trim(), w("Each agent runs it on the model it picks for a subagent"));
        await ed.locator(".lib-subagentmodel").click();
        const mmenu = page.locator(".proto-menu");
        await mmenu.waitFor();
        assert.equal((await mmenu.locator(".pm-head").textContent()).trim(), w("Model"));
        assert.deepEqual((await mmenu.locator(".pm-name").allTextContents()).map((x) => x.trim()), [w("Picked by the agent"), "GPT-6 Sol", "review"]);
        assert.equal((await mmenu.locator(".pm-item", { hasText: "review" }).locator(".pm-note").textContent()).trim(), w("routing group"));
        await mmenu.locator(".pm-item", { hasText: "review" }).click();
        await mmenu.waitFor({ state: "detached" });
        assert.equal((await ed.locator(".lib-subagentmodel").textContent()).trim(), "review");
        assert.equal((await hint("model").textContent()).trim(), w("Not set for {agents}: theirs runs on the model the agent picks", { agents: "Claude Code, Cursor, Droid" }));
        // the skills from an app menu
        assert.equal((await ed.locator(".lib-subagentskills").textContent()).trim(), w("None"));
        await ed.locator(".lib-subagentskills").click();
        const menu = page.locator(".proto-menu");
        await menu.waitFor();
        assert.equal((await menu.locator(".pm-head").textContent()).trim(), w("Skills"));
        await menu.locator(".pm-item", { hasText: /^tdd$/ }).click();
        // a click outside closes it, keeping what was ticked
        await ed.locator(".ehead b").click();
        await menu.waitFor({ state: "detached" });
        assert.equal((await ed.locator(".lib-subagentskills").textContent()).trim(), "tdd");
        await ed.waitFor();
        assert.deepEqual(await sideways(page), [], "sideways scroll in the editor");
        await ed.locator(".bar button.primary").click();
        await ed.waitFor({ state: "detached" });
        assert.deepEqual(posts.shift(), { what: "subagents/save", body: { old: "", subagent: { name: "test-writer", description: "写测试", prompt: "Write tests first.\nThen run them.", model: "group/review", skills: ["tdd"], mcp: [], agents: ["claude", "codex", "cursor", "droid"] } } });
        await row("test-writer").waitFor();
        assert.equal((await row("test-writer").locator(".lib-subagentuses").textContent()).trim(), w("Model: {name}", { name: "review" }) + " · " + w("Skills: {names}", { names: "tdd" }));
        await v.locator(".lib-subagents-nomodel").filter({ hasText: w("{agents} can't be given a subagent's model: theirs runs on the model the agent picks.", { agents: "Claude Code, Cursor, Droid" }) }).waitFor();
        // Codex's subagent model set on the Agents page from here on
        state.held = "lead";

        // a row opens it again; Remove asks first, Cancel posts nothing
        await press(page, row("reviewer"));
        await ed.waitFor();
        await settled(page);
        assert.equal((await ed.locator(".ehead b").textContent()).trim(), "reviewer");
        assert.equal(await ed.locator(".lib-subagentprompt").inputValue(), "You review code.\n\nReport bugs only.");
        assert.equal((await ed.locator(".lib-subagentmcp").textContent()).trim(), "github");
        await ed.locator(".bar button.danger").click();
        const sheet = page.locator("dialog.action-confirm");
        await sheet.waitFor();
        await sheet.locator("button", { hasText: w("Cancel") }).click();
        await sheet.waitFor({ state: "detached" });
        await page.waitForTimeout(150);
        assert.deepEqual(posts, [], "Cancel posts nothing");
        await ed.locator(".bar button.danger").click();
        await sheet.waitFor();
        await sheet.locator("button.danger-fill").click();
        await ed.waitFor({ state: "detached" });
        assert.deepEqual(posts.shift(), { what: "subagents/remove", body: { name: "reviewer" } });
        assert.equal(await row("reviewer").count(), 0);
        await row("test-writer").waitFor();
        // opened again: its model, and that Codex's Agents page one takes its place
        await press(page, row("test-writer"));
        await ed.waitFor();
        await settled(page);
        assert.equal((await ed.locator(".lib-subagentmodel").textContent()).trim(), "review");
        assert.equal((await hint("model").textContent()).trim(), w("Not set for {agents}: theirs runs on the model the agent picks", { agents: "Claude Code, Cursor, Droid" }) + " · " + w("Codex's subagent model on the Agents page is set, and takes the place of this one"));
        await ed.locator(".bar button", { hasText: w("Cancel") }).click();
        await ed.waitFor({ state: "detached" });

        assert.deepEqual(posts, []);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
