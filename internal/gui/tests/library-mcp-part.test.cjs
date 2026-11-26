// Run with Node's test runner and Playwright on the module path; see README.md.
// zysicyj on X (Windows): ticking agents on a server in Library → MCP said
// "Saving…" for about a minute. A change to the servers alone answers with
// the servers' part of the page ({part: "mcp", mcp: {...}}), read without
// the skills: the page takes it and keeps the rest it has, the skills and
// the instructions. A read of the whole page that was on its way when the
// part came keeps the part, which is newer, and takes the rest. Chromium
// and WebKit, every language; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/zy";
const agents = [
  { id: "claude", name: "Claude Code", icon: "claudecode-color", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude.json` },
  { id: "codex", name: "Codex", icon: "openai", skills: `${HOME}/.codex/skills`, mcp: `${HOME}/.codex/config.toml` },
];
const servers = (on) => [{ name: "notes", transport: "stdio", command: "uvx", args: ["notes-mcp"], agents: on }];
const whole = (st) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents, servers: servers(st.on), foundServers: [], projects: [], plugins: [], problems: [],
  instructions: { shared: "Be brief.", sets: [{ id: "default", name: "", active: true, text: "Be brief." }], agents: [] },
  skills: st.skills.map((name) => ({ name, kind: "folder", description: "d", source: `${HOME}/src/${name}`, agents: ["claude"] })),
  foundSkills: [],
});

function server(lang, st) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") {
      st.reads++;
      // the read is answered as the library was when it was asked
      const v = whole(st);
      if (st.holdRead) await new Promise((go) => { st.letRead = go; });
      return json(v);
    }
    if (url.pathname === "/api/library/servers/agents") {
      const body = req.postDataJSON();
      st.sent.push(body);
      st.on = body.agents;
      // the servers' part alone: no skills, no instructions
      return json({ part: "mcp", mcp: { agents, servers: servers(st.on), foundServers: [], projects: [], plugins: [], mcpHub: false }, problems: [], result: { changed: ["codex"] } });
    }
    if (url.pathname === "/api/library/mcp/check") return json({ servers: {} });
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

const chip = (page, id) => page.locator(`#view-library .lib-server[data-server="notes"] [data-agent="${id}"]`);
// the tab's own button: at a wide window the side nav shows it in its place
const openTab = (page, id) => page.evaluate((id) => document.querySelector(`#view-library .lib-tabs [data-seg="${id}"]`).click(), id);
const pressed = (page, id) => chip(page, id).getAttribute("aria-pressed");

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a server change's answer is the servers' part, and the page keeps the rest", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(lang, async () => {
        const st = { on: ["claude"], skills: ["pdf", "slides"], sent: [], reads: 0 };
        const errors = [];
        const ctx = await browser.newContext({ viewport: { width: 1100, height: 900 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, st));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        await chip(page, "codex").waitFor();
        await page.waitForFunction(() => !document.querySelector("#view-library .lib-loading"));
        assert.equal(await pressed(page, "codex"), "false");

        // ticked: the part comes back, and nothing reads the page again
        const reads = st.reads;
        const answered = page.waitForResponse((r) => r.url().endsWith("/servers/agents"));
        await chip(page, "codex").click();
        await answered;
        await page.waitForFunction(() => document.querySelector('#view-library .lib-server[data-server="notes"] [data-agent="codex"]')?.getAttribute("aria-pressed") === "true");
        assert.deepEqual(st.sent, [{ name: "notes", agents: ["claude", "codex"] }]);
        assert.equal(st.reads, reads, "the page was read again after a server change");

        // the skills the page had are still there
        await openTab(page, "skills");
        await page.locator("#view-library .lib-skill").first().waitFor();
        assert.equal(await page.locator("#view-library .lib-skill").count(), 2, "the skills went with the servers' answer");

        // a whole read on its way when a part comes: the part is newer
        await openTab(page, "mcp");
        await chip(page, "codex").waitFor();
        st.holdRead = true;
        st.skills = ["pdf", "slides", "video"];
        // coming back to the window reads the page (quietLoad)
        await page.evaluate(() => window.dispatchEvent(new Event("focus")));
        for (let i = 0; i < 100 && !st.letRead; i++) await page.waitForTimeout(20);
        assert.ok(st.letRead, "no read of the page was asked");
        st.holdRead = false;
        const off = page.waitForResponse((r) => r.url().endsWith("/servers/agents"));
        await chip(page, "codex").click();
        await off;
        await page.waitForFunction(() => document.querySelector('#view-library .lib-server[data-server="notes"] [data-agent="codex"]')?.getAttribute("aria-pressed") === "false");
        // the read, asked while codex was still ticked, comes back now
        const done = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/library");
        st.letRead();
        await done;
        await page.waitForTimeout(100);
        assert.equal(await pressed(page, "codex"), "false", "a read older than the change put codex back");
        await openTab(page, "skills");
        await page.waitForFunction(() => document.querySelectorAll("#view-library .lib-skill").length === 3);

        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
