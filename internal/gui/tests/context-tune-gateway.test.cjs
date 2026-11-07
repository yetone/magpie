// Run with Node's test runner and Playwright on the module path; see README.md.
// linkaww on Discord, a magpie in Docker in gateway mode: Usage › Context's
// Tuned for you said Claude Code made 0 calls, while Through magpie below
// counted its requests. Tuned for you replays this computer's agents'
// session files, and in gateway mode the agents are on other computers:
// the page leaves the section out, from the first paint (bootPrefs) and
// when /api/tune says so (gateway: true), and draws it as before with
// gateway mode off. Chromium and WebKit, English and Chinese, 900 and 420.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets"));
const cacheRow = { knob: "cache_ttl", setting: { value: "1h", source: "settings" }, few: true };
const cases = {
  "gateway mode": { boot: true, tune: { days: 7, agents: [], gateway: true }, shown: false },
  // turned on by Settings after the page loaded: the server says so
  "gateway mode, said by the server": { boot: false, tune: { days: 7, agents: [], gateway: true }, shown: false },
  "gateway mode off": { boot: false, tune: { days: 7, agents: [{ agent: "claude", calls: 0, advice: [cacheRow] }] }, shown: true },
};

function serve(lang, { boot, tune }) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: true, ...(boot ? { gateway: true } : {}) })};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/tune") return json(tune);
    if (url.pathname === "/api/context") return json({ days: 7, agents: [], sessions: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType, status: 200 });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [name, c] of Object.entries(cases)) test(`${engine}, ${name}: Tuned for you ${c.shown ? "is drawn" : "is left out"}`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) for (const [width, height] of [[900, 760], [420, 760]]) {
      await t.test(`${lang}, ${width}x${height}`, async () => {
        const ctx = await browser.newContext({ viewport: { width, height }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.usageTab", "context"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, c));
        await page.goto("http://magpie.test/?view=usage");
        // Through magpie is drawn
        await page.locator("#contextPane .ctx-none").waitFor();
        // Tuned for you as /api/tune's answer has it: waited for, as the
        // section is drawn while it is asked
        const want = c.shown ? 1 : 0;
        await page.waitForFunction((n) => document.querySelectorAll("#contextPane .ctx-tune-note").length === n &&
          document.querySelectorAll("#contextPane .ctx-tune").length === n, want).catch(() => {});
        assert.deepEqual(errors, []);
        assert.equal(await page.locator("#contextPane .ctx-tune-note").count(), want);
        assert.equal(await page.locator("#contextPane .ctx-tune").count(), want);
        await ctx.close();
      });
    }
  });
}
