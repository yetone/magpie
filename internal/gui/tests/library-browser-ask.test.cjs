// Run with Node's test runner and Playwright on the module path; see README.md.
// Magpie Browser in the Library's MCP list, connected and given to no
// agent: it is added opt-in, so the router offers it to none until one is
// ticked, and an agent asked to use the browser reached for AppleScript
// instead. Its row asks which agents may use it, once the extension has
// connected (read again while the row is shown, no reload), each agent a
// button that gives it the server through servers/agents; once one has
// it, the row asks nothing more. Not connected, or given to an agent
// already, it asks nothing. The ask is a line of its own under the row's
// first line, as wide as the row past its picture, and that first line
// is the name and command alone, as tall as any other server's (tao0194
// on Discord: in the panel it sat inside the name's column, squeezed to
// a few characters between the command and the agents' chips). Every
// language, Chromium and WebKit, at 1100, 600, the panel's 440 and 360 wide,
// nothing running off to the side; no backend, the API is faked here.
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
const ASK = "Connected to your browser. Which agents may use it? They act in your tabs, signed in as you are.";

function fixture(lang, st) {
  const posts = [];
  const library = () => ({
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
    mcpHub: true, hubPath: "/mcp/magpie/hub/",
    agents: [
      { id: "claude", name: "Claude Code", icon: "claudecode-color", mcp: `${HOME}/.claude.json`, skills: `${HOME}/.claude/skills`, hub: "on" },
      { id: "codex", name: "Codex", icon: "codex-color", mcp: `${HOME}/.codex/config.toml`, hub: "on" },
      { id: "zed", name: "Zed", icon: "zed", mcp: `${HOME}/.config/zed/settings.json`, hub: "on" },
    ],
    servers: [
      { name: "magpie-browser", transport: "stdio", command: "/Applications/magpie.app/Contents/MacOS/magpie", args: ["mcp", "browser"], agents: st.agents },
      { name: "notes", transport: "stdio", command: "uvx", args: ["notes-mcp"], agents: [] },
      { name: "context7", transport: "http", url: "https://mcp.context7.com/mcp", agents: ["claude", "codex", "zed"] },
    ],
    instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [], problems: [],
  });
  const route = async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/library") return json(library());
    if (url.pathname === "/api/library/browser") {
      return json({ dir: `${HOME}/Magpie Browser Extension`, written: true, version: "0.1.0", connected: st.connected, ...(st.connected ? { extension: "0.1.0", browser: "Chrome/156" } : {}) });
    }
    if (url.pathname === "/api/library/servers/agents" && req.method() === "POST") {
      const body = req.postDataJSON();
      posts.push(body);
      if (body.name === "magpie-browser") st.agents = body.agents;
      return json({ ...library(), result: { problems: [] } });
    }
    if (url.pathname.startsWith("/api/library/market/")) return json({ items: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
  return { route, posts };
}

const wide = (page) => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a connected Magpie Browser given to no agent asks which agents may use it", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const shots = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [name, buf] of shots) await fs.writeFile(path.join(process.env.ARTIFACT_DIR, `${engine}-library-browser-ask-${name}.png`), buf);
      }
      await browser.close();
    });
    const open = async (lang, width, st) => {
      const f = fixture(lang, st);
      const context = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", f.route);
      await page.addInitScript(() => { localStorage.setItem("magpie.libTab", "mcp"); });
      await page.goto("http://magpie.test/?view=library");
      const row = page.locator("#view-library .lib-server").filter({ has: page.locator(".name", { hasText: "magpie-browser" }) });
      await row.waitFor();
      return { f, context, page, errors, row };
    };
    for (const lang of LANGS) {
      const w = (s) => tr(lang, s);
      for (const width of [1100, 600, 440, 360]) {
        await t.test(`${lang} at ${width}px: asks once connected, a pick gives it, then asks nothing`, async () => {
          const st = { connected: false, agents: [] };
          const { f, context, page, errors, row } = await open(lang, width, st);
          // not connected: nothing asked, however long it is looked at
          await page.waitForTimeout(400);
          assert.equal(await row.locator(".lib-browserask:visible").count(), 0);
          // the extension connects: the row asks on its next look
          st.connected = true;
          const ask = row.locator(".lib-browserask");
          await ask.waitFor({ state: "visible", timeout: 10000 });
          assert.equal(await ask.locator(".lib-browserask-q").textContent(), w(ASK));
          assert.deepEqual(await ask.locator(".lib-browserask-pick").allTextContents(), ["Claude Code", "Codex", "Zed"]);
          // the other server, given to no agent either, asks nothing
          assert.equal(await page.locator("#view-library .lib-browserask:visible").count(), 1);
          assert.equal(await wide(page), 0);
          assert.equal(await row.evaluate((e) => e.scrollWidth - e.clientWidth), 0);
          shots.push([`${lang}-${width}-ask`, await row.screenshot()]);
          // its own line: under the name, the command and the chips, from
          // the name's left edge to the chips' right edge, and the first
          // line no taller than the server's beside it
          const box = (l) => l.evaluate((e) => { const r = e.getBoundingClientRect(); return { top: r.top, bottom: r.bottom, left: r.left, right: r.right, height: r.height }; });
          const [a, who, chips, nm] = await Promise.all([box(ask), box(row.locator(":scope > .who")), box(row.locator(":scope > .lib-agents")), box(row.locator(".name"))]);
          const other = await box(page.locator("#view-library .lib-server").filter({ has: page.locator(".name", { hasText: "notes" }) }).locator(":scope > .who"));
          assert(a.top >= who.bottom - 0.5 && a.top >= chips.bottom - 0.5, `ask starts under the first line: ${JSON.stringify({ a, who, chips })}`);
          assert(Math.abs(a.left - nm.left) <= 1, `ask starts under the name: ${a.left} vs ${nm.left}`);
          // and the name beside the row's picture, however long the command
          const pic = await box(row.locator(":scope > .lib-glyph, :scope > .lib-logo"));
          assert(nm.left >= pic.right && nm.top < pic.bottom, `name beside the picture: ${JSON.stringify({ nm, pic })}`);
          assert(a.right >= chips.right - 1, `ask reaches the chips' right edge: ${a.right} vs ${chips.right}`);
          assert(Math.abs(who.height - other.height) <= 1, `first line as tall as another server's: ${who.height} vs ${other.height}`);

          await ask.locator('.lib-browserask-pick[data-agent="codex"]').click();
          await page.waitForFunction(() => document.querySelector('#view-library .lib-server .lib-agents .lib-ag[data-agent="codex"]')?.getAttribute("aria-pressed") === "true");
          assert.deepEqual(f.posts, [{ name: "magpie-browser", agents: ["codex"] }]);
          // asked and answered: the editor didn't open, and the row asks no more
          assert.equal(await page.locator("#modal .editor").count(), 0);
          assert.equal(await page.locator("#view-library .lib-browserask").count(), 0);
          assert.equal(await row.locator('.lib-agents .lib-ag[data-agent="claude"]').getAttribute("aria-pressed"), "false");
          assert.deepEqual(errors, []);
          await context.close();
        });
      }
    }
    await t.test("given to an agent already: connected, it asks nothing", async () => {
      const { context, page, errors, row } = await open("en", 1100, { connected: true, agents: ["claude"] });
      await page.waitForTimeout(800);
      assert.equal(await row.locator(".lib-browserask:visible").count(), 0);
      assert.deepEqual(errors, []);
      await context.close();
    });
  });
}
