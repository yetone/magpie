// Run with Node's test runner and Playwright on the module path; see README.md.
// The owner: opening a connected agent's row shook it — the line with its
// name moved and the space under it grew — and opening and closing jumped
// instead of moving as iOS does. Opened, the name and the switch stay where
// they were and the opened part starts right where the shut row ended; it
// slides open and shut through the heights between, and no step at either
// end. With reduced motion it opens at once. Wide and narrow windows; no
// backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const model = (id, label) => ({ key: "model", label: "model", value: id, options: [{ value: id, label, ref: "relay/" + id, note: "Relay · via magpie" }] });
const state = {
  agents: [
    { id: "codex", name: "Codex", icon: "generic", path: "/fixture/codex", wired: true, fields: [model("m1", "m1"), { key: "effort", label: "effort", value: "high", options: [{ value: "high", label: "High" }] }] },
    { id: "goose", name: "Goose", icon: "generic", path: "/fixture/goose", wired: true, fields: [model("m2", "m2")] },
  ],
  profiles: [],
};

async function serve(route) {
  const url = new URL(route.request().url());
  const json = (data) => route.fulfill({ json: data });
  if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:false};` });
  if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
  if (url.pathname === "/api/state") return json({ ...state, settings: { lang: "en", theme: "light" } });
  if (url.pathname === "/api/usage/quotas") return json([]);
  if (url.pathname === "/api/groups") return json({ groups: [] });
  if (url.pathname === "/api/plugins") return json({ plugins: [] });
  if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
  if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
  if (url.pathname.startsWith("/api/")) return json({});
  const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
  await route.fulfill({ body: await fs.readFile(file), contentType });
}

const row = '.row.agent[data-id="codex"]';
// where the row's line sits: its height shut and its bottom line, the
// name's and the switch's offsets, and the opened part's top
const where = (page) => page.evaluate((r) => {
  const el = document.querySelector(r), top = el.getBoundingClientRect().top;
  const at = (s) => Math.round(el.querySelector(s)?.getBoundingClientRect().top - top);
  return { height: Math.round(el.getBoundingClientRect().height), line: parseFloat(getComputedStyle(el).borderBottomWidth), who: at(":scope > .who"), sw: at(":scope > .ag-conn"), exp: el.querySelector(".ag-exp") ? at(":scope > .ag-exp") : null };
}, row);
// the row's height every frame for a while, from a click on its link
const heights = (page, ms) => page.evaluate(([r, ms]) => new Promise((done) => {
  const hs = [], t0 = performance.now();
  const f = () => { hs.push(Math.round(document.querySelector(r).getBoundingClientRect().height)); if (performance.now() - t0 < ms) requestAnimationFrame(f); else done(hs); };
  document.querySelector(r + " .ag-link").click();
  requestAnimationFrame(f);
}), [row, ms]);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [980, 560]) {
    test(`${engine} ${width}px: a row opens in place, and slides`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width, height: 700 } });
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve);
      await page.goto("http://magpie.test/?view=agents");
      await page.locator(`${row} .ag-conn`).waitFor();

      const shut = await where(page);
      const opening = await heights(page, 800);
      const open = await where(page);
      assert.equal(open.who, shut.who, "the name moved");
      assert.equal(open.sw, shut.sw, "the switch moved");
      // its top line where the shut row's bottom line was
      assert.equal(open.exp, shut.height - shut.line, "the opened part starts where the shut row ended");
      const full = open.height;
      assert.ok(full > shut.height + 60, `opened: ${full}`);
      // the heights between, growing, no step bigger than a frame's worth
      const mid = opening.filter((h) => h > shut.height && h < full);
      assert.ok(mid.length >= 3, "slid open: " + opening);
      for (let i = 1; i < opening.length; i++) assert.ok(opening[i] >= opening[i - 1], "grew only: " + opening);
      const closing = await heights(page, 700);
      assert.ok(closing.filter((h) => h > shut.height && h < full).length >= 3, "slid shut: " + closing);
      for (let i = 1; i < closing.length; i++) assert.ok(closing[i] <= closing[i - 1], "shrank only: " + closing);
      assert.deepEqual(await where(page), shut, "shut again as it was");

      // another row open: the one open slides shut, this one opens
      await page.locator(`${row} .ag-link`).click();
      await page.waitForTimeout(700);
      await page.locator('.row.agent[data-id="goose"] .ag-link').click();
      await page.waitForTimeout(1000);
      assert.equal(await page.locator(".row.agent .ag-exp").count(), 1);
      assert.equal(await page.locator('.row.agent[data-id="goose"] .ag-exp').count(), 1);
      assert.equal((await where(page)).height, shut.height);

      // reduced motion: open at once
      await page.emulateMedia({ reducedMotion: "reduce" });
      await page.locator('.row.agent[data-id="goose"] .ag-link').click();
      await page.waitForTimeout(500);
      const now = await heights(page, 120);
      assert.equal(now[0], full, "opened at once: " + now);
      assert.deepEqual(errors, []);
    });
  }
}
