// Run with Node's test runner and Playwright on the module path; see README.md.
// Beside the sidebar the open page's row is marked by one shape (app.js
// navTopGo) that moves to the row picked on a spring, as iOS moves a
// selection, the way the sub-rows' selection slides: it sets off as the row
// is pressed, goes back when the press is let go off the row, and lands on
// it. Down from the Library, whose list folds away above the row picked, it
// goes to where that row comes to rest and stays there, never past it and
// back up (小李 on Discord: a white block bounced up). The rows have no ground of their own under it, hovered or not.
// Settings, at the foot, has its own; the shape is away while it is open.
// With reduced motion it is put there at once. In the header's tabs it
// isn't drawn. Both engines, two languages, two widths.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/aimer";
const library = {
  dir: `${HOME}/.magpie/library`, backups: `${HOME}/.magpie/backups`, home: HOME,
  agents: [{ id: "codex", name: "Codex", icon: "", skills: `${HOME}/.codex/skills`, mcp: `${HOME}/.codex/config.toml` }],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [],
  skills: [{ name: "grilling", description: "Grill a plan", kind: "folder", agents: ["codex"], source: `${HOME}/skills/grilling` }],
  servers: [{ name: "fs", transport: "stdio", command: "npx", args: ["fs-mcp"], agents: ["codex"] }],
};

function serve(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { url: "http://127.0.0.1:3425", running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/library") return json(library);
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// the shape's top and height, and a row's, in the sidebar's list
const shape = (page) => page.evaluate(() => {
  const hl = document.querySelector("#nav > .nav-top-hl");
  if (!hl || hl.hidden || getComputedStyle(hl).display === "none") return null;
  const b = hl.getBoundingClientRect();
  return { top: b.top, h: b.height };
});
const rowOf = (page, v) => page.evaluate((v) => {
  const b = document.querySelector(`#nav button[data-view="${v}"]`).getBoundingClientRect();
  return { top: b.top, h: b.height };
}, v);
// the shape's tops, one a frame, until it has been still for 20 frames
const watch = (page) => page.evaluate(() => { window.__tops = []; const f = () => { const hl = document.querySelector("#nav > .nav-top-hl"); window.__tops.push(hl.getBoundingClientRect().top); const t = window.__tops; if (t.length < 400 && !(t.length > 20 && t.slice(-20).every((y) => y === t[t.length - 1]))) requestAnimationFrame(f); else window.__still = true; }; window.__still = false; requestAnimationFrame(f); });
const settled = async (page) => { await page.waitForFunction(() => window.__still); return page.evaluate(() => window.__tops); };
const on = async (page, v) => {
  const [s, r] = [await shape(page), await rowOf(page, v)];
  assert(s, `the shape is drawn for ${v}`);
  assert(Math.abs(s.top - r.top) < 0.5 && Math.abs(s.h - r.h) < 0.5, `the shape on ${v}'s row: ${JSON.stringify(s)} vs ${JSON.stringify(r)}`);
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: the sidebar's open page is one shape that springs from row to row`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) for (const width of [1024, 1440]) await t.test(`${lang} ${width}`, async () => {
      const context = await browser.newContext({ viewport: { width, height: 800 } });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=providers");
      await page.waitForSelector("#nav > .nav-top-hl:not([hidden])");
      assert.ok(await page.evaluate(() => document.body.classList.contains("side-nav")), "a sidebar at " + width);
      await on(page, "providers");
      // the list where it was before the shape: in the sidebar, at its edge
      const [side, nav, hl] = await page.evaluate(() => ["#side", "#nav", "#nav > .nav-top-hl"].map((q) => { const b = document.querySelector(q).getBoundingClientRect(); return { l: b.left, r: b.right }; }));
      assert(nav.l - side.l < 16 && nav.r <= side.r && hl.l === nav.l && hl.r === nav.r, `the list in the sidebar: ${JSON.stringify({ side, nav, hl })}`);
      const ground = (v) => page.locator(`#nav button[data-view="${v}"]`).evaluate((b) => getComputedStyle(b).backgroundColor);
      assert.equal(await ground("providers"), "rgba(0, 0, 0, 0)", "the open row's ground is the shape's");

      // hovered, a row has no ground of its own
      await page.locator('#nav button[data-view="agents"]').hover();
      assert.equal(await ground("agents"), "rgba(0, 0, 0, 0)", "no block under the pointer ahead of the shape");

      // pressed, the shape sets off before the press ends; let go off the
      // row, it goes back and the page stays
      const usage = await page.locator('#nav button[data-view="usage"]').boundingBox();
      await page.mouse.move(usage.x + 20, usage.y + usage.height / 2);
      await watch(page);
      await page.mouse.down();
      await page.waitForFunction((from) => Math.abs(document.querySelector("#nav > .nav-top-hl").getBoundingClientRect().top - from) > 2, (await shape(page)).top);
      assert.equal(await page.locator("#nav button.on").getAttribute("data-view"), "providers", "the page changes only on the click");
      await page.mouse.move(usage.x + usage.width + 300, usage.y + 200);
      await page.mouse.up();
      await settled(page);
      assert.equal(await page.locator("#nav button.on").getAttribute("data-view"), "providers");
      await on(page, "providers");

      // to Usage, then up to Agents while Usage's tabs fold away under it,
      // then the Library and down to Providers while the Library's fold away
      // above it: it moves by frames, never at once, and lands on its row
      for (const v of ["usage", "agents", "library", "providers"]) {
        if (v === "providers") assert(await page.locator("#nav .nav-sub:not(.leaving)").count(), "the Library's list is open above Providers");
        const from = (await shape(page)).top, to = (await rowOf(page, v)).top;
        await watch(page);
        await page.locator(`#nav button[data-view="${v}"]`).click();
        const tops = await settled(page);
        await on(page, v);
        const moving = tops.filter((y) => Math.abs(y - from) > 0.5 && Math.abs(y - (tops[tops.length - 1])) > 0.5);
        assert(moving.length >= 3, `${v}: the shape moves by frames, not at once: ${tops.map(Math.round).join(" ")}`);
        const end = tops[tops.length - 1], past = Math.max(...tops.map((y) => Math.sign(end - from) * (y - end)));
        assert(past < 3, `${v}: it settles with at most a little overshoot (${past.toFixed(2)}px): ${tops.map(Math.round).join(" ")}`);
        if (v === "usage") assert(Math.abs(end - to) < 0.5);
      }

      // Settings has its own; back, the shape is on the row picked
      await page.locator("#prefs").click();
      await page.waitForFunction(() => document.querySelector("#nav > .nav-top-hl").hidden);
      assert.notEqual(await page.locator("#prefs").evaluate((b) => getComputedStyle(b).backgroundColor), "rgba(0, 0, 0, 0)", "Settings keeps its own selection");
      await page.locator('#nav button[data-view="routing"]').click();
      await page.waitForTimeout(300);
      await on(page, "routing");

      // reduced motion: there at once
      await page.emulateMedia({ reducedMotion: "reduce" });
      await page.locator('#nav button[data-view="gateway"]').click();
      await on(page, "gateway");
      assert.deepEqual(errors, []);
      await context.close();
    });

    await t.test("the header's tabs draw none", async () => {
      const context = await browser.newContext({ viewport: { width: 900, height: 700 } });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      await page.route("**/*", serve("en"));
      await page.goto("http://magpie.test/?view=providers");
      await page.waitForSelector("#nav button.on");
      assert.ok(!(await page.evaluate(() => document.body.classList.contains("side-nav"))));
      await page.locator('#nav button[data-view="usage"]').click();
      assert.equal(await shape(page), null);
      assert.notEqual(await page.locator("#nav > .thumb").count(), 0, "the header's own thumb");
      await context.close();
    });
  });
}
