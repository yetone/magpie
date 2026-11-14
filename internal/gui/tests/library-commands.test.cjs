// Run with Node's test runner and Playwright on the module path; see README.md.
// miantiao_me on X: Slash commands, a Library tab after Subagents. A slash
// command (a name, a description, a prompt with $ARGUMENTS and $1, $2…)
// is kept once and given to agents with the chips every other tab has.
// Its row shows /name and what it is for, wrapped. Asides say which
// agents it is given to take only $ARGUMENTS (so a command using $1 isn't
// written for them) and which agents magpie can't write one for: Codex,
// which reads custom prompts no more, and an agent in WSL. Their chips
// are greyed, say why, and post nothing. A row's chip posts
// commands/agents. Add opens the editor with every agent that can take
// one picked: the prompt's Enter is a new line, not a save; an input
// method's Enter saves nothing; a hint names the agents picked that take
// only $ARGUMENTS while the prompt uses $1; a leading / on the name is
// dropped; Save posts commands/save. A row opens it again, and Remove
// asks first, then posts commands/remove.
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
const HOME = "/Users/mian";
const agent = (id, name, more = {}) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json`, ...more });
const LONG = "审阅指定文件或目录里改动过的代码，只报告真正的 bug：逻辑错误、边界条件、并发问题和资源泄漏；不评论代码风格，也不提出与本次改动无关的重构建议。";
const cmd = (name, description, prompt, agents) => ({ name, description, prompt, agents });
const WSL = "claude@wsl:Ubuntu";
const COMMAND_AGENTS = [
  { id: "claude", dir: `${HOME}/.claude/commands`, args: true },
  { id: "codex", no: "Codex reads custom prompts no more: its skills took their place" },
  { id: "gemini", dir: `${HOME}/.gemini/commands` },
  { id: "opencode", dir: `${HOME}/.config/opencode/commands`, args: true },
  { id: WSL, wsl: true, no: "magpie gives no slash commands to an agent in WSL yet" },
];
const lib = (commands) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex"), agent("gemini", "Gemini CLI"), agent("opencode", "OpenCode"), agent(WSL, "Claude Code (Ubuntu)")],
  instructions: { agents: [] }, servers: [], skills: [], foundServers: [], projects: [], foundSkills: [], problems: [],
  hooks: [], hookAgents: [], subagents: [], subagentAgents: [], commands, commandAgents: COMMAND_AGENTS,
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
    if (url.pathname === "/api/library") return json(lib(state.commands));
    if (url.pathname.startsWith("/api/library/") && req.method() === "POST" && !url.pathname.endsWith("/reveal")) {
      const body = req.postDataJSON(), what = url.pathname.replace("/api/library/", "");
      posts.push({ what, body });
      if (what === "commands/agents") state.commands = state.commands.map((x) => (x.name === body.name ? { ...x, agents: body.agents } : x));
      if (what === "commands/save") state.commands = [...state.commands.filter((x) => x.name !== body.old), body.command];
      if (what === "commands/remove") state.commands = state.commands.filter((x) => x.name !== body.name);
      return json({ ...lib(state.commands), result: { changed: ["claude"] } });
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
  for (const e of document.querySelectorAll("#view-library .lib-command, #view-library .lib-aside, #view-library .lib-tabs, #modal .lib-commandedit *")) {
    const r = e.getBoundingClientRect();
    if (r.width && (r.right > w + 0.5 || r.left < -0.5)) over.push((e.className || e.tagName) + " " + Math.round(r.left) + "–" + Math.round(r.right));
  }
  return over;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Library's Slash commands tab", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    I18N = strings().I18N; // i18n.js's and the plugins'
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());

    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(lang, async () => {
        const w = (key, vars) => tr(lang, key, vars);
        const state = { commands: [cmd("review", LONG, "Review $1 for bugs.\n\nFocus: $ARGUMENTS", ["claude", "gemini"])] };
        const posts = [], errors = [];
        const open = async (width) => {
          const ctx = await browser.newContext({ viewport: { width, height: 1000 }, reducedMotion: "reduce" });
          await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "commands"); } catch {} });
          const page = await ctx.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
          await page.route("**/*", server(lang, state, posts));
          await page.goto("http://magpie.test/");
          await page.locator('button[data-view="library"]').click();
          await page.locator("#view-library .lib-command").first().waitFor();
          return { ctx, page };
        };
        const wrapped = async (page, width) => {
          const r = page.locator("#view-library .lib-command").first();
          const fit = await r.locator(".lib-commanddesc").evaluate((e) => ({ clipped: e.scrollWidth > e.clientWidth + 1 || e.scrollHeight > e.clientHeight + 1, lines: Math.round(e.getBoundingClientRect().height / parseFloat(getComputedStyle(e).lineHeight)) }));
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
          await press(page, page.locator("#view-library .lib-command").first());
          await page.locator("#modal .lib-commandedit").waitFor();
          await settled(page);
          assert.deepEqual(await sideways(page), [], "sideways scroll in the editor at 1100");
          await ctx.close();
        }

        const { ctx, page } = await open(420);
        const v = page.locator("#view-library");
        const row = (name) => v.locator(".lib-command").filter({ has: page.locator(".name", { hasText: new RegExp("^/" + name + "$") }) });
        const agentsOf = (name) => state.commands.find((s) => s.name === name)?.agents;

        // the tab, with its count, between Subagents and Claude plugins
        const tabs = (await v.locator(".lib-tabs button").allTextContents()).map((x) => x.trim());
        const at = tabs.indexOf(w("Slash commands") + " · 1");
        assert(at > 0, tabs.join("|"));
        assert.deepEqual(tabs.slice(at - 1, at + 2), [w("Subagents"), w("Slash commands") + " · 1", w("Claude plugins")]);
        assert.equal((await row("review").locator(".lib-commanddesc").textContent()).trim(), LONG);
        await wrapped(page, 420);
        // Gemini takes only $ARGUMENTS, and review uses $1
        await v.locator(".lib-commands-noargs").filter({ hasText: w("{agents} take only $ARGUMENTS, all the words at once: a command that uses $1, $2… isn't written for them.", { agents: "Gemini CLI" }) }).waitFor();
        await v.locator(".lib-commands-no").filter({ hasText: w("magpie can't give slash commands to {agents}: their chips are greyed, and say why.", { agents: "Codex, Claude Code (Ubuntu)" }) }).waitFor();

        // Codex and the agent in WSL: greyed, say why, and a click posts nothing
        const codex = row("review").locator('.lib-ag[data-agent="codex"]');
        assert.equal(await codex.getAttribute("aria-disabled"), "true");
        assert.equal(await codex.getAttribute("title"), w("Codex reads custom prompts no more: its skills took their place"));
        const wsl = row("review").locator(`.lib-ag[data-agent="${WSL}"]`);
        assert.equal(await wsl.getAttribute("aria-disabled"), "true");
        assert.equal(await wsl.getAttribute("title"), w("magpie gives slash commands only to agents on this computer, not yet to {agent} in WSL", { agent: "Claude Code (Ubuntu)" }));
        await press(page, codex);
        await press(page, wsl);
        await page.waitForTimeout(150);
        assert.deepEqual(posts, [], "a greyed chip posted");

        // a row's chip gives it to OpenCode
        await press(page, row("review").locator('.lib-ag[data-agent="opencode"]'));
        await page.waitForFunction(() => document.querySelector('#view-library .lib-command .lib-ag[data-agent="opencode"][aria-pressed="true"]'));
        await page.waitForTimeout(200);
        assert.deepEqual(posts.shift(), { what: "commands/agents", body: { name: "review", agents: ["claude", "gemini", "opencode"] } });
        assert.deepEqual(agentsOf("review"), ["claude", "gemini", "opencode"]);

        // Add: the editor, every agent that can take it picked
        await press(page, v.locator(".after-list button"));
        const ed = page.locator("#modal .lib-commandedit");
        await ed.waitFor();
        await settled(page);
        assert.equal(await page.locator("#modal select").count(), 0, "a native <select>");
        assert.equal((await ed.locator(".ehead b").textContent()).trim(), w("New slash command"));
        const edChip = (id) => ed.locator(`.lib-ag[data-agent="${id}"]`);
        for (const id of ["claude", "gemini", "opencode"]) assert.equal(await edChip(id).getAttribute("aria-pressed"), "true", id);
        for (const id of ["codex", WSL]) assert.equal(await edChip(id).getAttribute("aria-pressed"), "false", id);
        await ed.locator("input").first().fill("/explain");
        const desc = ed.locator(".lib-commanddescin");
        assert.equal(await desc.getAttribute("placeholder"), w("e.g. Review a diff for bugs"));
        await desc.fill("讲解代码");
        // Enter choosing an input method's word is the IME's: nothing saved
        await desc.evaluate((i) => i.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", isComposing: true, keyCode: 229, bubbles: true })));
        await page.waitForTimeout(150);
        assert.deepEqual(posts, [], "an IME's Enter saved the command");
        // the prompt's Enter is a new line, not a save
        const prompt = ed.locator(".lib-commandprompt");
        const hint = ed.locator(".lib-commandhint-args");
        await prompt.click();
        await page.keyboard.type("Explain $ARGUMENTS.");
        assert.equal((await hint.textContent()).trim(), "", "a hint with no $1");
        await page.keyboard.press("Enter");
        await page.keyboard.type("Start with $1.");
        await page.waitForTimeout(150);
        assert.deepEqual(posts, [], "the prompt's Enter saved");
        assert.equal(await prompt.inputValue(), "Explain $ARGUMENTS.\nStart with $1.");
        // the hint names the agents picked that take only $ARGUMENTS
        assert.equal((await hint.textContent()).trim(), w("Not written for {agents} while it uses $1, $2…: they take only $ARGUMENTS", { agents: "Gemini CLI" }));
        await edChip("gemini").click();
        assert.equal((await hint.textContent()).trim(), "");
        assert.deepEqual(await sideways(page), [], "sideways scroll in the editor");
        await ed.locator(".bar button.primary").click();
        await ed.waitFor({ state: "detached" });
        assert.deepEqual(posts.shift(), { what: "commands/save", body: { old: "", command: { name: "explain", description: "讲解代码", prompt: "Explain $ARGUMENTS.\nStart with $1.", agents: ["claude", "opencode"] } } });
        await row("explain").waitFor();

        // a row opens it again; Remove asks first, Cancel posts nothing
        await press(page, row("review"));
        await ed.waitFor();
        await settled(page);
        assert.equal((await ed.locator(".ehead b").textContent()).trim(), "/review");
        assert.equal(await ed.locator(".lib-commandprompt").inputValue(), "Review $1 for bugs.\n\nFocus: $ARGUMENTS");
        assert.equal((await hint.textContent()).trim(), w("Not written for {agents} while it uses $1, $2…: they take only $ARGUMENTS", { agents: "Gemini CLI" }));
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
        assert.deepEqual(posts.shift(), { what: "commands/remove", body: { name: "review" } });
        assert.equal(await row("review").count(), 0);
        await row("explain").waitFor();
        // with no command given to Gemini using $1, its aside is gone
        assert.equal(await v.locator(".lib-commands-noargs").count(), 0);

        assert.deepEqual(posts, []);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
