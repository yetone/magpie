// Run with Node's test runner and Playwright on the module path; see README.md.
// Subagents from a repository (CHENGJIAHEGOXIS on X): the Subagents tab
// has a find line like the skills', for msitarzewski/agency-agents' link
// or a folder in it. What it finds is listed to pick from: Select all,
// a filter past eight, and a tag for one the library has as it is (not
// pickable), one changed upstream (picked to update), one changed here
// (left alone, not pickable) and one whose name the user's own subagent
// has (it goes in beside it). The files left out say why. Import posts
// subagents/import with the paths picked and the agents ticked, never
// one magpie can't write for (WSL), and says what it did.
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
const HOME = "/Users/cheng";
const SRC = "https://github.com/msitarzewski/agency-agents";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const WSL = "claude@wsl:Ubuntu";
const SUB_AGENTS = [
  { id: "claude", dir: `${HOME}/.claude/agents`, skills: true, mcp: true },
  { id: "codex", dir: `${HOME}/.codex/agents`, model: true },
  { id: WSL, wsl: true, no: "magpie gives no subagents to an agent in WSL yet" },
];
const lib = (subagents) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex"), agent(WSL, "Claude Code (Ubuntu)")],
  instructions: { agents: [] }, servers: [], skills: [], foundServers: [], projects: [], foundSkills: [], problems: [],
  hooks: [], hookAgents: [], subagents, subagentAgents: SUB_AGENTS, subagentModels: [],
});
// agency-agents' own names and descriptions, as the probe reads them
const cand = (dir, name, description, more = {}) => ({ path: `${dir}/${dir}-${name}.md`, name, description, ...more });
const CANDIDATES = [
  cand("engineering", "ai-data-remediation-engineer", "Specialist in self-healing data pipelines — uses air-gapped local SLMs and semantic clustering to automatically detect, classify, and fix data anomalies at scale. Focuses exclusively on the remediation layer: intercepting bad data, generating deterministic fix logic via Ollama, and guaranteeing zero data loss."),
  cand("engineering", "backend-architect", "Senior backend architect specializing in scalable system design, database architecture, API development, and cloud infrastructure.", { as: "backend-architect-2" }),
  cand("engineering", "frontend-developer", "Expert frontend developer specializing in modern web technologies, React/Vue/Angular frameworks, UI implementation, and performance optimization", { state: "have" }),
  cand("engineering", "security-engineer", "Expert application security engineer specializing in threat modeling, vulnerability assessment, secure code review.", { state: "update" }),
  cand("design", "ui-designer", "Expert UI designer specializing in visual design systems, component libraries, and pixel-perfect interface creation", { state: "edited" }),
  cand("design", "ux-researcher", "Expert user experience researcher specializing in user behavior analysis, usability testing, and data-driven design insights"),
  cand("marketing", "content-creator", "Expert content strategist and creator for multi-platform campaigns."),
  cand("marketing", "growth-hacker", "Expert growth strategist specializing in rapid user acquisition through data-driven experimentation."),
  cand("paid-media", "paid-media-auditor", "Comprehensive paid media auditor who systematically evaluates Google Ads, Microsoft Ads, and Meta accounts across 200+ checkpoints."),
  cand("testing", "api-tester", "Expert API testing specialist focused on comprehensive API validation, performance testing, and quality assurance"),
];
const LEFT = [{ path: "integrations/mcp-memory/backend-architect-with-memory.md", error: "the same name as engineering/engineering-backend-architect.md" }];

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
    if (url.pathname === "/api/library") return json(lib(state.subagents));
    if (url.pathname === "/api/library/subagents/probe") {
      posts.push({ what: "subagents/probe", body: req.postDataJSON() });
      return json({ source: SRC, kind: "github", candidates: CANDIDATES, left: LEFT });
    }
    if (url.pathname.startsWith("/api/library/") && req.method() === "POST") {
      const body = req.postDataJSON(), what = url.pathname.replace("/api/library/", "");
      posts.push({ what, body });
      if (what === "subagents/import") {
        const added = body.paths.map((p) => CANDIDATES.find((c) => c.path === p)).filter((c) => !c.state);
        state.subagents = [...state.subagents, ...added.map((c) => ({ name: c.as || c.name, description: c.description, prompt: "…", agents: body.agents }))];
        return json({ ...lib(state.subagents), result: { changed: ["claude"], subagents: { added: added.map((c) => c.as || c.name), updated: ["security-engineer"] } } });
      }
      return json({ ...lib(state.subagents), result: { changed: [] } });
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
  for (const e of document.querySelectorAll("#view-library .lib-safind, #view-library .lib-safind *")) {
    const r = e.getBoundingClientRect();
    if (r.width && (r.right > w + 0.5 || r.left < -0.5)) over.push((e.className || e.tagName) + " " + Math.round(r.left) + "–" + Math.round(r.right));
  }
  return over;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": subagents imported from a repository", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    I18N = strings().I18N;
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());

    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [420, 1100]) {
        await t.test(lang + " " + width, async () => {
          const w = (key, vars) => tr(lang, key, vars);
          const state = { subagents: [{ name: "backend-architect", description: "My own", prompt: "Mine.", agents: ["claude"] }] };
          const posts = [], errors = [];
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
          const v = page.locator("#view-library");
          const card = v.locator(".lib-safind");
          await card.waitFor();
          assert.deepEqual(await sideways(page), [], `sideways scroll before a find at ${width}`);

          const inp = card.locator('input[data-lib="sasource"]');
          assert.equal(await inp.getAttribute("placeholder"), w("owner/repo, a GitHub or GitLab link, any git repository's address, or a folder on this computer"));
          await inp.fill(SRC);
          const find = card.locator(".lib-find button", { hasText: w("Find subagents") });
          await press(page, find);
          await card.locator(".lib-probe").waitFor();
          assert.deepEqual(posts.shift(), { what: "subagents/probe", body: { source: SRC } });

          // what was found, where
          assert.equal((await card.locator(".lib-probehead .note").textContent()).trim(), w("{count} subagents in {src}", { count: 10, src: "github.com/msitarzewski/agency-agents" }));
          const pick = (name) => card.locator(".lib-sapick").filter({ has: page.locator(".name", { hasText: new RegExp("^" + name + "$") }) });
          assert.equal(await card.locator(".lib-sapick").count(), 10);
          // each state, tagged; the ones there's nothing to do for, not pickable
          assert.equal((await pick("frontend-developer").locator(".lib-tag").textContent()).trim(), w("in the library"));
          assert.equal(await pick("frontend-developer").locator("input").isDisabled(), true);
          assert.equal((await pick("security-engineer").locator(".lib-tag").textContent()).trim(), w("Changed upstream"));
          assert.equal(await pick("security-engineer").locator("input").isDisabled(), false);
          assert.equal((await pick("ui-designer").locator(".lib-tag").textContent()).trim(), w("Changed here"));
          assert.equal(await pick("ui-designer").locator("input").isDisabled(), true);
          // the user's own backend-architect is left as it is: this one goes in beside it
          assert.equal((await pick("backend-architect-2").locator(".lib-tag").textContent()).trim(), w("adds as {name}", { name: "backend-architect-2" }));
          assert.equal(await pick("backend-architect-2").locator(".lib-tag").getAttribute("title"), w("The library has another subagent called {name}. This one goes in beside it as {as}.", { name: "backend-architect", as: "backend-architect-2" }));
          // the file left out, and why
          assert.equal((await card.locator(".lib-left").textContent()).trim(), w("{path} left out: {error}", LEFT[0]));
          assert.deepEqual(await sideways(page), [], `sideways scroll with what was found at ${width}`);

          // ten found: none picked, then Select all picks the eight there's something to do for
          const go = card.locator(".lib-probefoot button.primary");
          assert.equal(await go.isDisabled(), true);
          await press(page, card.locator(".lib-probehead button", { hasText: w("Select all") }));
          assert.equal((await go.textContent()).trim(), w("Import {count} subagents", { count: 8 }));
          assert.equal(await card.locator(".lib-probehead button").textContent(), w("Select none"));

          // the filter narrows the list, by a word of the description too
          const filter = card.locator('input[data-lib="safilter"]');
          assert.equal(await filter.getAttribute("placeholder"), w("Filter…"));
          await filter.fill("pipelines");
          assert.deepEqual(await card.locator(".lib-sapick .name").allTextContents(), ["ai-data-remediation-engineer"]);
          await filter.fill("");

          // the WSL agent's chip: greyed, and never posted
          assert.equal(await card.locator(`.lib-ag[data-agent="${WSL}"]`).getAttribute("aria-disabled"), "true");
          await press(page, go);
          await page.waitForFunction(() => !document.querySelector("#view-library .lib-safind .lib-probe"));
          const imp = posts.shift();
          assert.equal(imp.what, "subagents/import");
          assert.equal(imp.body.source, SRC);
          assert.deepEqual(imp.body.agents, ["claude", "codex"]);
          assert.deepEqual([...imp.body.paths].sort(), CANDIDATES.filter((c) => !c.state || c.state === "update").map((c) => c.path).sort());
          const said = w("{count} subagents imported", { count: 7 }) + " · " + w("updated: {names}", { names: "security-engineer" });
          await page.waitForFunction((s) => document.querySelector("#status").textContent === s, said).catch(async () => assert.equal(await page.locator("#status").textContent(), said));
          assert.equal(await v.locator(".lib-subagent").count(), 8);
          assert.deepEqual(await sideways(page), [], `sideways scroll after the import at ${width}`);
          assert.deepEqual(errors, []);
          await ctx.close();
        });
      }
    }
  });
}
