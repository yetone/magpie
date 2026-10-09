// Run with Node's test runner and Playwright on the module path; see README.md.
// #1373 (xiaozhu1337): text selected in a dialog by dragging, with the mouse
// let go outside the dialog, closed the dialog. Only a press and a release
// both on the backdrop close it: a drag out of a field, or in from the
// backdrop, leaves it open. The same holds for the provider editor and for
// the confirmation over it. No backend, the API is faked here.
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

// press at one point, move in steps (a selection follows), let go at another
async function drag(page, from, to) {
  await page.mouse.move(from.x, from.y);
  await page.mouse.down();
  await page.mouse.move(to.x, to.y, { steps: 8 });
  await page.mouse.up();
}
const middle = async (locator) => { const b = await locator.boundingBox(); return { x: b.x + b.width / 2, y: b.y + b.height / 2 }; };
const out = { x: 2, y: 2 };

for (const engine of process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"]) {
  for (const lang of ["en", "zh"]) {
    for (const width of [1100, 440]) {
      test(`${engine} ${lang} ${width}px: a selection dragged out of a dialog doesn't close it`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await browser.newPage({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", fixture(lang));
        await page.goto("http://magpie.test/?view=providers");
        const row = page.locator('#providers .row.provider[data-id="relay"]');
        const modal = page.locator("#modal");
        await row.click();
        await page.getByRole("dialog").waitFor();
        const field = modal.locator("input[type=text], input:not([type])").first();
        await field.waitFor();
        const name = await field.inputValue();

        // #1373: from inside the name field, out past the dialog, let go
        const start = await field.boundingBox();
        // as the reporter does, past the field's end to bring the rest in
        const beside = { x: width - 3, y: start.y + start.height / 2 };
        assert.equal(await page.evaluate(({ x, y }) => document.elementFromPoint(x, y)?.id, beside), "modal", "the drag ends over the backdrop");
        await drag(page, { x: start.x + 6, y: beside.y }, beside);
        assert.ok(await field.evaluate((e) => e.selectionEnd > e.selectionStart), "the drag selected text");
        await page.waitForTimeout(300);
        assert.equal(await modal.isVisible(), true, "a selection dragged out keeps the dialog");
        // a press on the backdrop let go inside the dialog is no backdrop click either
        await drag(page, out, await middle(modal.locator(".dialog")));
        await page.waitForTimeout(300);
        assert.equal(await modal.isVisible(), true, "a press from the backdrop let go inside keeps the dialog");
        assert.equal(await field.inputValue(), name, "the field is as it was");
        // a real click on the backdrop still closes it
        await page.mouse.click(out.x, out.y);
        await modal.waitFor({ state: "hidden" });

        // the confirmation over an edited dialog keeps to the same rule
        await row.click();
        const url = modal.locator('input[type="url"]').first();
        await url.fill("https://changed.test/v1");
        await page.mouse.click(out.x, out.y);
        const ask = page.getByRole("alertdialog");
        await ask.waitFor();
        await drag(page, await middle(ask.locator("p")), out);
        await page.waitForTimeout(300);
        assert.equal(await ask.isVisible(), true, "a selection dragged out of the confirmation keeps it");
        await page.mouse.click(out.x, out.y);
        await ask.waitFor({ state: "detached" });
        assert.equal(await modal.isVisible(), true, "the backdrop's click on the confirmation keeps the edit");
        assert.equal(await url.inputValue(), "https://changed.test/v1");
        assert.deepEqual(errors, []);
      });
    }
  }
}
