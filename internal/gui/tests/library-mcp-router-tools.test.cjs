// Run with Node's test runner and Playwright on the module path; see README.md.
// ArvinCharl on X: the MCP router's tools fill an agent's context. With
// the router on, the Library's MCP tab has a Tools on demand row under the
// router's name: a switch, on by default (settings' routerAllTools off),
// saying that an agent given more than 10 tools gets two in their place,
// and which agents find tools themselves. A click posts servers/hub-tools
// {on: true} (every tool whole), says so, and the switch reads off; a click
// again posts {on: false}. Off, there is no row. In every language, both
// engines, wide and narrow: no sideways scroll, the switch inside its row.
// No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/mg";
const fixture = (st) => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  mcpHub: st.on, hubPath: "/mcp/magpie/hub/", hubNote: "", hubName: "la-box", hubAllTools: st.all,
  agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude.json`, ...(st.on ? { hub: "on" } : {}) }],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [], problems: [],
  servers: [{ name: "fetch", transport: "stdio", command: "uvx", args: ["mcp-server-fetch"], agents: ["claude"] }],
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
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${st.web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", gateway: "http://127.0.0.1:3425", publicURL: "https://mac.tail0.ts.net" } });
    if (url.pathname === "/api/library") return json(fixture(st));
    if (url.pathname === "/api/library/servers/hub-tools") {
      const body = req.postDataJSON();
      st.sent.push(body);
      st.all = body.on;
      return json(fixture(st));
    }
    if (url.pathname === "/api/library/mcp/check") return json({ servers: Object.fromEntries(req.postDataJSON().names.map((n) => [n, { state: "ok", tools: 1 }])) });
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

const shot = async (page, name) => {
  if (!process.env.ARTIFACT_DIR) return;
  await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
  await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `router-tools-${name}.png`) });
};

const sideways = (page) => page.evaluate(() => {
  const over = [];
  const w = document.documentElement.clientWidth;
  if (document.documentElement.scrollWidth > w) over.push("page " + document.documentElement.scrollWidth);
  for (const e of document.querySelectorAll("#view-library .lib-hubtoolsrow, #view-library .lib-hubtoolsrow .lib-switch, #view-library .lib-hubtoolsrow .who")) {
    const r = e.getBoundingClientRect();
    if (r.width && (r.right > w + 0.5 || r.left < -0.5)) over.push(e.className + " " + Math.round(r.left) + "–" + Math.round(r.right));
  }
  const row = document.querySelector("#view-library .lib-hubtoolsrow")?.getBoundingClientRect();
  const f = document.querySelector("#view-library .lib-hubtoolsrow .lib-switch")?.getBoundingClientRect();
  if (row && f && (f.right > row.right + 0.5 || f.top < row.top - 0.5 || f.bottom > row.bottom + 0.5)) over.push("switch out of its row");
  return over;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the router's tools on demand", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    I18N = strings().I18N;
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());

    for (const width of [1100, 420]) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(`${lang} ${width}px`, async () => {
        const w = (key, vars) => tr(lang, key, vars);
        const st = { on: false, all: false, sent: [], web: width < 800 };
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
        await v.locator(".lib-hub").waitFor();
        // off: no row
        assert.equal(await v.locator(".lib-hubtoolsrow").count(), 0);
        await ctx.close();

        st.on = true;
        const ctx2 = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        await ctx2.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
        const p = await ctx2.newPage();
        p.setDefaultTimeout(5000);
        p.setDefaultNavigationTimeout(60000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.route("**/*", server(lang, st));
        await p.goto("http://magpie.test/");
        await p.locator('button[data-view="library"]').click();
        const row = p.locator("#view-library .lib-hubtoolsrow");
        await row.waitFor();
        // under the router's name
        const after = await p.evaluate(() => {
          const at = document.querySelector("#view-library .lib-hubnoterow").getBoundingClientRect();
          return document.querySelector("#view-library .lib-hubtoolsrow").getBoundingClientRect().top >= at.bottom - 0.5;
        });
        assert.ok(after, "the row isn't under the router's name");
        assert.equal((await row.locator(".name").textContent()).trim(), w("Tools on demand"));
        assert.equal((await row.locator(".sub").textContent()).trim(), w("An agent given more than 10 tools gets two in their place, one that finds a tool and one that runs it, so their definitions stay out of every request. Claude Code, Claude Desktop, Codex and Pi find tools themselves and are given every one."));
        const sw = () => p.locator("#view-library .lib-hubtoolsrow .lib-switch");
        assert.equal(await sw().getAttribute("aria-label"), w("Tools on demand"));
        assert.equal(await sw().getAttribute("aria-checked"), "true");
        assert.deepEqual(await sideways(p), [], "sideways scroll");
        await shot(p, `${engine}-${lang}-${width}-on`);

        // off: every tool whole
        await sw().click();
        await p.waitForFunction((x) => document.querySelector("#status")?.textContent === x, w("Agents are given every tool of the router — restart them to use it"));
        assert.deepEqual(st.sent, [{ on: true }]);
        await p.waitForFunction(() => document.querySelector("#view-library .lib-hubtoolsrow .lib-switch")?.getAttribute("aria-checked") === "false");
        // on again
        await sw().click();
        await p.waitForFunction((x) => document.querySelector("#status")?.textContent === x, w("Agents are given the router's tools on demand — restart them to use it"));
        assert.deepEqual(st.sent, [{ on: true }, { on: false }]);
        await p.waitForFunction(() => document.querySelector("#view-library .lib-hubtoolsrow .lib-switch")?.getAttribute("aria-checked") === "true");
        assert.deepEqual(await sideways(p), [], "sideways scroll, after");
        assert.deepEqual(errors, []);
        await ctx2.close();
      });
    }
  });
}
