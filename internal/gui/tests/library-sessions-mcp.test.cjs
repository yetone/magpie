// Run with Node's test runner and Playwright on the module path; see README.md.
// Magpie Sessions in the Library's Discover (cryingLiu on X): an MCP server
// that searches and reads what was said in every agent's sessions on this
// computer. What was said is the user's own, so it is opt in: its sheet
// says what the agents picked may do and ticks none until they are picked,
// and the image servers' cost hint is not shown for it. Every language,
// Chromium and WebKit, at 1100 and 420 wide, nothing running off to the
// side; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = strings();
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const tr = (lang, s) => (lang === "en" ? s : I18N[lang]?.[s] ?? s);
const HOME = "/Users/tester";
const ABOUT = "Search and read what was said in every agent's sessions on this computer: an earlier conversation, decision or fix, from Claude Code, Codex or any agent Magpie reads.";
const HINT = "Pick the agents that may search and read what was said in your other sessions.";
const IMAGES = "Pick the agents that may generate images: each image costs what the model set in Settings › Models › Images charges.";

function route(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/library") {
      return json({
        dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
        agents: [
          { id: "claude", name: "Claude Code", icon: "claudecode-color", mcp: `${HOME}/.claude.json`, skills: `${HOME}/.claude/skills` },
          { id: "codex", name: "Codex", icon: "codex", mcp: `${HOME}/.codex/config.toml`, skills: `${HOME}/.codex/skills` },
        ],
        instructions: { agents: [], sets: [] }, servers: [], foundServers: [], projects: [], foundSkills: [], skills: [], problems: [],
      });
    }
    if (url.pathname === "/api/library/market/servers") {
      return json({ items: [
        { id: "magpie-sessions", name: "magpie-sessions", title: "Magpie Sessions", publisher: "Magpie", description: ABOUT, transport: "stdio", runs: "magpie", featured: true, optIn: true, have: "" },
        { id: "fetch", name: "fetch", title: "Fetch", description: "Read the web.", transport: "stdio", runs: "uvx", featured: true, have: "" },
      ] });
    }
    if (url.pathname === "/api/library/market/skills") return json({ items: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

const wide = (page) => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Magpie Sessions' sheet is opt in and says what it gives", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of LANGS) {
      for (const width of [1100, 420]) {
        await t.test(`${lang} at ${width}px`, async () => {
          const context = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
          const page = await context.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", route(lang));
          await page.addInitScript(() => { localStorage.setItem("magpie.libTab", "mcp"); });
          await page.goto("http://magpie.test/?view=library");
          await page.locator('#view-library .mk[data-market="mcp"] .mk-card[data-id="magpie-sessions"]').click();
          const sheet = page.locator("#modal .mk-sheet");
          await sheet.waitFor();
          assert.equal((await sheet.locator(".mk-name.big").textContent()).trim(), "Magpie Sessions");
          const hints = await sheet.locator(".mk-hint").allTextContents();
          assert(hints.map((h) => h.trim()).includes(tr(lang, HINT)), hints.join(" | "));
          assert(!hints.some((h) => h.includes(tr(lang, IMAGES))), "not the image servers' hint");
          // opt in: no agent is ticked until it is picked
          assert.equal(await sheet.locator(".lib-ag").count() >= 2, true, "the agents to pick from");
          assert.equal(await sheet.locator(".lib-ag.on").count(), 0, "no agent ticked");
          assert.equal(await wide(page), 0);
          if (lang !== "en") assert.ok(I18N[lang]?.[HINT], `${lang} has the hint`);
          assert.deepEqual(errors, []);
          await context.close();
        });
      }
    }
  });
}
