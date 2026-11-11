// Run with Node's test runner and Playwright on the module path; see README.md.
// A plugin's page is shown as broken for what its own code does wrong
// (pagebridge.js). A read of the window above it, which the sandboxed frame
// is shut out of ("Failed to read a named property 'origin' from 'Location':
// Blocked a frame with origin "null" ..."), or an error out of a script an
// extension put in the frame, is not the page's: onducoder opened Day card
// in magpie web 0.1.3702 and got "stopped working" over a page that was fine.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));
const bridge = path.resolve(process.env.MAGPIE_BRIDGE || path.join(__dirname, "../pagebridge.js"));

const tok = (c) => c.repeat(32);
const entry = (c, name) => ({ spec: "/x/" + name, name, key: "plugin-" + name, title: name, reads: [], url: `/_magpie/plugin/${tok(c)}/index.html` });
// WebKit hands a sandboxed page's uncaught error to its handler as a bare
// "Script error." with no error object, so only what arrives as a rejection
// can be told apart there (masked: true are Chromium's alone)
const CASES = {
  // the exact read from the report, uncaught
  parentOrigin: { masked: true, c: "a", broken: false, js: `setTimeout(() => { void parent.location.origin; });` },
  topOrigin: { masked: true, c: "b", broken: false, js: `setTimeout(() => { void top.location.origin; });` },
  rejected: { c: "c", broken: false, js: `Promise.resolve().then(() => parent.location.origin);` },
  extension: { masked: true, c: "d", broken: false, js: `setTimeout(() => { const e = new Error("inpage"); e.stack = "Error: inpage\\n    at chrome-extension://abcdef/inpage.js:1:1"; throw e; });` },
  extensionRejected: { c: "e", broken: false, js: `{ const e = new Error("inpage"); e.stack = "Error: inpage\\n    at moz-extension://abcdef/inpage.js:1:1"; Promise.reject(e); }` },
  // the storage the sandbox has none of (nivekxft: "Failed to read the
  // 'localStorage' property from 'Window': The document is sandboxed ...")
  storage: { masked: true, c: "1", broken: false, js: `setTimeout(() => { void localStorage.length; });` },
  storageRejected: { c: "2", broken: false, js: `Promise.resolve().then(() => sessionStorage.getItem("x"));` },
  // the page's own mistakes still break it
  own: { masked: true, c: "f", broken: true, js: `setTimeout(() => { throw new Error("the page's own bug"); });` },
  ownRejected: { c: "0", broken: true, js: `Promise.reject(new Error("the page's own bug"));` },
};
const pages = Object.entries(CASES).map(([name, v]) => entry(v.c, name.toLowerCase()));
const html = (js) => `<!doctype html><meta charset="utf-8"><script src="_magpie/bridge.js"></script><script>${js}
setTimeout(() => { window.done = magpie.options.get().then(() => true, () => true); }, 50);</script><p>page</p>`;

async function server(route) {
  const url = new URL(route.request().url());
  const json = (data) => route.fulfill({ json: data });
  if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:true};` });
  if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
  if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang: "en", theme: "light" } });
  if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
  if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
  if (url.pathname === "/api/plugin-pages") return json(pages);
  if (url.pathname === "/api/plugin-panels") return json([]);
  if (url.pathname.startsWith("/api/")) return json({});
  const m = url.pathname.match(/^\/_magpie\/plugin\/([0-9a-f]{32})\/(.+)$/);
  if (m) {
    const headers = { "X-Content-Type-Options": "nosniff", "Cache-Control": "no-cache" };
    if (m[2] === "_magpie/bridge.js") return route.fulfill({ headers: { ...headers, "Content-Type": "text/javascript" }, body: await fs.readFile(bridge) });
    const one = Object.values(CASES).find((v) => tok(v.c) === m[1]);
    if (m[2] === "index.html" && one) return route.fulfill({ headers: { ...headers, "Content-Type": "text/html" }, body: html(one.js) });
    return route.fulfill({ status: 404, body: "" });
  }
  if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
  const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
  try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a read the sandbox shuts out, or an extension's error, does not break a page; its own bug does`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 1100, height: 700 }, reducedMotion: "reduce" })).newPage();
    page.setDefaultTimeout(8000);
    page.setDefaultNavigationTimeout(60000);
    await page.route("**/*", server);
    await page.goto("http://magpie.test/");
    await page.locator("#nav button[data-plugin-page]").nth(pages.length - 1).waitFor({ state: "attached" });
    for (const [name, v] of Object.entries(CASES)) {
      if (engine === "webkit" && v.masked) continue;
      const key = name.toLowerCase();
      await page.evaluate((k) => show("plugin-" + k), key);
      const frame = () => page.frames().find((x) => x.url().includes(tok(v.c)));
      for (let i = 0; i < 100 && !frame(); i++) await page.waitForTimeout(50);
      assert.ok(frame(), name + ": the frame opened");
      await frame().waitForFunction(() => window.magpie?.theme).catch(() => {});
      // the window answers a frame's messages in order, so once this answer
      // is back, whatever the page raised before it has been heard
      if (!v.broken) {
        assert.ok(frame(), `${name}: the page's frame is still there`);
        await frame().evaluate(() => window.done);
      }
      else await page.locator(`#view-plugin-${key} .slot-error`).waitFor();
      const broke = await page.locator(`#view-plugin-${key} .slot-error`).count();
      assert.equal(broke, v.broken ? 1 : 0, `${name}: ${v.broken ? "its own bug breaks the page" : "the page is not broken"}`);
    }
  });
}
