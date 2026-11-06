// Run with Node's test runner and Playwright on the module path; see README.md.
// treeman_26897 on Discord: magpie run as an MCP entry point for remote
// clients has no agents, and the MCP router could only be turned on with
// one to write to: the Library said "No agents to write to", and in
// gateway mode (magpie web on such a server) it wasn't there at all.
// With no agent, or in gateway mode, the Library is its MCP tab alone: the
// servers with no agent chips, nothing from agents' own configs, and the
// router's switch, there before the first server too. Its lines say any
// app connects at the address; a click posts servers/hub on, says the
// router is on, counts what it serves and shows the address, and Connect…
// has Other apps alone, with the gateway key a remote app needs. In every
// language, both engines, the app with no agents at 1100px and a browser
// in gateway mode at 420px: no sideways scroll, no native select.
// No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/srv/mg";
const fixture = (st) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  mcpHub: st.on, hubPath: "/mcp/magpie/hub/",
  // in gateway mode this computer's agents are there, and not shown
  agents: st.gateway ? [{ id: "claude", name: "Claude Code", icon: "claudecode-color", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude.json`, ...(st.on ? { hub: "on" } : {}) }] : [],
  instructions: { agents: [], sets: [] }, foundSkills: [], skills: [], problems: [],
  foundServers: st.gateway ? [{ name: "agent-own", transport: "stdio", command: "npx", args: ["agent-own"], agents: ["claude"] }] : [],
  projects: st.gateway ? [{ path: `${HOME}/code/app`, servers: { notes: ["claude"] } }] : [],
  servers: st.none ? [] : [{ name: "notes", transport: "stdio", command: "uvx", args: ["notes-mcp"], agents: st.gateway ? ["claude"] : [] }],
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
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${st.web}${st.gateway ? ",gateway:true" : ""}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", gateway: "http://127.0.0.1:3425", publicURL: "https://mcp.example.net" } });
    if (url.pathname === "/api/library") return json(fixture(st));
    if (url.pathname === "/api/library/servers/hub") {
      const body = req.postDataJSON();
      st.sent.push(body);
      st.on = body.on;
      return json({ ...fixture(st), result: { changed: [] } });
    }
    if (url.pathname === "/api/library/mcp/check") {
      const names = req.postDataJSON().names;
      st.checked.push(...names);
      return json({ servers: Object.fromEntries(names.map((n) => [n, { state: "ok", tools: 2 }])) });
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

const sideways = (page) => page.evaluate(() => {
  const over = [];
  const w = document.documentElement.clientWidth;
  if (document.documentElement.scrollWidth > w) over.push("page " + document.documentElement.scrollWidth);
  for (const e of document.querySelectorAll("#view-library .lib-hub, #view-library .lib-hub .lib-switch, #view-library .lib-server, #view-library .lib-hubat, #view-library .lib-hubat .lib-hubconnect, .lib-hubsheet, .lib-hubsheet .lib-hubapp, .lib-hubsheet .lib-hubaddr, .lib-hubsheet .bar button")) {
    const r = e.getBoundingClientRect();
    if (r.width && (r.right > w + 0.5 || r.left < -0.5)) over.push((e.className || e.tagName) + " " + Math.round(r.left) + "–" + Math.round(r.right));
  }
  return over;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the MCP router with no agent", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    I18N = strings().I18N;
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());

    for (const width of [1100, 420]) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(`${lang} ${width}px`, async () => {
        const w = (key, vars) => tr(lang, key, vars);
        // the app with no agents; a browser in gateway mode, its agent not shown
        const web = width < 800;
        const st = { on: false, sent: [], checked: [], web, gateway: web, none: false };
        const where = web ? "gateway mode" : "no agents";
        const url = (web ? "https://mcp.example.net" : "http://127.0.0.1:3425") + "/mcp/magpie/hub";
        const errors = [];
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        // the Skills tab last left: there is only MCP now
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, st));
        await page.goto("http://magpie.test/");
        // the Library is a page in gateway mode too
        const nav = page.locator('button[data-view="library"]');
        assert.ok(await nav.isVisible(), `${where}: no Library tab`);
        await nav.click();
        const v = page.locator("#view-library");
        const hub = v.locator(".lib-hub");
        await hub.waitFor();

        // the MCP tab alone, and nothing of agents
        assert.deepEqual((await v.locator(".lib-tabs .opt").allTextContents()).map((x) => x.trim().replace(/ · \d+$/, "")), [w("MCP servers")], `${where}: the tabs`);
        assert.equal((await v.locator(".lib-intro").first().textContent()).trim(), w("Add the servers other apps should reach, and turn on the MCP router: they connect at its address, from another computer with a gateway key."), where);
        assert.equal(await v.locator(".lib-server").count(), 1, where);
        assert.equal(await v.locator(".lib-ag, .lib-agents").count(), 0, `${where}: agent chips`);
        const text = await v.textContent();
        for (const gone of [w("In your agents"), w("In projects"), "Claude Code", "agent-own"]) assert.ok(!text.includes(gone), `${where}: ${gone} is shown`);
        assert.equal(await page.locator("select").count(), 0);

        // off: what it would do
        assert.equal((await hub.locator(".name").textContent()).trim(), w("MCP router"));
        assert.equal((await hub.locator(".sub.wraps").textContent()).trim(), w("Serve every server in the library at one address, for any app to connect to."));
        const sw = hub.locator(".lib-switch");
        assert.equal(await sw.getAttribute("aria-checked"), "false");
        assert.equal(await v.locator(".lib-hubat").count(), 0);
        assert.deepEqual(await sideways(page), [], `${where}: sideways scroll, off`);

        // on: the router is on, what it serves, and its address
        await sw.click();
        await page.waitForFunction((x) => document.querySelector("#status")?.textContent === x, w("The MCP router is on: apps can connect at its address"));
        assert.deepEqual(st.sent, [{ on: true }]);
        await page.waitForFunction(() => document.querySelector("#view-library .lib-hub .lib-switch")?.getAttribute("aria-checked") === "true");
        assert.equal((await v.locator(".lib-hub .sub.wraps").textContent()).trim(), w("Any app connects at the address below for every server in the library, as server__tool."));
        await page.waitForFunction((x) => document.querySelector("#view-library .lib-hubstats")?.textContent === x, [w("1 server"), w("{n} tools", { n: 2 })].join(" · "));
        assert.ok(st.checked.includes("notes"), `${where}: the router's server isn't checked`);
        assert.equal((await v.locator(".lib-hubURL").textContent()).trim(), url);
        assert.deepEqual(await sideways(page), [], `${where}: sideways scroll, on`);

        // Connect…: other apps alone, with the gateway key
        await v.locator(".lib-hubconnect").click();
        const sheet = page.locator(".lib-hubsheet");
        await sheet.waitFor();
        assert.equal(await sheet.locator(".ebody > .segs").count(), 0, `${where}: the sheet has tabs`);
        assert.equal(await sheet.locator(".lib-hubagent").count(), 0);
        assert.equal((await sheet.locator(".lib-hubaddr code").textContent()).trim(), url);
        assert.ok(await sheet.locator(".lib-hubapp").count() >= 3);
        assert.equal((await sheet.locator(".lib-hubcap").textContent()).trim(), w("From another computer, use the address magpie is reached at there, with a gateway key: Authorization: Bearer <key>."));
        assert.deepEqual(await sideways(page), [], `${where}: sideways scroll, sheet`);
        await sheet.locator(".bar button.primary").click();
        await sheet.waitFor({ state: "hidden" });

        // off again
        await v.locator(".lib-hub .lib-switch").click();
        await page.waitForFunction((x) => document.querySelector("#status")?.textContent === x, w("The MCP router is off"));
        assert.deepEqual(st.sent, [{ on: true }, { on: false }]);

        // no server yet: the switch is there to turn on first
        st.none = true;
        await page.reload();
        await page.locator('button[data-view="library"]').click();
        await v.locator(".lib-hub").waitFor();
        assert.equal(await v.locator(".lib-server").count(), 0);
        assert.ok((await v.textContent()).includes(w("Paste a server's JSON from its README, or fill it in.")), where);
        assert.equal(await v.locator(".lib-hub .lib-switch").getAttribute("aria-checked"), "false");
        assert.deepEqual(await sideways(page), [], `${where}: sideways scroll, no server`);

        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
