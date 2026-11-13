// Run with Node's test runner and Playwright on the module path; see README.md.
// #3836, #6426 (jinpwng on X): one MCP server on a Mac and in WSL, each
// starting its own program. A synced command server's editor offers Use
// another command on this computer: the command line is then this
// computer's, the hint names what the other computers run, and the save
// sends that as shared. Use the synced command puts it back and sends no
// shared. A server kept to this computer has no such button. Every
// language, Chromium and WebKit, 1100 and 390 wide; nothing scrolls
// sideways. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = require("./strings.cjs").strings(); // i18n.js's and the plugins'
const tr = (lang, s, vars = {}) => ((lang === "en" ? s : I18N[lang][s]) || s).replace(/\{(\w+)\}/g, (_, k) => vars[k]);
const HOME = "/Users/xv";
const agents = [{ id: "claude", name: "Claude Code", icon: "", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude.json` }];

function server(lang, calls) {
  const lib = {
    dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
    agents,
    instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [],
    servers: [{ name: "order-tools", transport: "stdio", command: "/Users/me/order-mcp", args: ["--config", "mac.yaml"], env: { TOKEN: "${ORDER_TOKEN}" }, agents: ["claude"] },
      { name: "kept", transport: "stdio", command: "/opt/kept", agents: ["claude"], local: true }],
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
      calls.push(s);
      const i = lib.servers.findIndex((x) => x.name === old);
      lib.servers[i] = { ...lib.servers[i], ...s };
      if (!s.shared) delete lib.servers[i].shared;
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
  test(engine + ": a synced MCP server's own command on this computer", async (t) => {
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
          const OWN = "Use another command on this computer", SYNCED = "Use the synced command", HINT = "This computer's own, with its environment below. Your other computers run {cmd}";
          if (lang !== "en") for (const s of [OWN, SYNCED, HINT]) assert.ok(I18N[lang][s], `${lang}: ${s}`);
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
          const rowOf = (n) => page.locator("#view-library .lib-server", { has: page.locator(`.lib-health[data-server="${n}"]`) });
          const ed = page.locator("#modal .lib-editor");
          const line = ed.locator(".lib-cmd");
          const open = async (n) => { await rowOf(n).locator(".name").click(); await ed.waitFor(); };
          const save = async () => { await ed.locator(":scope > .bar button.primary").click(); await ed.waitFor({ state: "detached" }); };
          const synced = w(HINT, { cmd: "/Users/me/order-mcp --config mac.yaml" });

          await rowOf("order-tools").waitFor();
          // a synced command server offers its own command here
          await open("order-tools");
          await ed.getByRole("button", { name: w(OWN), exact: true }).click();
          await ed.locator(".hint", { hasText: synced }).waitFor();
          await line.fill("/opt/order-mcp --config /etc/ops.yaml");
          await save();
          assert.equal(calls.length, 1);
          assert.equal(calls[0].command, "/opt/order-mcp");
          assert.deepEqual(calls[0].args, ["--config", "/etc/ops.yaml"]);
          assert.deepEqual(calls[0].shared, { command: "/Users/me/order-mcp", args: ["--config", "mac.yaml"], env: { TOKEN: "${ORDER_TOKEN}" } });
          // opened again it says so, and saving keeps it
          await open("order-tools");
          await ed.locator(".hint", { hasText: synced }).waitFor();
          assert.equal(await line.inputValue(), "/opt/order-mcp --config /etc/ops.yaml");
          await save();
          assert.deepEqual(calls[1].shared, calls[0].shared);
          // back to the synced command
          await open("order-tools");
          await ed.getByRole("button", { name: w(SYNCED), exact: true }).click();
          assert.equal(await line.inputValue(), "/Users/me/order-mcp --config mac.yaml");
          assert.equal(await ed.locator(".hint", { hasText: synced }).count(), 0);
          await save();
          assert.equal(calls[2].shared, null);
          assert.equal(calls[2].command, "/Users/me/order-mcp");
          // a server kept to this computer has nothing synced to differ from
          await open("kept");
          assert.equal(await ed.getByRole("button", { name: w(OWN), exact: true }).count(), 0);
          await ed.locator(":scope > .bar button", { hasText: w("Cancel") }).click();
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
          await ctx.close();
        });
      }
    }
    assert.deepEqual(errors, []);
  });
}
