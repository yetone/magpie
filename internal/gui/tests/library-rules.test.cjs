// Run with Node's test runner and Playwright on the module path; see README.md.
// JonahFu on X: Rules, a Library tab after Slash commands. A rule (a
// name and a Markdown body, kept to some files by globs if wanted) is
// kept once and given to the agents that read a folder of rules: Claude
// Code, Cline and Kiro CLI. Its row shows the name and its paths, or its
// first line. Agents magpie can't give rules to (an agent in WSL) have
// greyed chips that say why, post nothing, and an aside names them. A
// row's chip posts rules/agents. Add opens the editor with every agent
// that can take one picked: the paths are folded under Advanced; a hint
// names the agents picked that read every rule always (Cline) while the
// rule has paths; Save posts rules/save with one glob a line. A row
// opens it again, Advanced open when it has paths, with each agent's
// file as a path to show; Remove asks first, then posts rules/remove.
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
const HOME = "/Users/jonah";
const agent = (id, name, more = {}) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json`, ...more });
const WSL = "claude@wsl:Ubuntu";
const RULE_AGENTS = [
  { id: "claude", dir: `${HOME}/.claude/rules`, paths: true },
  { id: "cline", dir: `${HOME}/.cline/rules` },
  { id: "kiro-cli", dir: `${HOME}/.kiro/steering`, paths: true },
  { id: WSL, wsl: true, no: "magpie gives no rules to an agent in WSL yet" },
];
const API = `${HOME}/.claude/rules/api.md`;
const rule = (name, paths, body, agents, files) => ({ name, paths, body, agents, files });
const lib = (rules) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex"), agent("cline", "Cline"), agent("kiro-cli", "Kiro CLI"), agent(WSL, "Claude Code (Ubuntu)")],
  instructions: { agents: [] }, servers: [], skills: [], foundServers: [], projects: [], foundSkills: [], problems: [],
  hooks: [], hookAgents: [], subagents: [], subagentAgents: [], commands: [], commandAgents: [], rules, ruleAgents: RULE_AGENTS,
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
    if (url.pathname === "/api/library") return json(lib(state.rules));
    if (url.pathname.startsWith("/api/library/") && req.method() === "POST") {
      const body = req.postDataJSON(), what = url.pathname.replace("/api/library/", "");
      posts.push({ what, body });
      if (what === "reveal") return json({});
      if (what === "rules/agents") state.rules = state.rules.map((x) => (x.name === body.name ? { ...x, agents: body.agents } : x));
      if (what === "rules/save") state.rules = [...state.rules.filter((x) => x.name !== body.old), body.rule];
      if (what === "rules/remove") state.rules = state.rules.filter((x) => x.name !== body.name);
      return json({ ...lib(state.rules), result: { changed: ["claude"] } });
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
  for (const e of document.querySelectorAll("#view-library .lib-rule, #view-library .lib-aside, #view-library .lib-tabs, #modal .lib-ruleedit *")) {
    const r = e.getBoundingClientRect();
    if (r.width && (r.right > w + 0.5 || r.left < -0.5)) over.push((e.className || e.tagName) + " " + Math.round(r.left) + "–" + Math.round(r.right));
  }
  return over;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Library's Rules tab", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    I18N = strings().I18N; // i18n.js's and the plugins'
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());

    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(lang, async () => {
        const w = (key, vars) => tr(lang, key, vars);
        const state = { rules: [rule("api", ["src/api/**/*.ts", "docs/**/*.md"], "# API\n\nEvery endpoint validates its input.", ["claude", "kiro-cli"], { claude: API, "kiro-cli": `${HOME}/.kiro/steering/api.md` }), rule("style", [], "## Style\n\nTabs, not spaces.", ["claude"], { claude: `${HOME}/.claude/rules/style.md` })] };
        const posts = [], errors = [];
        const open = async (width) => {
          const ctx = await browser.newContext({ viewport: { width, height: 1000 }, reducedMotion: "reduce" });
          await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "rules"); } catch {} });
          const page = await ctx.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
          await page.route("**/*", server(lang, state, posts));
          await page.goto("http://magpie.test/");
          await page.locator('button[data-view="library"]').click();
          await page.locator("#view-library .lib-rule").first().waitFor();
          return { ctx, page };
        };

        // 1100 wide: the list, and the editor
        {
          const { ctx, page } = await open(1100);
          assert.deepEqual(await sideways(page), [], "sideways scroll with the list at 1100");
          await press(page, page.locator("#view-library .lib-rule").first());
          await page.locator("#modal .lib-ruleedit").waitFor();
          await settled(page);
          assert.deepEqual(await sideways(page), [], "sideways scroll in the editor at 1100");
          await ctx.close();
          posts.length = 0;
        }

        const { ctx, page } = await open(420);
        const v = page.locator("#view-library");
        const row = (name) => v.locator(".lib-rule").filter({ has: page.locator(".name", { hasText: new RegExp("^" + name + "$") }) });
        const agentsOf = (name) => state.rules.find((s) => s.name === name)?.agents;
        assert.deepEqual(await sideways(page), [], "sideways scroll with the list at 420");

        // the tab, with its count, between Slash commands and Claude plugins
        const tabs = (await v.locator(".lib-tabs button").allTextContents()).map((x) => x.trim());
        const at = tabs.indexOf(w("Rules") + " · 2");
        assert(at > 0, tabs.join("|"));
        assert.deepEqual(tabs.slice(at - 1, at + 2), [w("Slash commands"), w("Rules") + " · 2", w("Claude plugins")]);
        // a rule kept to files shows them under its name
        assert.equal((await row("api").locator(".lib-ruledesc").textContent()).trim(), "src/api/**/*.ts · docs/**/*.md");
        // one with none, its first line
        assert.equal((await row("style").locator(".lib-ruledesc").textContent()).trim(), "Style");
        await v.locator(".lib-rules-no").filter({ hasText: w("magpie can't give rules to {agents}: their chips are greyed, and say why.", { agents: "Claude Code (Ubuntu)" }) }).waitFor();
        // Codex reads no folder of rules: no chip at all
        assert.equal(await row("api").locator('.lib-ag[data-agent="codex"]').count(), 0);

        // the agent in WSL: greyed, says why, and a click posts nothing
        const wsl = row("api").locator(`.lib-ag[data-agent="${WSL}"]`);
        assert.equal(await wsl.getAttribute("aria-disabled"), "true");
        assert.equal(await wsl.getAttribute("title"), w("magpie gives rules only to agents on this computer, not yet to {agent} in WSL", { agent: "Claude Code (Ubuntu)" }));
        await press(page, wsl);
        await page.waitForTimeout(150);
        assert.deepEqual(posts, [], "a greyed chip posted");

        // a row's chip gives it to Cline
        await press(page, row("api").locator('.lib-ag[data-agent="cline"]'));
        await page.waitForFunction(() => document.querySelector('#view-library .lib-rule .lib-ag[data-agent="cline"][aria-pressed="true"]'));
        await page.waitForTimeout(200);
        assert.deepEqual(posts.shift(), { what: "rules/agents", body: { name: "api", agents: ["claude", "kiro-cli", "cline"] } });
        assert.deepEqual(agentsOf("api"), ["claude", "kiro-cli", "cline"]);

        // Add: the editor, every agent that can take it picked, the paths folded
        await press(page, v.locator(".after-list button"));
        const ed = page.locator("#modal .lib-ruleedit");
        await ed.waitFor();
        await settled(page);
        assert.equal(await page.locator("#modal select").count(), 0, "a native <select>");
        assert.equal((await ed.locator(".ehead b").textContent()).trim(), w("New rule"));
        const edChip = (id) => ed.locator(`.lib-ag[data-agent="${id}"]`);
        for (const id of ["claude", "cline", "kiro-cli"]) assert.equal(await edChip(id).getAttribute("aria-pressed"), "true", id);
        assert.equal(await edChip(WSL).getAttribute("aria-pressed"), "false");
        const more = ed.locator("details.lib-rulemore");
        assert.equal(await more.evaluate((d) => d.open), false, "Advanced open on a new rule");
        assert.equal(await ed.locator(".lib-rulefiles").count(), 0, "files listed for a rule not saved");
        await ed.locator("input").first().fill("testing");
        const body = ed.locator(".lib-rulebody");
        await body.click();
        await page.keyboard.type("Run the tests first.");
        // the body's Enter is a new line, not a save
        await page.keyboard.press("Enter");
        await page.keyboard.type("测试要覆盖边界。");
        await page.waitForTimeout(150);
        assert.deepEqual(posts, [], "the body's Enter saved");
        await more.locator("summary").click();
        const paths = ed.locator(".lib-rulepaths");
        const hint = ed.locator(".lib-rulehint-paths");
        assert.equal((await hint.textContent()).trim(), "", "a hint with no paths");
        await paths.fill("**/*_test.go\n\n  **/*.test.ts  \n**/*_test.go");
        // the hint names the agents picked that read every rule always
        assert.equal((await hint.textContent()).trim(), w("Not written for {agents}: they read every rule in every conversation", { agents: "Cline" }));
        await edChip("cline").click();
        assert.equal((await hint.textContent()).trim(), "");
        assert.deepEqual(await sideways(page), [], "sideways scroll in the editor");
        await ed.locator(".bar button.primary").click();
        await ed.waitFor({ state: "detached" });
        assert.deepEqual(posts.shift(), { what: "rules/save", body: { old: "", rule: { name: "testing", paths: ["**/*_test.go", "**/*.test.ts"], body: "Run the tests first.\n测试要覆盖边界。", agents: ["claude", "kiro-cli"] } } });
        await row("testing").waitFor();

        // a row opens it again: Advanced open for its paths, and each
        // agent's file a path to show
        await press(page, row("api"));
        await ed.waitFor();
        await settled(page);
        assert.equal((await ed.locator(".ehead b").textContent()).trim(), "api");
        assert.equal(await ed.locator(".lib-rulebody").inputValue(), "# API\n\nEvery endpoint validates its input.");
        assert.equal(await more.evaluate((d) => d.open), true, "Advanced closed on a rule with paths");
        assert.equal(await paths.inputValue(), "src/api/**/*.ts\ndocs/**/*.md");
        assert.equal((await hint.textContent()).trim(), w("Not written for {agents}: they read every rule in every conversation", { agents: "Cline" }));
        const files = ed.locator(".lib-rulefile .lib-path");
        assert.deepEqual((await files.allTextContents()).map((x) => x.trim()).sort(), ["~/.claude/rules/api.md", "~/.kiro/steering/api.md"]);
        await files.filter({ hasText: "~/.claude/rules/api.md" }).click();
        await page.waitForTimeout(150);
        assert.deepEqual(posts.shift(), { what: "reveal", body: { path: API } });
        assert.deepEqual(await sideways(page), [], "sideways scroll in the editor with files");
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
        assert.deepEqual(posts.shift(), { what: "rules/remove", body: { name: "api" } });
        assert.equal(await row("api").count(), 0);
        await row("testing").waitFor();

        assert.deepEqual(posts, []);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
