// Run with Node's test runner and Playwright on the module path; see README.md.
// xMopf7RF on X: Claude Magpie, the separate Claude Desktop, is an agent of
// its own in the Library. Its Desktop (claude-desktop#magpie) and its Code
// tab's Claude Code (claude#magpie-desktop) each have a chip of their own
// beside Claude Desktop's and Claude Code's, named for them, lit only for
// what was given to them; a click on Claude Desktop's chip asks for Claude
// Desktop alone. MCP and skills, 980px and 420px, every language, both
// engines. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/mo";
const AS = `${HOME}/Library/Application Support`;
const TWIN = "claude-desktop#magpie", CODE = "claude#magpie-desktop";
const NAMES = { [TWIN]: "Claude Magpie", [CODE]: "Claude Code · Claude Magpie", "claude-desktop": "Claude Desktop", claude: "Claude Code" };
const fixture = () => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [
    { id: "claude", name: NAMES.claude, icon: "claudecode-color", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude.json` },
    { id: CODE, name: NAMES[CODE], icon: "claudecode-color", skills: `${HOME}/.claude-magpie-desktop/skills`, mcp: `${HOME}/.claude-magpie-desktop/.claude.json` },
    { id: "claude-desktop", name: NAMES["claude-desktop"], icon: "claude-color", mcp: `${AS}/Claude/claude_desktop_config.json`, skills: `${AS}/Claude-3p/local-agent-mode-sessions/skills-plugin/o/a/skills` },
    { id: TWIN, name: NAMES[TWIN], icon: "claude-color", mcp: `${AS}/Claude-Magpie-3p/claude_desktop_config.json`, skills: `${AS}/Claude-Magpie-3p/local-agent-mode-sessions/skills-plugin/o/a/skills` },
  ],
  instructions: { agents: [], sets: [] }, foundSkills: [], problems: [], foundServers: [], projects: [],
  servers: [{ name: "fs", transport: "stdio", command: "npx", args: ["-y", "fs"], agents: [TWIN] }],
  skills: [{ name: "pdf", kind: "folder", description: "Read PDFs", source: `${HOME}/skills/pdf`, agents: [CODE] }],
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

function server(lang, posts) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/library") return json(fixture());
    if ((url.pathname === "/api/library/servers/agents" || url.pathname === "/api/library/skills/agents") && req.method() === "POST") {
      posts.push({ path: url.pathname, ...req.postDataJSON() });
      return json({ ...fixture(), result: { problems: [] } });
    }
    if (url.pathname === "/api/library/mcp/check") return json({ servers: {} });
    if (url.pathname.startsWith("/api/library/market/")) return json({ items: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

const sideways = (page) => page.evaluate(() => {
  const w = document.documentElement.clientWidth;
  const over = [];
  if (document.documentElement.scrollWidth > w) over.push("page " + document.documentElement.scrollWidth);
  for (const e of document.querySelectorAll("#view-library .lib-row, #view-library .lib-agents")) {
    const r = e.getBoundingClientRect();
    if (r.width && (r.right > w + 0.5 || r.left < -0.5)) over.push(e.className + " " + Math.round(r.left) + "–" + Math.round(r.right));
  }
  return over;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Claude Magpie is an agent of its own in the Library", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    I18N = strings().I18N;
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());

    for (const width of [980, 420]) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const tab of ["mcp", "skills"]) {
      await t.test(`${lang} ${width}px ${tab}`, async () => {
        const errors = [], posts = [];
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        await ctx.addInitScript((x) => { try { localStorage.setItem("magpie.libTab", x); } catch {} }, tab);
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posts));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        const row = tab === "mcp" ? v.locator('.lib-server[data-server="fs"]') : v.locator(".lib-row", { hasText: "Read PDFs" }).first();
        await row.locator(".lib-agents").waitFor();
        const given = tab === "mcp" ? TWIN : CODE;
        // one chip each, none folded into Claude Desktop's or Claude Code's
        for (const id of Object.keys(NAMES)) {
          const chip = row.locator(`.lib-ag[data-agent="${id}"]`);
          assert.equal(await chip.count(), 1, `${tab}: no chip of its own for ${id}`);
          const on = id === given;
          assert.equal(await chip.getAttribute("aria-pressed"), on ? "true" : "false", `${tab}: ${id}`);
          const tip = on ? tr(lang, "{agent} has it — click to take it away", { agent: NAMES[id] }) : tr(lang, "Give it to {agent}", { agent: NAMES[id] });
          assert.equal((await chip.getAttribute("title")).split("\n")[0], tip);
        }
        assert.equal(await row.locator(".lib-ag[data-members]").count(), 0, "folded into a menu");
        // the page never scrolls sideways; a row's hand of four or more
        // chips can reach past its edge at 420px in ja, before Claude
        // Magpie too (six agents do it in en), #6558
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), true, "sideways scroll");
        if (!(width < 800 && lang === "ja")) assert.deepEqual(await sideways(page), [], "past the row's edge");

        // given to Claude Desktop, Claude Magpie's keeps what it had
        const sent = page.waitForResponse((r) => /\/api\/library\/(servers|skills)\/agents$/.test(new URL(r.url()).pathname) && r.request().method() === "POST");
        await row.locator('.lib-ag[data-agent="claude-desktop"]').click();
        await sent;
        assert.equal(posts.length, 1, "nothing posted");
        assert.equal(posts[0].path, tab === "mcp" ? "/api/library/servers/agents" : "/api/library/skills/agents");
        assert.deepEqual([...posts[0].agents].sort(), [given, "claude-desktop"].sort());
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
