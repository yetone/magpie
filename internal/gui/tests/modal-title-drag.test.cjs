// Run with Node's test runner and Playwright on the module path; see README.md.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const assets = path.resolve(__dirname, "../assets");

function fixture(lang) {
  const settings = { theme: "light", lang, tray: "panel", currency: "usd", textSize: 100, version: "0.1.700", redactWords: [], lanURLs: [], otel: {} };
  const relay = { id: "relay", name: "A relay whose name runs on well past the width of its own field", icon: "generic", host: "relay.test", chat: "https://relay.test/v1",
    responses: "", anthropic: "", models: [{ id: "fixture-model", name: "Fixture model", on: true }], agents: [], fallback: [], headers: {},
    key: { set: true, masked: "sk-…1234" }, keyList: [], ready: true };
  const providers = { providers: [relay], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", textSize: 100, web: true })};` });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    try { await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] }); }
    catch { await route.fulfill({ status: 404, body: "" }); }
  };
}


// ssuuuuooo on X: on the Mac the window couldn't be dragged by its title bar
// while a sheet or dialog was open: the scrim covers the bar and had no drag
// region. The window's drag is asked of the element under the pointer
// (--wails-draggable, inherited), so the strip must say drag, and the dialog
// and its controls no-drag. Every dialog of the window is the one #modal (the
// add sheet is part of the page, and a confirmation is the browser's top
// layer, which has no strip of ours). No backend.
const drag = (el) => getComputedStyle(el).getPropertyValue("--wails-draggable").trim();
const probe = (page, x, y) => page.evaluate(([x, y, f]) => { const e = document.elementFromPoint(x, y); return { id: e?.id || e?.className, v: (0, eval)(f)(e) }; }, [x, y, drag.toString()]);

for (const engine of process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"]) {
  for (const lang of ["en", "zh"]) {
    for (const width of [1100, 440]) {
      test(`${engine} ${lang} ${width}px: the title bar strip still drags the window while a dialog is open`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await browser.newPage({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", fixture(lang));
        await page.goto("http://magpie.test/?view=providers");
        const modal = page.locator("#modal");
        const kinds = [
          ["the provider editor", async () => { await page.locator('#providers .row.provider[data-id="relay"]').click(); }],
          ["a new provider's editor, from the add sheet", async () => { await page.locator("#addProvider").click(); await page.locator("#addSheet .row-head .custom").click(); }],
        ];
        for (const [name, open] of kinds) {
          await open();
          await page.getByRole("dialog").first().waitFor();
          await modal.waitFor({ state: "visible" });
          await page.waitForTimeout(400);
          const strip = await page.evaluate(() => document.querySelector("#modal .modal-grip")?.getBoundingClientRect().height ?? 0);
          assert.ok(strip >= 40, `${name}: the strip has height`);
          // the strip's far corner: past any dialog, over the scrim
          const at = await probe(page, width - 4, 4);
          assert.equal(at.v, "drag", `${name}: the top strip drags the window`);
          // the dialog's own controls and body are never the drag
          const buttons = modal.locator(".dialog button:visible, .dialog input:visible");
          const n = Math.min(await buttons.count(), 6);
          assert.ok(n > 0, `${name}: the dialog has controls`);
          for (let i = 0; i < n; i++) {
            const b = await buttons.nth(i).boundingBox();
            const r = await probe(page, b.x + b.width / 2, b.y + b.height / 2);
            assert.notEqual(r.v, "drag", `${name}: control ${i} is not a drag region`);
          }
          const d = await modal.locator(".dialog").boundingBox();
          const inDialog = await probe(page, d.x + d.width / 2, d.y + 4 > 46 ? d.y + 4 : Math.min(d.y + d.height - 4, 700));
          assert.notEqual(inDialog.v, "drag", `${name}: the dialog is not a drag region`);
          // below the strip the scrim is no drag: a press there is a backdrop click
          const low = await probe(page, 2, 700);
          assert.notEqual(low.v, "drag", `${name}: the scrim below the strip is not a drag region`);
          // a click on the strip closes the dialog as any backdrop click does
          await page.mouse.click(width - 4, 4);
          await modal.waitFor({ state: "hidden" });
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}

// ssuuuuooo again (v0.1.3631): the strip was free only at the sides; a tall
// dialog (the provider editor in a short window) lay over its middle, where a
// window is taken. Every point along the bar must drag, the middle too.
for (const engine of process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"]) {
  for (const mac of [false, true]) {
    test(`${engine} ${mac ? "mac" : "web"}: the middle of the title bar drags with a tall dialog open`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: 1000, height: 520 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      await page.route("**/*", fixture("en"));
      await page.goto("http://magpie.test/?view=providers");
      await page.evaluate((mac) => { document.body.classList.remove("web"); document.body.classList.add("window"); if (mac) document.body.classList.add("mac"); }, mac);
      await page.locator('#providers .row.provider[data-id="relay"]').click();
      await page.getByRole("dialog").first().waitFor();
      await page.waitForTimeout(400);
      const grip = await page.evaluate(() => document.querySelector("#modal .modal-grip").getBoundingClientRect().height);
      const d = await page.locator("#modal .dialog").boundingBox();
      const m = await page.locator("#modal").boundingBox();
      assert.ok(d.y >= m.y + (mac ? 0 : grip) - 0.5, `the dialog starts at ${d.y}, below the strip`);
      // the header and sidebar are inert behind a dialog: the title bar's drag is the strip's
      assert.ok(await page.evaluate(() => document.querySelector("header.top").inert), "the header is inert");
      const top = 0; // the bar's rows from the window's top edge
      for (const x of [4, 250, 500, 750, 996]) {
        for (const y of [top + 4, top + grip / 2, top + grip - 3]) {
          const r = await probe(page, x, y);
          assert.equal(r.v, "drag", `(${x}, ${y}) drags the window`);
        }
      }
    });
  }
}
