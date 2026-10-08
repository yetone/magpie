// Run with Node's test runner and Playwright on the module path; see README.md.
// The Routing page scrolls to its end (#1249, wizzy-yang on #1213): in
// WebKit the page stopped well short of its bottom, a frame after each
// scroll pulled back by the container queries its narrow and wide layouts
// went by. Wheeled down to the end, at a phone's width, the tray's, and a
// desk's, the view's scrollTop reaches its scroll range, in Chromium and
// WebKit, in English and Chinese — and the narrow and wide layouts still
// follow each box's own width.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const who = { id: "fixture-key", provider: "fixture", name: "Fixture", who: "key-1", kind: "key", model: "model-a", routing: "", used: 0 };
const routes = Array.from({ length: 12 }, (_, i) => ({ id: 100 - i, seq: 100 - i, time: at(i), agent: "fixture", model: "model-a", provider: "fixture", order: [who],
  tries: [{ id: who.id, model: "model-a", start: at(i), done: true, status: 200, ms: 1200 }], done: true, status: 200, ms: 1200, tokens: 3000 }));
const agents = Array.from({ length: 14 }, (_, i) => ({ id: "a" + i, name: "Agent " + i, path: "/t/a" + i + ".json", icon: "generic", wired: true,
  fields: [{ key: "model", label: "model", value: "m", options: [{ value: "m", label: "M", ref: "a/m" }] }] }));

function serve(lang) {
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents, profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      // the trace asked again: the page is redrawn with each answer
      if (url.searchParams.get("wait")) await new Promise((ok) => setTimeout(ok, 100));
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: routes.length, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [{ day, requests: routes.length }], routes: url.searchParams.get("day") ? routes : [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the number of columns a grid lays out
const tracks = (page, sel) => page.locator(sel).first().evaluate((e) => getComputedStyle(e).gridTemplateColumns.split(" ").length);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: the Routing page scrolls to its end`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of ["en", "zh"]) for (const [width, height] of [[440, 620], [600, 700], [900, 500], [1200, 600], [1400, 700]]) {
      await t.test(`${lang}, ${width}x${height}`, async () => {
        const ctx = await browser.newContext({ viewport: { width, height }, reducedMotion: "reduce" });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=routing");
        const view = page.locator("#view-routing");
        await page.locator("#view-routing .rt-req").nth(11).waitFor();
        await page.waitForTimeout(500);

        // the layouts by width: magpie and the agent side by side over the
        // accounts when the stage is narrow, each request on three lines in
        // a narrow list, the accounts beside the requests on a wide page
        const narrow = width <= 600;
        assert.equal(await tracks(page, ".rt-stage"), narrow ? 2 : 3, "the stage's columns");
        assert.equal(await tracks(page, ".rt-reqs"), width < 800 ? 2 : 4, "the requests' columns");
        assert.equal(await tracks(page, ".rt-cols"), width >= 1200 ? 2 : 1, "the requests and the accounts, side by side or one under the other");

        // down to the end, by the wheel
        const box = await view.boundingBox();
        await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
        for (let i = 0; i < 20; i++) { await page.mouse.wheel(0, 300); await page.waitForTimeout(40); }
        await page.waitForTimeout(300);
        // and on at the end, as the page is redrawn: each turn of the wheel
        // there leaves it at its end, not pulled back from it
        const short = [];
        for (let i = 0; i < 10; i++) {
          await page.mouse.wheel(0, 300);
          await page.waitForTimeout(120);
          const { range, top } = await view.evaluate((v) => ({ range: v.scrollHeight - v.clientHeight, top: v.scrollTop }));
          assert(range > 0, "the page is taller than the window here");
          if (top < range - 1) short.push(`${top} of ${range}`);
        }
        assert.deepEqual(short, [], "scrolled to");
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
