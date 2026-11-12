// Run with Node's test runner and Playwright on the module path; see README.md.
// An agent's profiles (hermes#coder, hermes#work, ...) are one chip in a
// row, not an icon each (Yanzhongixuy on X: twenty Hermes glyphs in every
// skill row). The chip opens an app menu of the profiles, ticked as the row
// has them, and a tick writes the whole list. Agents with fewer than three
// entries keep their own chips. English, Chinese, Traditional Chinese,
// Japanese and German; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/me";
const agent = (id, name, icon) => ({ id, name, icon, skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const AGENTS = [agent("claude", "Claude Code", "claudecode-color"), agent("hermes", "Hermes Agent", "hermes"),
  ...["coder", "work", "play", "ops", "docs"].map((n) => agent("hermes#" + n, "Hermes · " + n, "hermes")),
  agent("omp", "omp", "omp"), agent("omp#a", "omp · a", "omp")];
const ALL = AGENTS.map((a) => a.id);

const sent = [];
function server(lang, lib) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/library") return json(lib);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/library/servers/agents") { sent.push(route.request().postDataJSON()); lib.servers[0].agents = route.request().postDataJSON().agents; return json(lib); }
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}


const lib = () => ({
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: structuredClone(AGENTS),
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], problems: [], skills: [],
  servers: [{ name: "context7", transport: "stdio", command: "npx", args: ["-y", "@upstash/context7-mcp"], agents: ["claude", "hermes", "hermes#work"] }],
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an agent's profiles are one chip with a menu", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(lang, async () => {
        sent.length = 0;
        const ctx = await browser.newContext({ viewport: { width: 1000, height: 700 } });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, lib()));
        try {
          await page.goto("http://magpie.test/");
          await page.locator('button[data-view="library"]').click();
          const strip = page.locator("#view-library .lib-row > .lib-agents").first();
          await strip.locator(".lib-ag.profiles").waitFor();
          // claude, one Hermes chip, omp and omp#a as they were: 4 chips (+ All)
          assert.equal(await strip.locator(".lib-ag[data-agent]").count(), 3);
          assert.equal(await strip.locator(".lib-ag.profiles").count(), 1);
          assert.equal(await strip.locator('.lib-ag[data-agent^="hermes"]').count(), 0);
          const chip = strip.locator(".lib-ag.profiles");
          assert.equal(await chip.locator(".cnt").textContent(), "2");
          assert(await chip.evaluate((c) => c.classList.contains("on") && c.classList.contains("some")));
          await chip.click();
          const menu = page.locator(".proto-menu");
          await menu.waitFor();
          // the heading ticks all; then each of the six
          assert.equal(await menu.locator(".pm-item").count(), 7);
          assert.equal(await menu.locator('.pm-item.on').count(), 2);
          await menu.locator(".pm-item", { hasText: "coder" }).click();
          await page.waitForFunction(() => document.querySelector(".lib-ag.profiles .cnt")?.textContent === "3");
          await page.waitForFunction(() => true);
          await page.waitForTimeout(100);
          assert.deepEqual([...sent.at(-1).agents].sort(), ["claude", "hermes", "hermes#coder", "hermes#work"].sort());
          // the heading takes them all
          await menu.locator(".pm-group").click();
          await page.waitForFunction(() => document.querySelector(".lib-ag.profiles .cnt")?.textContent === "6");
          await page.waitForTimeout(100);
          assert.deepEqual([...sent.at(-1).agents].sort(), ["claude", "hermes", "hermes#coder", "hermes#docs", "hermes#ops", "hermes#play", "hermes#work"].sort());
          assert(!(await chip.evaluate((c) => c.classList.contains("some"))));
          assert.deepEqual(errors, []);
        } finally {
          await ctx.close();
        }
      });
    }
  });
}
