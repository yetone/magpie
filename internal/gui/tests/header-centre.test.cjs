// Run with Node's test runner and Playwright on the module path; see README.md.
// emo172 (#442): on Windows at text size 110% the header's tabs sat left of
// the middle: once their gaps closed up (fitTop's cramped) they also left the
// middle for the row, a third of the room to their left and two thirds to
// their right. Now the closer tabs are tried in the middle first, and only
// when they don't fit there go in the row, halfway between what is either
// side of them. The window is swept from 1100 to 560 points at text size
// 100, 110 and 125% (the zoom is the webview's, so here a browser zoomed:
// the window's points over the zoom in CSS pixels), with an Update pill
// waiting as in the report: the tabs never touch the buttons, are in the
// middle of the window whenever they aren't in the row, and in the row have
// as much room either side, give or take the header's padding against the
// gap before the buttons. Chromium and WebKit, English and Chinese; no
// backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
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

const settled = (page) => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
const header = (page) => page.evaluate(() => {
  const top = document.querySelector(".top"), r = top.getBoundingClientRect();
  const n = document.querySelector("#nav").getBoundingClientRect(), a = document.querySelector(".actions").getBoundingClientRect();
  return {
    cls: top.className, update: !document.querySelector("#update").hidden,
    off: n.left + n.width / 2 - innerWidth / 2, left: n.left - r.left, right: a.left - n.right,
  };
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the header's tabs in the middle, or halfway", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of ["zh", "en"]) for (const zoom of [1, 1.1, 1.25]) {
      await t.test(`${lang} at ${Math.round(zoom * 100)}%`, async () => {
        const ctx = await browser.newContext({ viewport: { width: Math.round(1100 / zoom), height: 600 }, deviceScaleFactor: zoom });
        await ctx.addInitScript(() => { Object.defineProperty(Navigator.prototype, "platform", { get: () => "Win32" }); });
        const page = await ctx.newPage();
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/");
        await page.waitForSelector("#nav button.on");
        await page.waitForFunction(() => !document.querySelector("#update").hidden);
        let middle = 0, inrow = 0;
        for (let w = 1100; w >= 560; w -= 10) {
          await page.setViewportSize({ width: Math.round(w / zoom), height: 600 });
          await settled(page);
          const h = await header(page), at = `${w}pt: ${JSON.stringify(h)}`;
          assert(h.update, "the Update pill is up");
          assert(h.right >= 7.5, "the tabs touch the buttons at " + at);
          if (h.cls.includes("inrow")) {
            inrow++;
            assert(h.left >= 7.5, "the tabs run off the left at " + at);
            assert(Math.abs(h.left - h.right) <= 6.5, "the tabs aren't halfway at " + at);
          } else {
            middle++;
            assert(Math.abs(h.off) <= 0.5, "the tabs aren't in the middle at " + at);
          }
          // the closer tabs are tried in the middle before the row
          if (h.cls.includes("cramped") && !h.cls.includes("inrow")) middle += 1000;
        }
        assert(middle > 1000, `the closer tabs were never in the middle (${middle})`);
        assert(inrow > 0, "the narrowest windows never put the tabs in the row");
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
