// Run with Node's test runner and Playwright on the module path; see README.md.
// The day card's Copy picture (nivekxft on X, "分享图片失败: Cannot read
// properties of undefined (reading 'write')"): magpie web opened over plain
// http, or a webview, has no navigator.clipboard. The picture is then copied
// as a selected <img> by the page's copy command; where that fails too, the
// page gets a message that says to save it, not a TypeError.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function serve(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: copying a picture with no navigator.clipboard`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(8000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.addInitScript(() => {
        Object.defineProperty(Navigator.prototype, "clipboard", { get: () => undefined, configurable: true });
        window.__copied = [];
        const real = document.execCommand.bind(document);
        document.execCommand = (cmd, ...rest) => {
          if (cmd !== "copy") return real(cmd, ...rest);
          const sel = getSelection();
          const node = sel.rangeCount ? sel.getRangeAt(0).cloneContents() : null;
          window.__copied.push(!!node?.querySelector("img[src^='data:image/png']"));
          return window.__take !== false;
        };
      });
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/");
      await page.waitForFunction(() => typeof t === "function" && window.bootPrefs);
      const run = () => page.evaluate(async () => {
        await import("/pageshare.js");
        const c = document.createElement("canvas");
        c.width = 3; c.height = 2;
        const png = await new Promise((r) => c.toBlob(r, "image/png"));
        try { return { ok: await pageShare("/x/day", false, { png }) }; } catch (e) { return { err: e.message, name: e.name }; }
      });
      assert.equal(await page.evaluate(() => navigator.clipboard), undefined, "the fixture has no clipboard");

      // the copy command takes the picture
      const got = await run();
      assert.deepEqual(got, { ok: { ok: true } }, "copied without navigator.clipboard");
      assert.deepEqual(await page.evaluate(() => window.__copied), [true], "the picture was what was selected");
      assert.equal(await page.locator("div[contenteditable]").count(), 0, "the box is gone");

      // nothing takes it: a message that says what to do
      await page.evaluate(() => { window.__take = false; });
      const bad = await run();
      assert.ok(bad.err && !/undefined|reading/.test(bad.err), `a clear message, got ${bad.err}`);
      assert.equal(bad.err, await page.evaluate(() => t("This browser can't copy a picture on this address (it needs https or localhost). Save it instead.")));
      assert.notEqual(bad.name, "TypeError");
      if (lang === "zh") assert.match(bad.err, /保存/);
      assert.deepEqual(errors, []);
    });
  }
}
