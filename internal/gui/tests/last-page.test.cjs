// Run with Node's test runner and Playwright on the module path; see README.md.
// The window opens on the page the reader last left, as a Mac app comes back
// as it was (molvqingtai and lc on Discord asked for Routing first, and for
// the sidebar's order to be the reader's): left on Routing, the window opened
// again (no ?view=) and reloaded is on Routing, its tab picked, nothing
// scrolled. A page asked for (?view=, the tray's links) wins over the one
// kept, Agents too. A page kept that isn't one, or one gateway mode leaves
// out (Agents), gives way to the first page (Providers there).
// Chromium and WebKit, 1100 (sidebar) and 420 (header's tabs) wide. No
// backend: the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const BASE = "http://magpie.test/";

function server({ web = false, gateway = false } = {}) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (d) => route.fulfill({ json: d });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:${web}${gateway ? ",gateway:true" : ""}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang: "en", theme: "light" } });
    if (url.pathname === "/api/settings") return json({ theme: "light", lang: "en", version: "0.1.400", gateway: "http://127.0.0.1:3425", web, gatewayOn: web && gateway, visionModels: [], imageGenModels: [], lanURLs: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// the page shown, its tab picked, and how far anything is scrolled
const shown = (page) => page.evaluate(() => {
  const views = [...document.querySelectorAll("main.view")].filter((v) => !v.hidden).map((v) => v.id.replace("view-", ""));
  const on = document.querySelector("#nav button[data-view].on")?.dataset.view || (document.querySelector("#prefs.on") ? "settings" : "");
  const scrolled = [...document.querySelectorAll("main.view")].some((v) => v.scrollTop) || scrollY !== 0 || scrollX !== 0;
  return { views, on, scrolled };
});

async function open(page, url, want, where) {
  await page.goto(url);
  await page.waitForSelector("#nav button.on, #prefs.on");
  await page.waitForTimeout(150);
  assert.deepEqual(await shown(page), { views: [want], on: want, scrolled: false }, where);
}

const keep = (page, v) => page.evaluate((x) => localStorage.setItem("magpie.lastView", x), v);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: the window opens on the page last left`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const width of [1100, 420]) {
      const where = `${engine} ${width}`;
      const context = await browser.newContext({ viewport: { width, height: 640 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server());

      // first time: Agents, the first page
      await open(page, BASE, "agents", `${where}: first open`);
      // left on Routing, opened again and reloaded: Routing
      const tab = page.locator('#nav button[data-view="routing"]');
      await tab.scrollIntoViewIfNeeded();
      await tab.click();
      await page.waitForFunction(() => !document.querySelector("#view-routing").hidden);
      await open(page, BASE, "routing", `${where}: opened again`);
      await page.reload();
      await page.waitForSelector("#nav button.on");
      assert.deepEqual(await shown(page), { views: ["routing"], on: "routing", scrolled: false }, `${where}: reloaded`);
      // Settings, left there, comes back too
      await page.locator("#prefs").click();
      await page.waitForFunction(() => !document.querySelector("#view-settings").hidden);
      await open(page, BASE, "settings", `${where}: Settings kept`);

      // a page asked for wins over the one kept, Agents too
      await keep(page, "routing");
      await open(page, BASE + "?view=usage", "usage", `${where}: ?view=usage`);
      await keep(page, "routing");
      await open(page, BASE + "?view=agents", "agents", `${where}: ?view=agents`);
      // a page there is no longer: the first page
      await keep(page, "nonesuch");
      await open(page, BASE, "agents", `${where}: a page gone`);
      // left on Agents: Agents
      await keep(page, "plugins");
      await open(page, BASE, "plugins", `${where}: Plugins kept`);
      await page.locator('#nav button[data-view="agents"]').click();
      await open(page, BASE, "agents", `${where}: Agents kept`);
      assert.deepEqual(errors, [], where);
      await context.close();

      // magpie web in gateway mode: Agents isn't there (the Library is, for
      // its MCP router)
      const gw = await browser.newContext({ viewport: { width, height: 640 }, reducedMotion: "reduce" });
      const gp = await gw.newPage();
      gp.setDefaultTimeout(5000);
      gp.setDefaultNavigationTimeout(60000);
      const gerr = [];
      gp.on("pageerror", (e) => gerr.push(e.message));
      await gp.route("**/*", server({ web: true, gateway: true }));
      await open(gp, BASE, "providers", `${where} gateway: first open`);
      for (const v of ["agents"]) {
        await keep(gp, v);
        await open(gp, BASE, "providers", `${where} gateway: ${v} kept`);
      }
      await keep(gp, "routing");
      await open(gp, BASE, "routing", `${where} gateway: routing kept`);
      assert.deepEqual(gerr, [], `${where} gateway`);
      await gw.close();
    }
  });
}
