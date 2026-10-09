// Run with Node's test runner and Playwright on the module path; see README.md.
// The refresh icon stops where a turn ends, on the compositor's clock: it
// used to wait for animationiteration and then drop its spin, so a page busy
// drawing the reply when the turn came round let the icon run on and then
// jump back (the twitch reported on X).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: the refresh icon ends on a whole turn, however busy the page is`, async (t) => {
      assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 600 } })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const settings = { lang, theme: "light" };
      const state = { agents: [], profiles: [], settings };
      await page.route("**/*", async (route) => {
        const url = new URL(route.request().url());
        const json = (data) => route.fulfill({ json: data });
        if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
        if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
        if (url.pathname === "/api/state") return json(state);
        if (url.pathname === "/api/sync" || url.pathname === "/api/agents/rescan") {
          await new Promise((r) => setTimeout(r, 300));
          return json(state);
        }
        if (url.pathname === "/api/settings") return json(settings);
        if (url.pathname === "/api/groups") return json({ groups: [] });
        if (url.pathname.startsWith("/api/")) return json({});
        const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
        const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
        await route.fulfill({ body: await fs.readFile(file), contentType });
      });
      await page.goto("http://magpie.test/?view=agents");
      await page.locator("#sync").waitFor();
      const end = await page.evaluate(() => new Promise((done) => {
        const b = document.querySelector("#sync");
        const svg = b.querySelector("svg");
        let spin;
        // the reply arrives and the page is busy past the end of the turn
        // it caught: the icon must still stop there, not wherever it got to
        const status = document.querySelector("#status") || document.body;
        new MutationObserver((_, o) => {
          o.disconnect();
          const s = performance.now();
          while (performance.now() - s < 1100) {}
        }).observe(status, { childList: true, subtree: true, characterData: true, attributes: true });
        // read the spin as it is let go, before the class takes it away
        const list = b.classList, remove = list.remove;
        list.remove = function (...names) {
          if (names.includes("spin") && spin) done({ state: spin.playState, time: spin.currentTime, end: spin.effect.getComputedTiming().endTime });
          return remove.apply(this, names);
        };
        b.click();
        spin = svg.getAnimations({ subtree: true })[0];
      }));
      assert.equal(end.state, "finished", `the spin was cut off mid-turn at ${Math.round(end.time)}ms`);
      assert(Number.isFinite(end.end) && Math.round(end.end) % 900 === 0, `the spin ends on a whole turn, not at ${end.end}ms`);
      assert.deepEqual(errors, []);
    });
  }
}
