// Run with Node's test runner and Playwright on the module path; see README.md.
// A model's favourite star (#482): not a favourite, an outline star shows on
// hover; once a favourite, a filled star shows always, and its title and
// aria-pressed say which. In the window and the tray panel, light and dark,
// English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = ["claude-sonnet-5-5", "claude-opus-5-5"].map((m) => ({ value: m, ref: "claude/" + m, label: "Label " + m }));
const agents = [{ id: "claude", name: "Claude Code", path: "/test/claude", wired: true, fields: [{ key: "model", label: "model", value: "claude-sonnet-5-5", options: models }] }];

function server(lang, theme) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents, profiles: [], settings: { lang, theme } });
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

const words = { en: { add: "Add to favorites", remove: "Remove from favorites" }, zh: { add: "添加到收藏", remove: "从收藏中移除" } };
const row = '.row.agent[data-id="claude"]';

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a favourite's star is filled", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) for (const theme of ["light", "dark"]) for (const panel of [false, true]) {
      await t.test(`${lang} ${theme} ${panel ? "tray panel" : "window"}`, async () => {
        const w = words[lang];
        const page = await (await browser.newContext({ viewport: panel ? { width: 440, height: 560 } : { width: 980, height: 600 } })).newPage();
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, theme));
        await page.goto("http://magpie.test/" + (panel ? "?mode=panel" : ""));
        await page.locator(row).waitFor();
        if (panel) {
          await page.locator(`${row} .ag-sum`).click();
          await page.waitForTimeout(700);
          await page.locator(`${row} .ag-open .field[data-key="model"]`).click();
        } else {
          // the window: beside the connected agent's switch
          await page.locator(`${row} > .field.ag-start[data-key="model"]`).click();
        }
        const li = page.locator("#pop:not([hidden]) #list li").filter({ hasText: "Label claude-opus-5-5" }).first();
        await li.waitFor();
        const star = li.locator(".favorite");
        const look = () => star.evaluate((b) => {
          const p = b.querySelector("path"), cs = getComputedStyle(p);
          return { shown: getComputedStyle(b).display !== "none", fill: cs.fill, color: getComputedStyle(b).color, pressed: b.getAttribute("aria-pressed"), title: b.title };
        });
        await li.hover();
        let s = await look();
        assert(s.shown, "the star shows on hover");
        assert.equal(s.fill, "none", "not a favourite: an outline star");
        assert.equal(s.pressed, "false");
        assert.equal(s.title, w.add);

        await star.click();
        await page.mouse.move(2, 2);
        await page.waitForTimeout(100);
        s = await look();
        assert(s.shown, "a favourite's star shows without hover");
        assert.notEqual(s.fill, "none", "a favourite: a filled star");
        assert.equal(s.fill, s.color, "filled in the star's own colour");
        assert.equal(s.pressed, "true");
        assert.equal(s.title, w.remove);

        await li.hover();
        await star.click();
        s = await look();
        assert.equal(s.fill, "none", "unfavourited: the outline again");
        assert.equal(s.pressed, "false");
        await page.context().close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
