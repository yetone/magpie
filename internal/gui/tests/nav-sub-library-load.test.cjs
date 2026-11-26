// Run with Node's test runner and Playwright on the module path; see README.md.
// chen on Discord: on every reload of Library › MCP servers, with no agent
// on this computer, the sidebar's list under Library stood a dozen rows
// tall, empty below Skills, for a fraction of a second. Until the library
// is read the page drew all its tabs, the list unrolled to their height,
// and the three left once the library came held that height until the
// unroll was done (app.js navSubs, navicons.js navSubRefit).
// Two loads, each with the library's read held until the list unrolls:
// the first visit (the tabs are unknown till the read) and a reload (the
// page draws the three it had last time). In every frame the list ends
// with its last row, and on the reload it is three rows from the start.
// Every language, both engines. No backend: the API is faked here.
// MAGPIE_ASSETS picks another assets folder (the old code, to see it red).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));
const HOME = "/srv/mg";
// no agent on this computer: the library is MCP, Skills and Instructions
const library = {
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  mcpHub: true, hubPath: "/mcp/magpie/hub/", agents: [],
  instructions: { agents: [], sets: [{ id: "default", name: "", active: true, text: "Be brief." }] },
  skills: [], foundSkills: [], projects: [], problems: [], foundServers: [],
  servers: [{ name: "calc", command: "calc", args: [], agents: [] }],
};

function server(lang, gate) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", gateway: "http://127.0.0.1:3425" } });
    if (url.pathname === "/api/library") { await gate.held; return json(library); }
    if (url.pathname.startsWith("/api/library/market/")) return json({ items: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { url: "http://127.0.0.1:3425", running: true, window: true }, plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// every frame: how far the list reaches below its last row, the most seen
const watch = () => {
  window.__gap = { most: -Infinity, at: "" };
  const look = () => {
    const box = document.querySelector("#nav .nav-sub:not(.leaving)");
    const rows = box ? [...box.querySelectorAll(".nav-s")] : [];
    if (rows.length) {
      const gap = box.getBoundingClientRect().bottom - rows[rows.length - 1].getBoundingClientRect().bottom;
      if (gap > window.__gap.most) window.__gap = { most: gap, at: `${rows.length} rows, list ${Math.round(box.getBoundingClientRect().height)}px` };
    }
    requestAnimationFrame(look);
  };
  requestAnimationFrame(look);
};

const sideNames = (page) => page.evaluate(() => [...document.querySelectorAll("#nav .nav-sub:not(.leaving) .nav-s")].map((s) => s.querySelector("span").textContent.trim()));
const still = (page) => page.waitForFunction(() => {
  const b = document.querySelector("#nav .nav-sub:not(.leaving)");
  return b && !document.querySelector("#nav .nav-sub.leaving") && [b, ...b.querySelectorAll("*")].every((e) => e.getAnimations().every((a) => a.playState !== "running"));
});
const unrolling = (page) => page.waitForFunction(() => document.querySelector("#nav .nav-sub:not(.leaving)")?.getAnimations().some((a) => a.playState === "running"));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: the Library's list in the sidebar has no empty room as the library loads`, async (t) => {
    const { I18N } = strings();
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) await t.test(lang, async () => {
      const w = (k) => (lang === "en" ? k : I18N[lang][k]);
      const three = [w("Instructions"), w("MCP servers"), w("Skills")];
      let gate = {};
      const hold = () => { gate.held = new Promise((r) => { gate.open = r; }); };
      hold();
      const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
      await context.addInitScript(watch);
      const page = await context.newPage();
      page.setDefaultTimeout(8000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, gate));
      await page.goto("http://magpie.test/");
      await page.waitForSelector("#nav button.on");
      assert.ok(await page.evaluate(() => document.body.classList.contains("side-nav")), "a sidebar");

      // the first visit: the library comes while its list unrolls
      await page.locator('#nav button[data-view="library"]').click();
      await unrolling(page);
      gate.open();
      await page.locator("#view-library .lib-row").first().waitFor();
      await page.waitForFunction((n) => document.querySelectorAll("#nav .nav-sub:not(.leaving) .nav-s").length === n, three.length);
      await still(page);
      assert.deepEqual(await sideNames(page), three, "the list once the library is read");
      let gap = await page.evaluate(() => window.__gap);
      assert.ok(gap.most <= 1, `first visit: the list reached ${gap.most.toFixed(1)}px below its last row (${gap.at})`);

      // a reload on the Library: three rows from the first frame
      hold();
      await page.reload();
      await page.waitForFunction(() => document.querySelector("#nav .nav-sub:not(.leaving) .nav-s"));
      assert.deepEqual(await sideNames(page), three, "reload: the list before the library is read");
      assert.deepEqual((await page.locator("#view-library .lib-tabs .opt").allTextContents()).map((x) => x.trim()), three, "reload: the page's tabs before the library is read");
      assert.equal((await page.locator("#view-library .lib-tabs .opt.on").textContent()).trim(), w("MCP servers"));
      gate.open();
      await page.locator("#view-library .lib-row").first().waitFor();
      await still(page);
      assert.deepEqual(await sideNames(page), three, "reload: the list once the library is read");
      gap = await page.evaluate(() => window.__gap);
      assert.ok(gap.most <= 1, `reload: the list reached ${gap.most.toFixed(1)}px below its last row (${gap.at})`);

      assert.deepEqual(errors, []);
      await context.close();
    });
  });
}
