// Run with Node's test runner and Playwright on the module path; see README.md.
// The owner, on the Library's Hooks tab: a found row's agent, Bring in and
// Remove "didn't line up down the list". A row whose command ran long had
// them further right than one with "bd prime": the long text squeezed the
// buttons, which could shrink, so each row's were a different width. Here
// every found row of Hooks, MCP and Skills, short and very long, has its
// agents' right edge, Bring in and Remove at the same x as the others'.
// In en, zh, zh-TW, ja and de, wide and at 600px. No backend: the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/ink";
const FILE = `${HOME}/.claude/settings.json`;
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const LONG = 'if [ -z "${HOME-}" ]; then case "${OSTYPE-}" in msys*|cygwin*|win32*) printf \'{}\\n\'; if [ -z "${ORCA_AGENT_HOOK_PORT-}" ]; then exit 0; fi; esac; fi; curl -s -X POST "http://127.0.0.1:${ORCA_AGENT_HOOK_PORT}/hook" -d @-';
const MID = '/Users/ink/.bun/bin/bun "/Users/ink/.confirmo/hooks/confirmo-hook.js"';
const hook = (key, event, matcher, cmd) => ({ key, agent: "claude", event, matcher, commands: [cmd], file: FILE, hook: { name: key, event: "turn-end", command: cmd, agents: ["claude"] } });
const FOUND_HOOKS = [
  hook("h1", "PostToolUse", "*", LONG),
  hook("h2", "PreCompact", "", "bd prime"),
  hook("h3", "PreToolUse", "", MID),
  hook("h4", "SessionEnd", "", "npx --yes ccclub@0.9.3 sync --silent"),
  hook("h5", "SessionStart", "*", LONG + LONG),
];
const server = (name, args) => ({ server: { name, transport: "stdio", command: "npx", args, agents: ["claude"] } });
const FOUND_SERVERS = [
  server("a", ["-y", "x"]),
  server("long-server-name-that-goes-on", ["-y", "@scope/some-very-long-package-name@1.2.3", "--root", "/Users/ink/workspace/projects/a/very/deep/folder/with/many/parts", "--verbose", "--no-color"]),
  server("b", ["y"]),
];
const skill = (name, description) => ({ name, description, agents: ["claude"] });
const FOUND_SKILLS = [
  skill("s", "Short"),
  skill("a-skill-with-a-long-name", "A description that runs on and on, well past the width of the row, so that it has to be cut with an ellipsis at the end of the line where it stops."),
  skill("t", ""),
];

const lib = () => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code")],
  instructions: { agents: [] }, servers: [], skills: [], projects: [], problems: [], hooks: [],
  hookAgents: [{ id: "claude", file: FILE, events: ["PreToolUse", "PostToolUse", "PreCompact", "SessionEnd", "SessionStart"] }],
  foundHooks: FOUND_HOOKS, foundServers: FOUND_SERVERS, foundSkills: FOUND_SKILLS,
});

function serve(lang) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
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

// each found row's right-hand side: the agents' box, Bring in and Remove
const edges = (page) => page.evaluate(() => [...document.querySelectorAll("#view-library .row.lib-row")]
  .filter((r) => r.querySelector(":scope > button.lib-icon.danger") && r.querySelector(":scope > .lib-have"))
  .map((r) => {
    const box = (e) => { const b = e.getBoundingClientRect(); return [b.left, b.right]; };
    return {
      name: r.querySelector(".name").textContent.trim(),
      have: box(r.querySelector(":scope > .lib-have"))[1],
      bring: box(r.querySelector(":scope > button.action")),
      remove: box(r.querySelector(":scope > button.lib-icon.danger")),
    };
  }));

const spread = (xs) => Math.max(...xs) - Math.min(...xs);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a found row's agent, Bring in and Remove line up down the list", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());

    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1280, 600]) {
        for (const tab of ["hooks", "mcp", "skills"]) {
          await t.test(`${lang} ${width} ${tab}`, async () => {
            const errors = [];
            const ctx = await browser.newContext({ viewport: { width, height: 1000 }, reducedMotion: "reduce" });
            await ctx.addInitScript((tab) => { try { localStorage.setItem("magpie.libTab", tab); } catch {} }, tab);
            const page = await ctx.newPage();
            page.setDefaultTimeout(5000);
            page.setDefaultNavigationTimeout(60000);
            page.on("pageerror", (e) => errors.push(e.message));
            await page.route("**/*", serve(lang));
            await page.goto("http://magpie.test/");
            await page.locator('button[data-view="library"]').click();
            await page.waitForFunction(() => document.querySelectorAll("#view-library .row.lib-row > button.lib-icon.danger").length >= 3);
            const rows = await edges(page);
            assert(rows.length >= 3, `${tab}: found rows listed, ${rows.length}`);
            const say = (k, xs) => `${k} spread ${spread(xs).toFixed(2)}px: ` + rows.map((r, i) => `${r.name.slice(0, 14)}=${xs[i].toFixed(1)}`).join(", ");
            if (process.env.MEASURE) console.log(engine, lang, width, tab, say("remove.left", rows.map((r) => r.remove[0])), "|", say("bring.left", rows.map((r) => r.bring[0])));
            for (const [k, xs] of [
              ["agents' right edge", rows.map((r) => r.have)],
              ["Bring in's left", rows.map((r) => r.bring[0])],
              ["Bring in's right", rows.map((r) => r.bring[1])],
              ["Remove's left", rows.map((r) => r.remove[0])],
              ["Remove's right", rows.map((r) => r.remove[1])],
            ]) assert(spread(xs) <= 0.5, say(k, xs));
            assert.deepEqual(errors, []);
            await ctx.close();
          });
        }
      }
    }
  });
}
