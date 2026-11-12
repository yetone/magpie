// Run with Node's test runner and Playwright on the module path; see README.md.
// Magpie Memory has a mark of its own, a brain (the owner, 11-12: the
// Library's Memory tab had a bookmark, and Memory's own requests the
// generic cube in Routing). The sidebar's Memory tab and every request
// Memory makes (client "memory", agent.others) draw the one brain in
// icons/memory.svg. TestEveryClientHasNameAndIcon holds the client to
// the Go table. Chromium and WebKit, every language, 1100 and 420px (the
// sidebar is there at 1100 only). No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/aimer";
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
// as agent.Clients() gives them
const clients = [
  { id: "claude", name: "Claude Code", icon: "claude-color" },
  { id: "memory", name: "Memory", icon: "memory" },
];
const routes = clients.map((c, i) => ({
  id: 100 + i, seq: 100 + i, time: at(i), agent: c.id, model: "claude-haiku-4-5", provider: "anthropic",
  order: [], tries: [], done: true, status: 200, ms: 900, tokens: 100,
}));
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const lib = {
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code")],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [], servers: [],
};

function serve(lang) {
  const state = { agents: [], clients, profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") {
      const d = url.searchParams.get("day");
      return json({ cut: false, days: [{ day, requests: routes.length }], routes: d ? routes : [] });
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { url: "http://127.0.0.1:3425", running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/library") return json(lib);
    if (url.pathname === "/api/memory") return json({ memories: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: Memory is drawn with its brain`, async (t) => {
    // the brain, as icons/memory.svg has it
    const svgText = await fs.readFile(path.join(assets, "icons/memory.svg"), "utf8").catch(() => "");
    const brain = (svgText.match(/<path d="([^"]+)"/) || [])[1];
    assert.ok(brain, "icons/memory.svg has a path");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [1100, 420]) await t.test(`${lang} ${width}px`, async () => {
      const context = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
      t.after(() => context.close());
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));

      if (width === 1100) {
        // the sidebar's Memory tab: the brain, and no other tab has it
        await page.goto("http://magpie.test/?view=library");
        await page.waitForSelector("body.side-nav");
        await page.waitForFunction(() => window.NAV_SUB_IC && document.querySelectorAll('.nav-sub[data-view="library"]:not(.leaving) .nav-s').length >= 8);
        const tabs = await page.evaluate(() => [...document.querySelectorAll('.nav-sub[data-view="library"]:not(.leaving) .nav-s')].map((s) => ({
          text: s.querySelector(":scope > span")?.textContent, d: s.querySelector(":scope > svg path")?.getAttribute("d") || "",
        })));
        const memName = await page.evaluate(() => document.querySelector('[data-seg="memory"]')?.textContent.trim());
        assert.ok(memName, "Library has a Memory tab");
        const mem = tabs.find((x) => x.text === memName);
        assert.ok(mem, `${memName} in ${JSON.stringify(tabs.map((x) => x.text))}`);
        assert.equal(mem.d, brain, `${memName}'s mark is the brain`);
        assert.equal(tabs.filter((x) => x.d === brain).length, 1, "only Memory has the brain");
      }

      // a request Memory made: its brain and its name, never the cube
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-days .rt-day").nth(1).click();
      await page.locator(".rt-req").nth(clients.length - 1).waitFor();
      // the mask's picture has come, or failed (a failed one is the cube)
      await page.evaluate(() => new Promise((r) => { const i = new Image(); i.onload = i.onerror = r; i.src = "icons/memory.svg"; }));
      await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => setTimeout(r))));
      const row = await page.locator(".rt-req").evaluateAll((bs) => bs.map((b) => {
        const ic = b.querySelector(".asked .ic");
        return { title: b.title, icon: ic?.dataset.icon, generic: !!ic?.classList.contains("generic"), mask: !!ic?.querySelector(".mask") };
      }).find((x) => x.title.startsWith("Memory · ")));
      assert.ok(row, "Memory's request is listed by its name");
      assert.equal(row.icon, "memory", JSON.stringify(row));
      assert.ok(!row.generic && row.mask, `drawn with icons/memory.svg, not the cube: ${JSON.stringify(row)}`);
      assert.deepEqual(errors, []);
    });
  });
}
