// Run with Node's test runner and Playwright on the module path; see README.md.
// The MCP router's code mode: with the router on, the Library's MCP tab
// has a Code mode row under Tools on demand, a switch on by default (the
// view's hubCode). A click posts servers/hub-code {on: false}, says so,
// and the switch reads off; a click again posts {on: true}. Off, there is
// no row. In every language, both engines, wide and narrow: no sideways
// scroll, the switch inside its row. No backend: the API is faked here.
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
  mcpHub: st.on, hubPath: "/mcp/magpie/hub/", hubNote: "", hubName: "la-box", hubAllTools: false, hubCode: st.code,
  hubOnDemandOver: OVER, hubFindsItself: FINDS,
  agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude.json`, ...(st.on ? { hub: "on" } : {}) }],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [], problems: [],
  servers: [{ name: "fetch", transport: "stdio", command: "uvx", args: ["mcp-server-fetch"], agents: ["claude"] }],
});

// the gateway's own threshold and agents (hubOnDemandOver, hubFindsItself
// in the library's view), not 10 and the four the row's words once named:
// the row says what it is given (11-18)
const OVER = 12;
const FINDS = ["Claude Code", "Claude Desktop", "Codex", "Pi", "Droid", "omp"];

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
    if (url.pathname === "/api/library/servers/hub-code") {
      const body = req.postDataJSON();
      st.sent.push(body);
      st.code = body.on;
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
  await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `router-code-${name}.png`) });
};

const sideways = (page) => page.evaluate(() => {
  const over = [];
  const w = document.documentElement.clientWidth;
  if (document.documentElement.scrollWidth > w) over.push("page " + document.documentElement.scrollWidth);
  for (const e of document.querySelectorAll("#view-library .lib-hubcoderow, #view-library .lib-hubcoderow .lib-switch, #view-library .lib-hubcoderow .who")) {
    const r = e.getBoundingClientRect();
    if (r.width && (r.right > w + 0.5 || r.left < -0.5)) over.push(e.className + " " + Math.round(r.left) + "–" + Math.round(r.right));
  }
  const row = document.querySelector("#view-library .lib-hubcoderow")?.getBoundingClientRect();
  const f = document.querySelector("#view-library .lib-hubcoderow .lib-switch")?.getBoundingClientRect();
  if (row && f && (f.right > row.right + 0.5 || f.top < row.top - 0.5 || f.bottom > row.bottom + 0.5)) over.push("switch out of its row");
  return over;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the router's code mode", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    I18N = strings().I18N;
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());

    for (const width of [1100, 420]) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(`${lang} ${width}px`, async () => {
        const w = (key, vars) => tr(lang, key, vars);
        const st = { on: false, code: true, sent: [], web: width < 800 };
        const errors = [];
        const open = async () => {
          const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
          await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
          const page = await ctx.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang, st));
          await page.goto("http://magpie.test/");
          await page.locator('button[data-view="library"]').click();
          await page.locator("#view-library .lib-hub").waitFor();
          return { ctx, page };
        };
        // the router off: no row
        let { ctx, page } = await open();
        assert.equal(await page.locator("#view-library .lib-hubcoderow").count(), 0);
        await ctx.close();

        st.on = true;
        ({ ctx, page } = await open());
        const row = page.locator("#view-library .lib-hubcoderow");
        await row.waitFor();
        // under Tools on demand
        const after = await page.evaluate(() => {
          const at = document.querySelector("#view-library .lib-hubtoolsrow").getBoundingClientRect();
          return document.querySelector("#view-library .lib-hubcoderow").getBoundingClientRect().top >= at.bottom - 0.5;
        });
        assert.ok(after, "the row isn't under Tools on demand");
        assert.equal((await row.locator(".name").textContent()).trim(), w("Code mode"));
        assert.equal((await row.locator(".sub").textContent()).trim(), w("Agents can run a short script that calls several tools in one step, and only its result reaches the model."));
        const sw = () => page.locator("#view-library .lib-hubcoderow .lib-switch");
        assert.equal(await sw().getAttribute("aria-label"), w("Code mode"));
        assert.equal(await sw().getAttribute("aria-checked"), "true");
        assert.deepEqual(await sideways(page), [], "sideways scroll");
        await shot(page, `${engine}-${lang}-${width}-on`);

        await sw().click();
        await page.waitForFunction((x) => document.querySelector("#status")?.textContent === x, w("Agents no longer run code with the router's tools — restart them to use it"));
        assert.deepEqual(st.sent, [{ on: false }]);
        await page.waitForFunction(() => document.querySelector("#view-library .lib-hubcoderow .lib-switch")?.getAttribute("aria-checked") === "false");
        await sw().click();
        await page.waitForFunction((x) => document.querySelector("#status")?.textContent === x, w("Agents can run code with the router's tools — restart them to use it"));
        assert.deepEqual(st.sent, [{ on: false }, { on: true }]);
        await page.waitForFunction(() => document.querySelector("#view-library .lib-hubcoderow .lib-switch")?.getAttribute("aria-checked") === "true");
        assert.deepEqual(await sideways(page), [], "sideways scroll, after");
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}
