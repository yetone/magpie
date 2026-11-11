// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › General › Sidebar pages (youimark on X: a docker gateway has no
// use for Agents or Library): the pages ticked off leave the sidebar, are
// kept for this browser (localStorage magpie.navHidden), and the window
// that would open on one opens Providers. Chromium and WebKit. No backend:
// the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const BASE = "http://magpie.test/";

function server({ web = false, gateway = false } = {}) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (d) => route.fulfill({ json: d });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:${web}${gateway ? ",gateway:true" : ""}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang: "en", theme: "light" } });
    if (url.pathname === "/api/settings") return json({ theme: "light", lang: "en", version: "0.1.400", gateway: "http://127.0.0.1:3425", web, gatewayOn: web && gateway, visionModels: [], imageGenModels: [], lanURLs: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}


for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a page left out of the sidebar stays out, for this browser`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const context = await browser.newContext({ viewport: { width: 1100, height: 700 }, reducedMotion: "reduce" });
    const page = await context.newPage();
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", server());
    const hidden = () => page.evaluate(() => [...document.querySelectorAll("#nav button[data-view]")].filter((b) => b.hidden).map((b) => b.dataset.view));
    await page.goto(BASE + "?view=settings");
    await page.waitForSelector("#navPagesRow:not([hidden]) .nav-pages-pick");
    assert.deepEqual(await hidden(), [], "all shown at first");
    assert.match(await page.locator(".nav-pages-pick").innerText(), /All shown/);
    await page.locator(".nav-pages-pick").click();
    await page.locator(".pm-item", { hasText: /^Agents/ }).click();
    await page.locator(".pm-item", { hasText: /^Library/ }).click();
    await page.keyboard.press("Escape");
    assert.deepEqual((await hidden()).sort(), ["agents", "library"]);
    assert.match(await page.locator(".nav-pages-pick").innerText(), /2 hidden/);
    // kept: a reload, and the window opening on Agents, gives way to Providers
    await page.evaluate(() => localStorage.removeItem("magpie.lastView")); // as a window opened fresh
    await page.goto(BASE);
    await page.waitForSelector("#nav button.on");
    assert.deepEqual((await hidden()).sort(), ["agents", "library"]);
    assert.equal(await page.evaluate(() => document.querySelector("#nav button.on").dataset.view), "providers");
    // and put back
    await page.goto(BASE + "?view=settings");
    await page.waitForSelector(".nav-pages-pick");
    await page.locator(".nav-pages-pick").click();
    await page.locator(".pm-item", { hasText: /^Agents/ }).click();
    await page.keyboard.press("Escape");
    assert.deepEqual(await hidden(), ["library"]);
    assert.deepEqual(errors, []);
  });
}
