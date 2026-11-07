// Run with Node's test runner and Playwright on the module path; see README.md.
// zachzhao1984 on Discord: an app's binary by full path, with spaces in
// it, typed as it is in Library → MCP's command field
// ("/Applications/Agent HUD.app/Contents/MacOS/Agent HUD --mcp"), was the
// program /Applications/Agent with three arguments. The page sends the
// words as typed and the library makes them the program that is there
// (library.spacedProgram, stood in for here by the fake save); the row
// and the editor then show the path quoted, and saving it unchanged keeps
// it one argument. A path typed in quotes is one argument at once. A save
// the library refuses says why in the dialog, which stays open. English
// and Chinese, Chromium and WebKit, 1100 and 390 wide. No backend: the API
// is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = require("./strings.cjs").strings(); // i18n.js's and the plugins'
const tr = (lang, s) => (lang === "en" ? s : I18N[lang][s]) || s;
const HOME = "/Users/zz";
const EXE = "/Applications/Agent HUD.app/Contents/MacOS/Agent HUD";
const TYPED = EXE + " --mcp";
const SHOWN = "'" + EXE + "' --mcp";
const REFUSED = 'server name "Agent HUD": use letters, digits, - and _ only';

function server(lang, saves) {
  const lib = {
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
    agents: [{ id: "claude", name: "Claude Code", icon: "", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude.json` }],
    instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [],
    servers: [{ name: "mine", transport: "stdio", command: "/opt/mine/mcp", agents: ["claude"] }],
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib });
    if (url.pathname === "/api/library/mcp/check") return route.fulfill({ json: { servers: {} } });
    if (url.pathname === "/api/library/servers/save") {
      const { old, server: s } = req.postDataJSON();
      saves.push(structuredClone(s));
      if (!/^[A-Za-z0-9_-]+$/.test(s.name)) return route.fulfill({ status: 400, json: { error: REFUSED } });
      // the library's check, on a Mac where Agent HUD is installed
      const words = [s.command, ...s.args];
      for (let i = 1; i <= words.length; i++) {
        if (words.slice(0, i).join(" ") === EXE) { s.command = EXE; s.args = words.slice(i); break; }
      }
      const i = lib.servers.findIndex((x) => x.name === (old || s.name));
      if (i < 0) lib.servers.push(s); else lib.servers[i] = { ...lib.servers[i], ...s };
      return route.fulfill({ json: { ...lib, result: { changed: ["claude"] } } });
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
  test(engine + ": a program whose path has spaces, typed as it is", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of ["en", "zh"]) {
      for (const width of [1100, 390]) {
        await t.test(`${lang} ${width}`, async () => {
          const w = (s) => tr(lang, s);
          if (lang !== "en") for (const s of ["＋ Add server", "e.g. github", "Add", "Save"]) assert.ok(I18N[lang][s], `${lang}: ${s}`);
          const ctx = await browser.newContext({ viewport: { width, height: 800 } });
          await ctx.addInitScript(() => {
            Object.defineProperty(Navigator.prototype, "platform", { get: () => "MacIntel" });
            try { localStorage.setItem("magpie.libTab", "mcp"); } catch {}
          });
          const page = await ctx.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          const saves = [];
          await page.route("http://magpie.test/**", server(lang, saves));
          await page.goto("http://magpie.test/");
          await page.locator('button[data-view="library"]').click();
          await page.locator("#view-library .lib-body:not(.lib-skel) .lib-row").first().waitFor();

          const ed = page.locator("#modal .lib-editor");
          const name = ed.locator(`input[placeholder="${w("e.g. github")}"]`);
          const cmd = ed.locator("textarea.lib-cmd");
          const ok = ed.locator(":scope > .bar button.primary");
          const row = (n) => page.locator("#view-library .lib-server", { has: page.locator(`.lib-health[data-server="${n}"]`) });
          const add = async (n, line) => {
            const b = page.locator("#view-library .after-list button");
            assert.equal(await b.textContent(), w("＋ Add server"));
            await b.click();
            await ed.waitFor();
            // a new server's dialog puts the caret in its name a frame after
            // it opens: typed sooner, the command would land there
            await page.waitForFunction(() => document.activeElement?.matches("#modal .lib-editor input"));
            await name.fill(n);
            await cmd.fill(line);
            await ok.click();
          };

          // a name the library refuses: its reason, and the dialog stays
          await add("Agent HUD", TYPED);
          await ed.locator(".editor-error", { hasText: REFUSED }).waitFor();
          assert.equal(await ed.count(), 1);
          await name.fill("agent-hud");
          await ok.click();
          await ed.waitFor({ state: "detached" });
          assert.deepEqual([saves.at(-1).command, ...saves.at(-1).args], TYPED.split(" "), "sent as typed");
          const line = row("agent-hud").locator(".sub.mono");
          assert.equal(await line.textContent(), SHOWN);

          // the editor shows it quoted, and saving it unchanged keeps it
          await row("agent-hud").locator(".name").click();
          await ed.waitFor();
          assert.equal(await cmd.inputValue(), SHOWN);
          await ok.click();
          await ed.waitFor({ state: "detached" });
          assert.deepEqual([saves.at(-1).command, ...saves.at(-1).args], [EXE, "--mcp"]);

          // typed in quotes, it is one argument as it is sent
          await add("hud-quoted", `"${EXE}" --mcp`);
          await ed.waitFor({ state: "detached" });
          assert.deepEqual([saves.at(-1).command, ...saves.at(-1).args], [EXE, "--mcp"]);
          assert.equal(await row("hud-quoted").locator(".sub.mono").textContent(), SHOWN);

          const wide = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
          assert.ok(wide <= 0, `scrolls sideways by ${wide}px`);
          await ctx.close();
        });
      }
    }
    assert.deepEqual(errors, []);
  });
}
