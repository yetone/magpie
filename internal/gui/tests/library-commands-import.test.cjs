// Run with Node's test runner and Playwright on the module path; see README.md.
// Slash commands the user already made in their agents (miantiao_me on X:
// "can the ones I defined in Pi be imported?"): the Slash commands tab has
// a find line like the Subagents tab's. Find in agents posts
// commands/probe and lists what was found: /name, what it is for, and the
// agents it is in. A new name is ticked. One the library has saying the
// same is tagged and pickable (it gives it to these agents too); one
// whose name the library has for another command is tagged and can't be
// picked; one name saying different things in two agents is tagged on
// both, and ticking one unticks the other. The files left out say why.
// Import posts commands/import with the paths ticked and says how many
// were imported. Nothing found says so.
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
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const COMMAND_AGENTS = [
  { id: "claude", dir: `${HOME}/.claude/commands`, args: true },
  { id: "pi", dir: `${HOME}/.pi/agent/prompts`, args: true },
  { id: "gemini", dir: `${HOME}/.gemini/commands` },
];
const lib = (commands) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("pi", "Pi"), agent("gemini", "Gemini CLI")],
  instructions: { agents: [] }, servers: [], skills: [], foundServers: [], projects: [], foundSkills: [], problems: [],
  hooks: [], hookAgents: [], subagents: [], subagentAgents: [], commands, commandAgents: COMMAND_AGENTS,
});
// Pi's own prompts (its repository's .pi/prompts), and the user's others
const pi = (name) => `${HOME}/.pi/agent/prompts/${name}.md`;
const cand = (name, description, files, more = {}) => ({
  path: Object.values(files)[0], name, description, prompt: "…", agents: Object.keys(files), files, ...more,
});
const CANDIDATES = [
  cand("cl", "Audit changelog entries before release", { pi: pi("cl") }),
  cand("is", "Analyze GitHub issues (bugs or feature requests)", { pi: pi("is") }),
  cand("pr", "Review PRs from URLs with structured issue and code analysis", { claude: `${HOME}/.claude/commands/pr.md`, pi: pi("pr") }),
  cand("review", "Review a diff", { claude: `${HOME}/.claude/commands/review.md` }, { clash: true }),
  cand("review", "Review a diff for bugs only, and say nothing of style", { pi: pi("review") }, { clash: true }),
  cand("ship", "Ship it", { gemini: `${HOME}/.gemini/commands/ship.toml` }, { state: "same" }),
  cand("wr", "Finish the current task end-to-end with changelog, commit, and push", { pi: pi("wr") }, { state: "taken" }),
];
const LEFT = [{ path: `${HOME}/.claude/commands/Fix.md`, error: "magpie names a command with lowercase letters, digits, - and _ only" }];

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
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(lib(state.commands));
    if (url.pathname === "/api/library/commands/probe") {
      posts.push({ what: "commands/probe" });
      return json(state.empty ? { candidates: [] } : { candidates: CANDIDATES, left: LEFT });
    }
    if (url.pathname.startsWith("/api/library/") && req.method() === "POST") {
      const body = req.postDataJSON(), what = url.pathname.replace("/api/library/", "");
      posts.push({ what, body });
      if (what === "commands/import") {
        const picked = body.paths.map((p) => CANDIDATES.find((c) => c.path === p));
        const added = picked.filter((c) => !c.state), joined = picked.filter((c) => c.state === "same");
        state.commands = [...state.commands.map((x) => (joined.some((c) => c.name === x.name) ? { ...x, agents: ["claude", "gemini"] } : x)),
          ...added.map((c) => ({ name: c.name, description: c.description, prompt: c.prompt, agents: c.agents }))];
        return json({ ...lib(state.commands), result: { changed: ["pi"], commands: { added: added.map((c) => c.name), joined: joined.map((c) => c.name) } } });
      }
      return json({ ...lib(state.commands), result: { changed: [] } });
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
const sideways = (page) => page.evaluate(() => {
  const over = [];
  const w = document.documentElement.clientWidth;
  if (document.documentElement.scrollWidth > w) over.push("page " + document.documentElement.scrollWidth);
  const v = document.querySelector("#view-library");
  if (v.scrollWidth > v.clientWidth + 1) over.push("library " + v.scrollWidth + ">" + v.clientWidth);
  for (const e of document.querySelectorAll("#view-library .lib-cmdfind, #view-library .lib-cmdfind *")) {
    const r = e.getBoundingClientRect();
    if (r.width && (r.right > w + 0.5 || r.left < -0.5)) over.push((e.className || e.tagName) + " " + Math.round(r.left) + "–" + Math.round(r.right));
  }
  return over;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": slash commands imported from the agents", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    I18N = strings().I18N;
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());

    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [420, 1100]) {
        await t.test(lang + " " + width, async () => {
          const w = (key, vars) => tr(lang, key, vars);
          const state = { commands: [
            { name: "ship", description: "Ship it", prompt: "…", agents: ["claude"] },
            { name: "wr", description: "Wrap up", prompt: "Mine.", agents: ["claude"] },
          ] };
          const posts = [], errors = [];
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
          const v = page.locator("#view-library");
          const card = v.locator(".lib-cmdfind");
          await card.waitFor();
          assert.equal((await card.locator(".lib-findnote").textContent()).trim(), w("Slash commands you made in your agents"));
          assert.deepEqual(await sideways(page), [], `sideways scroll before a find at ${width}`);

          const find = card.locator(".lib-find button", { hasText: w("Find in agents") });
          await press(page, find);
          await card.locator(".lib-probe").waitFor();
          assert.deepEqual(posts.shift(), { what: "commands/probe" });
          assert.equal((await card.locator(".lib-probehead .note").textContent()).trim(), w("{count} slash commands in your agents", { count: 7 }));
          const rows = card.locator(".lib-cmdpick");
          assert.equal(await rows.count(), 7);
          const row = (i) => rows.nth(i);
          // what each is, and where it is
          assert.equal((await row(2).locator(".name").textContent()).trim(), "/pr");
          assert.equal((await row(2).locator(".lib-cmdfrom").textContent()).trim(), "Claude Code · Pi");
          // new ones ticked, the first of a name saying different things
          // only; the library's same one pickable, unticked; one by a
          // name the library has for another, not pickable
          const checked = async () => Promise.all([...Array(7).keys()].map((i) => row(i).locator("input").isChecked()));
          assert.deepEqual(await checked(), [true, true, true, true, false, false, false]);
          for (const i of [3, 4]) {
            assert.equal((await row(i).locator(".lib-tag").textContent()).trim(), w("Differs by agent"));
            assert.equal(await row(i).locator(".lib-tag").getAttribute("title"), w("/{name} says something else in another agent: pick one.", { name: "review" }));
          }
          assert.equal((await row(5).locator(".lib-tag").textContent()).trim(), w("in the library"));
          assert.equal(await row(5).locator("input").isDisabled(), false);
          assert.equal((await row(6).locator(".lib-tag").textContent()).trim(), w("Name taken"));
          assert.equal(await row(6).locator(".lib-tag").getAttribute("title"), w("The library has another slash command called /{name}, so this one stays in the agent as it is.", { name: "wr" }));
          assert.equal(await row(6).locator("input").isDisabled(), true);
          assert.equal((await card.locator(".lib-left").textContent()).trim(), w("{path} left out: {error}", { path: "~/.claude/commands/Fix.md", error: LEFT[0].error }));
          assert.deepEqual(await sideways(page), [], `sideways scroll with what was found at ${width}`);

          // the other review instead: the first is unticked
          await press(page, row(4).locator("input"));
          assert.deepEqual(await checked(), [true, true, true, false, true, false, false]);
          // the list scrolls past 300px: the reader's own scroll to it
          await row(5).scrollIntoViewIfNeeded();
          await press(page, row(5).locator("input"));
          assert.deepEqual(await checked(), [true, true, true, false, true, true, false]);
          const go = card.locator(".lib-probefoot button.primary");
          assert.equal((await go.textContent()).trim(), w("Import {count} slash commands", { count: 5 }));
          await press(page, go);
          await page.waitForFunction(() => !document.querySelector("#view-library .lib-cmdfind .lib-probe"));
          const imp = posts.shift();
          assert.equal(imp.what, "commands/import");
          assert.deepEqual([...imp.body.paths].sort(), [pi("cl"), pi("is"), `${HOME}/.claude/commands/pr.md`, pi("review"), `${HOME}/.gemini/commands/ship.toml`].sort());
          const said = w("{count} slash commands imported", { count: 5 });
          await page.waitForFunction((s) => document.querySelector("#status").textContent === s, said).catch(async () => assert.equal(await page.locator("#status").textContent(), said));
          assert.equal(await v.locator(".lib-command").count(), 6);
          assert.deepEqual(await sideways(page), [], `sideways scroll after the import at ${width}`);

          // nothing of their own: said so, and closed
          state.empty = true;
          await press(page, find);
          await card.locator(".lib-probe").waitFor();
          posts.shift();
          assert.equal((await card.locator(".lib-probehead .note").textContent()).trim(), w("No slash commands of your own in your agents"));
          assert.equal(await card.locator(".lib-probefoot").count(), 0);
          await press(page, card.locator(".lib-probehead button", { hasText: w("Close") }));
          assert.equal(await card.locator(".lib-probe").count(), 0);
          assert.deepEqual(errors, []);
          await ctx.close();
        });
      }
    }
  });
}
