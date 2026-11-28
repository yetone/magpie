// Run with Node's test runner and Playwright on the module path; see README.md.
// gandli on X: with many skills the Library felt slow. The Skills tab drew
// every skill's row before its first frame: its list, set before the page
// was in the window, took its first look at the view for a scroll that had
// outrun the filling, and drew them all at once (150 rows, ~7,300 nodes,
// half a second in WebKit). Now the tab draws the rows about the view, the
// rest come in while the page is idle, and a scroll past them still draws
// what's left at once. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/aimer";
const N = 150;
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills`, mcp: `${HOME}/.${id}/mcp.json` });
const lib = {
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code"), agent("codex", "Codex"), agent("pi", "Pi")],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [],
  skills: Array.from({ length: N }, (_, i) => ({
    name: `skill-${String(i).padStart(3, "0")}`, description: `Skill number ${i}`, kind: "folder",
    agents: ["claude", "codex"], source: `${HOME}/skills/skill-${i}`,
  })),
  servers: [{ name: "fs", transport: "stdio", command: "npx", args: ["fs-mcp"], agents: ["codex"] }],
};

async function serve(route) {
  const url = new URL(route.request().url());
  if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:true};` });
  if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
  if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang: "en", theme: "light" } } });
  if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
  if (url.pathname === "/api/library") return route.fulfill({ json: lib });
  if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
  if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
  if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
  if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
  const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
  await route.fulfill({ body: await fs.readFile(file), contentType });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [980, 420]) {
    test(`${engine} ${width}px: the Skills tab draws the rows about the view first`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const ctx = await browser.newContext({ viewport: { width, height: 800 } });
      await ctx.addInitScript(() => { try { if (!sessionStorage.getItem("seeded")) { localStorage.setItem("magpie.libTab", "mcp"); sessionStorage.setItem("seeded", "1"); } } catch {} });
      const page = await ctx.newPage();
      page.setDefaultTimeout(10000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve);
      await page.goto("http://magpie.test/");
      await page.locator('button[data-view="library"]').click();
      const v = page.locator("#view-library");
      // narrow, the tabs are in the Library's sections menu: clicked here
      // as the page's own buttons, seen or not
      await v.locator('.lib-tabs .opt[data-seg="skills"]').waitFor({ state: "attached" });
      await v.locator(".lib-head .lib-more:not([disabled])").waitFor();

      const rows = () => page.evaluate(() => document.querySelectorAll("#view-library .lib-vl > .row").length);
      // what the click drew before the browser had a frame to paint
      const drawn = await page.evaluate(() => {
        document.querySelector('#view-library .lib-tabs .opt[data-seg="skills"]').click();
        return document.querySelectorAll("#view-library .lib-vl > .row").length;
      });
      assert.ok(drawn > 0, "the rows in view are drawn with the tab");
      assert.ok(drawn < 60, `the tab drew ${drawn} of ${N} rows before its first frame; want those about the view`);
      await v.getByText("skill-000", { exact: true }).waitFor();
      // the rest while the page is idle
      await page.waitForFunction((n) => document.querySelectorAll("#view-library .lib-vl > .row").length === n, N);

      // back to the tab and straight to its foot: the scroll outruns the
      // filling, and what's left is drawn at once
      await page.evaluate(() => document.querySelector('#view-library .lib-tabs .opt[data-seg="mcp"]').click());
      await v.locator(".lib-vl").waitFor({ state: "detached" });
      await page.evaluate(() => {
        document.querySelector('#view-library .lib-tabs .opt[data-seg="skills"]').click();
      });
      assert.ok(await rows() < 60, "drawn again about the view");
      await page.evaluate(() => { const m = document.querySelector("#view-library"); m.scrollTop = m.scrollHeight; });
      const last = v.getByText(`skill-${N - 1}`, { exact: true });
      await last.waitFor();
      await page.waitForFunction(() => new Promise((r) => requestAnimationFrame(() => r(true))));
      const box = await last.boundingBox();
      assert.ok(box && box.y >= 0 && box.y < 800, "the last skill is drawn where the scroll took the view");
      assert.deepEqual(errors, []);
    });
  }
}
