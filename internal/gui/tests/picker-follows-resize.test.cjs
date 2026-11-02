// Run with Node's test runner and Playwright on the module path; see README.md.
// #1134 (emo172): the Agents page's model picker opened in a wide window
// stayed where it was placed when the window came out of full screen, half
// of it past the window's right edge. A resize now places it again by its
// button, and closes it when the button is out of sight. English and
// Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = ["glm-5", "deepseek-v4"].map((m) => ({ value: m, ref: "p/" + m, label: "Label " + m }));
const agents = [{ id: "claude", name: "Claude Code", path: "/test/claude", wired: true, fields: [{ key: "model", label: "model", value: "glm-5", options: models }] }];

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents, profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const button = '.row.agent[data-id="claude"] > .field.ag-start[data-key="model"]';

// where the pop was placed (its style: its rect is scaled while it opens)
// and where its button is
const where = (page) => page.evaluate((sel) => {
  const pop = document.querySelector("#pop"), r = document.querySelector(sel).getBoundingClientRect();
  const left = parseFloat(pop.style.left), width = parseFloat(pop.style.width);
  return { hidden: pop.hidden, left, right: left + width, width, top: parseFloat(pop.style.top), bLeft: r.left, bBottom: r.bottom, w: innerWidth };
}, button);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an open picker follows a resize", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const page = await (await browser.newContext({ viewport: { width: 1440, height: 800 } })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/");
        await page.locator(button).click();
        await page.locator("#pop:not([hidden]) #list li").first().waitFor();
        const wide = await where(page);
        assert(wide.right <= wide.w, "placed inside the wide window");

        // out of full screen: narrower, the button moves left (and the
        // row settles for a few frames); the pop comes to rest by it
        await page.setViewportSize({ width: 840, height: 800 });
        const settled = (sel) => {
          const pop = document.querySelector("#pop"), r = document.querySelector(sel).getBoundingClientRect();
          const left = parseFloat(pop.style.left), width = parseFloat(pop.style.width);
          return !pop.hidden && left >= 8 && left + width <= innerWidth - 8
            && Math.abs(left - Math.max(8, Math.min(r.left, innerWidth - width - 8))) < 1
            && Math.abs(parseFloat(pop.style.top) - (r.bottom + 5)) < 1;
        };
        try {
          await page.waitForFunction(settled, button, { timeout: 3000 });
        } catch {
          assert.fail("the pop is not by its button in the narrow window: " + JSON.stringify(await where(page)));
        }
        // and stays there once the row's moves are done
        await page.evaluate(() => Promise.all(document.getAnimations().map((a) => a.finished.catch(() => {}))));
        await page.evaluate(() => new Promise((ok) => requestAnimationFrame(() => requestAnimationFrame(ok))));
        assert(await page.evaluate(settled, button), "still by its button: " + JSON.stringify(await where(page)));
        const narrow = await where(page);
        assert.notEqual(Math.round(narrow.bLeft), Math.round(wide.bLeft), "the button moved");

        // a window too short to show the button: the picker closes
        await page.evaluate((sel) => { const b = document.querySelector(sel); return b.getBoundingClientRect().top; }, button)
          .then((top) => page.setViewportSize({ width: 840, height: Math.max(40, Math.floor(top) - 20) }));
        await page.waitForFunction(() => document.querySelector("#pop").hidden);
        assert.equal(await page.locator(`${button}.open`).count(), 0, "the button no longer open");
        await page.context().close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
