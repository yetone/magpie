// Run with Node's test runner and Playwright on the module path; see README.md.
// bitxeno303 on X: magpie in Docker, DeepSeek Harness on their own
// computer at /mcp/magpie/hub/dsh, and the Library had no dsh to tick a
// server for. An agent on another computer that connected to the router
// (remote_agents.go, AgentView.remote) is a chip on each server row, in
// gateway mode and with no agent here alike, its tip saying where it is;
// a click posts servers/agents with it, a server kept direct is blocked
// for it, and By agent lists it. This computer's own agents stay unshown
// in gateway mode, in By agent too, and the tabs stay Instructions, MCP
// and Skills. In every language, both engines, at
// 1100px (no agents here) and 420px (gateway mode): no sideways scroll.
// No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/srv/mg";
const DSH = { id: "dsh", name: "DeepSeek Harness", icon: "deepseek-color", mcp: "/mcp/magpie/hub/dsh", remote: true, hub: "on" };
const fixture = (st) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  mcpHub: true, hubPath: "/mcp/magpie/hub/",
  // in gateway mode this computer's agents are there, and not shown
  agents: [...(st.gateway ? [{ id: "claude", name: "Claude Code", icon: "claudecode-color", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude.json`, hub: "on" }] : []), DSH],
  instructions: { agents: [], sets: [] }, foundSkills: [], skills: [], problems: [], foundServers: [], projects: [],
  servers: [
    { name: "notes", transport: "stdio", command: "uvx", args: ["notes-mcp"], agents: st.notes },
    { name: "web", transport: "http", url: "https://mcp.example.net/web", agents: st.web },
    { name: "kept", transport: "stdio", command: "kept-mcp", hub: "direct", agents: st.gateway ? ["claude"] : [] },
  ],
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

function server(lang, st) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${st.gateway}${st.gateway ? ",gateway:true" : ""}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", gateway: "http://127.0.0.1:3425", publicURL: "https://mcp.example.net" } });
    if (url.pathname === "/api/library") return json(fixture(st));
    if (url.pathname === "/api/library/servers/agents") {
      const body = req.postDataJSON();
      st.sent.push(body);
      if (body.name === "notes" || body.name === "web") st[body.name] = body.agents;
      return json({ ...fixture(st), result: { changed: [] } });
    }
    if (url.pathname === "/api/library/mcp/check") {
      const names = req.postDataJSON().names;
      return json({ servers: Object.fromEntries(names.map((n) => [n, { state: "ok", tools: 2 }])) });
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const sideways = (page) => page.evaluate(() => {
  const over = [];
  const w = document.documentElement.clientWidth;
  if (document.documentElement.scrollWidth > w) over.push("page " + document.documentElement.scrollWidth);
  for (const e of document.querySelectorAll("#view-library .lib-server, #view-library .lib-server .lib-agents, #view-library .lib-ag, #modal .lib-byagent-row, #modal .lib-byagent-row button")) {
    const r = e.getBoundingClientRect();
    if (r.width && (r.right > w + 0.5 || r.left < -0.5)) over.push((e.className || e.tagName) + " " + Math.round(r.left) + "–" + Math.round(r.right));
  }
  return over;
});
const press = async (page, loc) => {
  const b = await loc.boundingBox();
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an agent on another computer in the Library", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    I18N = strings().I18N;
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());

    for (const width of [1100, 420]) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(`${lang} ${width}px`, async () => {
        const w = (key, vars) => tr(lang, key, vars);
        const gateway = width < 800;
        const where = gateway ? "gateway mode" : "no agents here";
        const st = { gateway, notes: ["dsh"], web: [], sent: [] };
        const errors = [];
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, st));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        await v.locator(".lib-server").first().waitFor();

        // still the page of a magpie with no agent of its own
        assert.deepEqual((await v.locator(".lib-tabs .opt").allTextContents()).map((x) => x.trim().replace(/ · \d+$/, "")), [w("Instructions"), w("MCP servers"), w("Skills")], `${where}: the tabs`);
        assert.equal(await v.locator('.lib-ag[data-agent="claude"]').count(), 0, `${where}: this computer's agent is shown`);

        // its chip on each row, lit where it has the server
        const rowOf = (name) => v.locator(`.lib-server[data-server="${name}"]`);
        for (const name of ["notes", "web", "kept"]) assert.equal(await rowOf(name).locator('.lib-ag[data-agent="dsh"]').count(), 1, `${where}: no dsh chip on ${name}`);
        assert.equal(await rowOf("notes").locator('.lib-ag[data-agent="dsh"]').getAttribute("aria-pressed"), "true", `${where}: notes isn't lit for dsh`);
        assert.equal(await rowOf("web").locator('.lib-ag[data-agent="dsh"]').getAttribute("aria-pressed"), "false", where);
        const tip = await rowOf("web").locator('.lib-ag[data-agent="dsh"]').getAttribute("title");
        assert.ok(tip.endsWith(w("{agent} connects to the router from another computer", { agent: "DeepSeek Harness" })), `${where}: the tip ${JSON.stringify(tip)}`);
        // a server kept direct doesn't go through the router: blocked
        const kept = rowOf("kept").locator('.lib-ag[data-agent="dsh"]');
        assert.equal(await kept.getAttribute("aria-disabled"), "true", `${where}: kept isn't blocked for dsh`);
        assert.equal(await kept.getAttribute("title"), w("{agent} is on another computer: it reaches only the servers that go through the router", { agent: "DeepSeek Harness" }));
        assert.deepEqual(await sideways(page), [], `${where}: sideways scroll`);

        // a click gives it the server
        // Playwright's click: it scrolls the Library's own scroller and
        // waits for the chip to take the click
        await rowOf("web").locator('.lib-ag[data-agent="dsh"]').click();
        await page.waitForFunction(() => document.querySelector('#view-library .lib-ag.on[data-agent="dsh"]') && document.querySelectorAll('#view-library .lib-ag.on[data-agent="dsh"]').length === 2);
        assert.deepEqual(st.sent.at(-1), { name: "web", agents: ["dsh"] }, `${where}: what was posted`);

        // By agent lists it
        await press(page, v.locator(".lib-serverhead .lib-barmore"));
        const by = page.locator(".row-menu .rm-item.lib-byagent");
        await by.waitFor();
        await press(page, by);
        const sheet = page.locator("#modal .lib-byagent-sheet");
        await sheet.waitFor();
        await page.waitForFunction(() => !document.querySelector("#modal").getAnimations({ subtree: true }).length);
        assert.deepEqual(await sheet.locator(".lib-byagent-row").evaluateAll((rs) => rs.map((r) => r.dataset.agent)), ["dsh"], `${where}: By agent, this computer's agents unshown in gateway mode`);
        assert.equal(await sheet.locator('.lib-byagent-row[data-agent="dsh"] button.lib-agenton').isDisabled(), true, `${where}: dsh has every server it can reach`);
        assert.deepEqual(await sideways(page), [], `${where}: sideways scroll, By agent`);
        assert.equal(await page.locator("select").count(), 0);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
