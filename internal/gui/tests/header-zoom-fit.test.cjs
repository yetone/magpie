// Run with Node's test runner and Playwright on the module path; see README.md.
// emo172 (#457): on Windows at text size 110% the header's tabs stayed drawn
// in, in a window with room to spare. The text size is the webview's page
// zoom, and under it the buttons' right edge measures a hair (0.00004 CSS
// pixels) past the header's padding, where they sit by construction; fitTop
// took that for buttons running off, and drew the header in step after step.
// Chromium here zooms the page as the webview does (its default zoom level,
// not a device scale factor, which lays out without that rounding); WebKit
// has no page zoom to set, so it is the device scale factor there. A Windows
// window is swept from 1300 to 700 points at 110 and 125%, with an Update
// pill waiting: a wider window is never drawn in more than a narrower one,
// and with room to spare the header is as at 100%. English and Chinese; no
// backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const STEPS = ["tight", "cramped", "inrow", "crowded", "packed"];

function server(lang, size) {
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false,textSize:${size}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light", textSize: size } } });
    if (url.pathname === "/api/update") return route.fulfill({ json: { state: "ready", version: "0.1.590", latest: "0.1.592" } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], presets: [], excluded: [], gateway: { running: true, window: true } } });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// a browser whose pages are zoomed by `zoom`, as the webview zooms magpie's
async function zoomed(engine, zoom, t) {
  const viewport = { width: 1300, height: 600 };
  if (engine === "webkit") {
    const browser = await webkit.launch();
    t.after(() => browser.close());
    return browser.newContext({ viewport, deviceScaleFactor: zoom });
  }
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "magpie-zoom-"));
  await fs.mkdir(path.join(dir, "Default"));
  await fs.writeFile(path.join(dir, "Default", "Preferences"), JSON.stringify({ partition: { default_zoom_level: { x: Math.log(zoom) / Math.log(1.2) } } }));
  const ctx = await chromium.launchPersistentContext(dir, { channel: "chromium", viewport });
  t.after(async () => { await ctx.close(); await fs.rm(dir, { recursive: true, force: true }); });
  return ctx;
}

const settled = (page) => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
const steps = (page) => page.evaluate((all) => all.filter((c) => document.querySelector(".top").classList.contains(c)).length, STEPS);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a zoomed header with room is drawn as at 100%", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    for (const lang of ["zh", "en"]) for (const zoom of [1.1, 1.25]) {
      await t.test(`${lang} at ${Math.round(zoom * 100)}%`, async (t) => {
        const ctx = await zoomed(engine, zoom, t);
        await ctx.addInitScript(() => { Object.defineProperty(Navigator.prototype, "platform", { get: () => "Win32" }); });
        const page = ctx.pages()[0] || await ctx.newPage();
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, Math.round(zoom * 100)));
        await page.goto("http://magpie.test/");
        await page.waitForSelector("#nav button.on");
        await page.waitForFunction(() => !document.querySelector("#update").hidden);
        assert(Math.abs(await page.evaluate(() => devicePixelRatio) - zoom) < 0.01, "the page isn't zoomed");
        let last = 0;
        for (let w = 1300; w >= 700; w -= 10) {
          // the window's points; at a 1.1 zoom 1300 points are 1182 CSS pixels
          await page.setViewportSize({ width: engine === "webkit" ? Math.round(w / zoom) : w, height: 600 });
          await settled(page);
          const n = await steps(page), at = `${w}pt: ${await page.evaluate(() => document.querySelector(".top").className)}`;
          if (w >= 1200) assert.equal(n, 0, "the header is drawn in with room to spare at " + at);
          assert(n >= last, "a wider window is drawn in more than a narrower one at " + at);
          last = n;
        }
        assert(last > 0, "the narrowest window never drew the header in");
        assert.deepEqual(errors, []);
      });
    }
  });
}
