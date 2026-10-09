// Run with Node's test runner and Playwright on the module path; see README.md.
// A plugin installed a moment ago has no subscriptions named for it yet: the
// Installed list is drawn from what the plugins said the last time magpie asked
// them, which a plugin installed since hasn't been asked about yet, and asking
// them means every plugin's vendor answering, one at a time.
//
// So an empty list is two different things: "not asked yet" — the row says
// nothing rather than claiming a plugin that does sign in to something signs in
// to nothing — and "asked, and it has none" (`named`), where the row says so.
// No backend here: the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/aimer";
const NEW = "opencode-brandnew-auth";
const EMPTY = "opencode-nothing-auth";
const OLD = "opencode-copilot-auth";
const claude = { id: "claude", name: "Claude Code", icon: "", skills: `${HOME}/.claude/skills`, mcp: `${HOME}/.claude/mcp.json` };
const lib = {
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [claude],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [], servers: [],
};

function server(lang, state) {
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: state.providers });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: state.plugins });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { signsIn: "Signs in to", nothing: "Signs in to nothing magpie can use" },
  zh: { signsIn: "可登录", nothing: "没有 magpie 能用的登录" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a plugin whose subscriptions are not named yet claims nothing", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const w = words[lang];
        const state = {
          // NEW was installed a moment ago, and the answer for it has not
          // come: its row must claim nothing. EMPTY has been asked about and
          // has none (`named`), so its row says so.
          plugins: {
            plugins: [
              { spec: OLD, providers: ["GitHub Copilot"], named: true, version: "0.1.0", moved: [] },
              // the not-asked row carries no `named` at all: the backend's
              // pluginEntryJSON is `named,omitempty`, so a false one is left
              // off the wire rather than sent as false
              { spec: NEW, providers: [], version: "0.1.0", moved: [] },
              { spec: EMPTY, providers: [], named: true, version: "0.1.0", moved: [] },
            ],
            bun: true, bunVersion: "1.3.0", picker: false, mirror: false,
          },
          providers: { providers: [], gateway: { running: true } },
        };
        const ctx = await browser.newContext({ viewport: { width: 980, height: 800 } });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.pluginTab", "installed"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, state));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="plugins"]').click();
        const v = page.locator("#view-plugins");
        await v.locator(".pm-row", { hasText: NEW }).waitFor();

        const notYet = await v.locator(".pm-row", { hasText: NEW }).innerText();
        assert(!notYet.includes(w.nothing), "a plugin not asked about yet claims nothing:\n" + notYet);
        // the one whose names have come still says what it signs in to
        const old = v.locator(".pm-row", { hasText: OLD });
        assert((await old.innerText()).includes(w.signsIn), "the known plugin still names its subscriptions:\n" + (await old.innerText()));
        // one that has been asked about and has none says so
        const empty = await v.locator(".pm-row", { hasText: EMPTY }).innerText();
        assert(empty.includes(w.nothing), "a plugin asked about with no subscriptions says so:\n" + empty);
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
