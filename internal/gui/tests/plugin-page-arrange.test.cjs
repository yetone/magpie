// Run with Node's test runner and Playwright on the module path; see README.md.
// The sidebar's plugin pages (0lVeer on X): a press held on a tab picks it
// up, it is dragged to its place and the order is kept (plugin-pages/arrange);
// a quick drag or click moves nothing. The API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const entry = (name) => ({ spec: "/x/" + name, name, key: "plugin-" + name, title: name, reads: [], url: `/_magpie/plugin/${"a".repeat(32)}/${name}.html` });
const names = ["alpha", "beta", "gamma"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a held press on a plugin tab drags it and the order is kept`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const context = await browser.newContext({ viewport: { width: 1100, height: 700 }, reducedMotion: "reduce" });
    const page = await context.newPage();
    page.setDefaultTimeout(8000);
    const posts = [];
    let order = [];
    await page.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      const json = (data) => route.fulfill({ json: data });
      if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:false};` });
      if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
      if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang: "en", theme: "light" } });
      if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
      if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
      if (url.pathname === "/api/plugin-pages/arrange") { const b = route.request().postDataJSON(); posts.push(b.order); order = b.order; return json({}); }
      if (url.pathname === "/api/plugin-pages") return json(names.map(entry).sort((a, b) => (order.indexOf(a.key) + 1 || 9) - (order.indexOf(b.key) + 1 || 9)));
      if (url.pathname === "/api/plugin-panels") return json([]);
      if (url.pathname.startsWith("/api/") || url.pathname.startsWith("/_magpie/")) return json({});
      if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
      const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
      const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
      try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
    });
    await page.goto("http://magpie.test/");
    const tabs = page.locator("#nav button[data-plugin-page]");
    await tabs.nth(2).waitFor({ state: "attached" });
    const keys = () => tabs.evaluateAll((l) => l.map((b) => b.dataset.view.replace("plugin-", "")));
    const center = async (i) => { const r = await tabs.nth(i).boundingBox(); return [r.x + r.width / 2, r.y + r.height / 2]; };
    assert.deepEqual(await keys(), names);

    // quickly dragged: nothing is picked up
    let [x, y] = await center(0), [, y2] = await center(2);
    await page.mouse.move(x, y);
    await page.mouse.down();
    await page.mouse.move(x, y2, { steps: 6 });
    await page.mouse.up();
    assert.deepEqual(await keys(), names);
    assert.deepEqual(posts, []);

    // held, then dragged below the last
    [x, y] = await center(0);
    await page.mouse.move(x, y);
    await page.mouse.down();
    await page.waitForTimeout(550);
    [, y2] = await center(2);
    await page.mouse.move(x, y + 8, { steps: 3 });
    await page.mouse.move(x, y2 + 6, { steps: 10 });
    await page.mouse.up();
    await page.waitForFunction(() => !document.querySelector("#nav.sorting"));
    assert.deepEqual(await keys(), ["beta", "gamma", "alpha"]);
    assert.deepEqual(posts, [["plugin-beta", "plugin-gamma", "plugin-alpha"]]);
    assert.equal(await page.evaluate(() => document.querySelector("#view-plugin-alpha")?.classList.contains("on") ?? false), false, "the drop is not a click");

    // a reload lists them as kept
    await page.reload();
    await tabs.nth(2).waitFor({ state: "attached" });
    assert.deepEqual(await keys(), ["beta", "gamma", "alpha"]);
  });
}
