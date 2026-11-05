// Run with Node's test runner and Playwright on the module path; see README.md.
// math_milian on Discord: fff-mcp indexes the folder it starts in and
// refuses home, where magpie's check started it, so the Library said
// "fff exited (1)" while the agent, which starts it in its project, used
// it. The check starts such a server again in a project folder of its own
// (inProject): given to agents that start it themselves, its row says how
// many tools it has, and its tooltip that it exits in home, with what it
// said; run by magpie in home for the agents (the MCP router, or Codex on
// magpie), its row says it won't start in home, and its editor has
// "Through the MCP router" to turn off even with the router off. A server
// that is fine has no switch with the router off. Every language,
// Chromium and WebKit, 1100 and 390 wide; nothing scrolls sideways. No
// backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = require("./strings.cjs").strings(); // i18n.js's and the plugins'
const tr = (lang, s, vars = {}) => ((lang === "en" ? s : I18N[lang][s]) || s).replace(/\{(\w+)\}/g, (_, k) => vars[k]);
const HOME = "/Users/mm";
const SAID = 'Error: "Failed to init file picker: Can not run certain FFF features in a file system root or home directories. Consider smaller per-project directories."';
const agents = [
  { id: "claude", name: "Claude Code", icon: "", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude.json` },
  { id: "codex", name: "Codex", icon: "", skills: `${HOME}/.codex/skills`, mcp: `${HOME}/.codex/config.toml` },
];
const WORDS = [
  "It started in a project folder and listed its tools, as an agent starts it there. It exits when started in your home folder",
  "won't start in home",
  "It exits when started in your home folder, where magpie runs it for the agents given it, and works in a project folder. Turn off Through the MCP router in its editor, so each agent starts it in its own project",
];
const found = {
  fff: { state: "ok", tools: 3, project: true, detail: SAID },
  "fff-codex": { state: "error", why: "home", code: 1, detail: SAID },
  files: { state: "ok", tools: 12 },
};

function server(lang, calls) {
  const lib = {
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
    agents, mcpHub: false, hubPath: "/mcp/magpie/hub/",
    instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [],
    servers: [
      { name: "fff", transport: "stdio", command: `${HOME}/.local/bin/fff-mcp`, agents: ["claude"] },
      { name: "fff-codex", transport: "stdio", command: `${HOME}/.local/bin/fff-mcp`, agents: ["codex"] },
      { name: "files", transport: "stdio", command: "npx", args: ["files-mcp"], agents: ["claude"] },
    ],
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light", gateway: "http://127.0.0.1:3425" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib });
    if (url.pathname === "/api/library/mcp/check") {
      const names = req.postDataJSON().names || Object.keys(found);
      return route.fulfill({ json: { servers: Object.fromEntries(names.map((n) => [n, found[n]])) } });
    }
    if (url.pathname === "/api/library/servers/save") {
      const { old, server: s } = req.postDataJSON();
      calls.push(`${s.name} ${JSON.stringify(s.hub)}`);
      const i = lib.servers.findIndex((x) => x.name === old);
      lib.servers[i] = { ...lib.servers[i], ...s };
      if (!s.hub) delete lib.servers[i].hub;
      return route.fulfill({ json: { ...lib, result: { changed: ["codex"] } } });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a server that won't start in home", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1100, 390]) {
        await t.test(`${lang} ${width}`, async () => {
          const w = (s, v) => tr(lang, s, v);
          if (lang !== "en") for (const s of WORDS) assert.ok(I18N[lang][s], `${lang}: ${s}`);
          const calls = [];
          const ctx = await browser.newContext({ viewport: { width, height: 800 } });
          await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
          const page = await ctx.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang, calls));
          await page.goto("http://magpie.test/");
          await page.locator('button[data-view="library"]').click();
          const status = (name) => page.locator(`#view-library .lib-health[data-server="${name}"]`);
          await page.waitForFunction(() => document.querySelectorAll("#view-library .lib-health").length === 3 && !document.querySelector("#view-library .lib-health.checking"));

          // given to agents that start it themselves: its tools, and why it
          // was checked in a project folder
          assert.equal(await status("fff").textContent(), w("{n} tools", { n: 3 }));
          assert.ok((await status("fff").getAttribute("title")).startsWith(w(WORDS[0]) + "\n" + SAID), await status("fff").getAttribute("title"));
          assert.equal(await status("fff").evaluate((b) => b.classList.contains("ok")), true);
          // run by magpie in home: it won't start there, and what to do
          assert.equal(await status("fff-codex").textContent(), w(WORDS[1]));
          assert.ok((await status("fff-codex").getAttribute("title")).startsWith(w(WORDS[2]) + "\n" + SAID), await status("fff-codex").getAttribute("title"));
          assert.equal(await status("fff-codex").evaluate((b) => b.classList.contains("err")), true);
          // a server that is fine says what it always did
          assert.ok((await status("files").getAttribute("title")).startsWith(w("It started and listed its tools")));

          const rowOf = (name) => page.locator("#view-library .lib-server", { has: page.locator(`.lib-health[data-server="${name}"]`) });
          const ed = page.locator("#modal .lib-editor");
          const sw = ed.getByRole("switch", { name: w("Through the MCP router"), exact: true });
          const open = async (name) => { await rowOf(name).locator(".name").click(); await ed.waitFor(); };
          const cancel = async () => { await ed.locator(":scope > .bar button", { hasText: w("Cancel") }).click(); await ed.waitFor({ state: "detached" }); };
          // the router off, a fine server has nothing to choose
          await open("files");
          assert.equal(await sw.count(), 0, "a switch for a server that is fine");
          await cancel();
          // the one that won't start in home can be kept direct
          await open("fff-codex");
          assert.equal(await sw.getAttribute("aria-checked"), "true");
          await sw.click();
          await ed.locator(":scope > .bar button.primary").click();
          await ed.waitFor({ state: "detached" });
          assert.deepEqual(calls, ['fff-codex "direct"']);
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
          await ctx.close();
        });
      }
    }
    assert.deepEqual(errors, []);
  });
}
