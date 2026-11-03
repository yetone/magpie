// A macOS window hidden with orderOut keeps its WKWebView's document visible,
// so Routing's frame loop and SVG animations could keep a closed window hot.
// The app now tells the page before hiding it; this checks that the page's
// visibility, change event and CSS animations all follow that signal.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const alternateAppJS = process.env.MAGPIE_APP_JS;

function serve() {
  const state = { agents: [], profiles: [], settings: { lang: "en", theme: "light" } };
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: 'window.bootPrefs={lang:"en",theme:"light",web:true};' });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.pathname === "/app.js" && alternateAppJS) {
      return route.fulfill({ body: await fs.readFile(alternateAppJS), contentType: "text/javascript" });
    }
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: hiding the native window makes the page out of sight`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await browser.newPage();
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    await page.route("**/*", serve());
    await page.goto("http://magpie.test/?view=providers");
    await page.waitForFunction(() => typeof window.magpieWindowVisibility === "function");

    const hidden = await page.evaluate(() => {
      const style = document.createElement("style");
      style.textContent = "#visibilityProbe{animation:visibility-spin 1s linear infinite}@keyframes visibility-spin{to{transform:rotate(1turn)}}";
      document.head.append(style);
      const probe = document.createElement("i");
      probe.id = "visibilityProbe";
      document.body.append(probe);
      window.__visibilityEvents = 0;
      document.addEventListener("visibilitychange", () => window.__visibilityEvents++);
      window.magpieWindowVisibility(false);
      window.magpieWindowVisibility(false);
      return {
        hidden: document.hidden,
        state: document.visibilityState,
        marked: document.documentElement.classList.contains("magpie-out-of-sight"),
        playState: getComputedStyle(probe).animationPlayState,
        events: window.__visibilityEvents,
      };
    });
    assert.deepEqual(hidden, { hidden: true, state: "hidden", marked: true, playState: "paused", events: 1 });

    const shown = await page.evaluate(() => {
      window.magpieWindowVisibility(true);
      window.magpieWindowVisibility(true);
      return {
        hidden: document.hidden,
        state: document.visibilityState,
        marked: document.documentElement.classList.contains("magpie-out-of-sight"),
        playState: getComputedStyle(document.querySelector("#visibilityProbe")).animationPlayState,
        events: window.__visibilityEvents,
      };
    });
    assert.deepEqual(shown, { hidden: false, state: "visible", marked: false, playState: "running", events: 2 });

    // a window that takes focus is on screen, whichever path showed it
    const focused = await page.evaluate(() => {
      window.magpieWindowVisibility(false);
      window.dispatchEvent(new Event("focus"));
      return { hidden: document.hidden, marked: document.documentElement.classList.contains("magpie-out-of-sight") };
    });
    assert.deepEqual(focused, { hidden: false, marked: false });
  });
}
