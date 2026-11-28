// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › General › Sidebar pages (youimark on X: a docker gateway has no
// use for Agents or Library): the pages ticked off leave the sidebar, are
// kept for this browser (localStorage magpie.navHidden), and the window
// that would open on one opens Providers. A link to one still opens it.
// A plugin's page left out stays out after a reload too (dolacmeo on X:
// its tab is made after the window's start, and came back; #7335), and a
// plugin's page in a built-in's place stays beside it in the reader's
// order (from eisen0419's 9997313c). Chromium and WebKit. No backend: the
// API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const BASE = "http://magpie.test/";

const pluginPage = (name) => ({ spec: "/x/" + name, name, key: "plugin-" + name, title: name, reads: [], url: `/_magpie/plugin/${"a".repeat(32)}/${name}.html` });

function server({ web = false, gateway = false, plugins = [], held = null } = {}) {
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/navorder.js" && held) await held;
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
    if (url.pathname === "/api/plugin-pages") return json(plugins.map((p) => typeof p === "string" ? pluginPage(p) : { ...pluginPage(p.name), ...p }));
    if (url.pathname === "/api/plugin-panels") return json([]);
    if (url.pathname.startsWith("/_magpie/")) return route.fulfill({ contentType: "text/html", body: "<!doctype html><p>page</p>" });
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
    // still reachable: a link or ?view= to a page left out opens it, not Agents
    // or Providers; only the window's own kept page gives way
    const on = () => page.evaluate(() => [...document.querySelectorAll(".view")].find((v) => !v.hidden)?.id);
    await page.evaluate(() => show("library"));
    await page.waitForFunction(() => !document.querySelector("#view-library").hidden);
    assert.equal(await on(), "view-library");
    await page.goto(BASE + "?view=library");
    await page.waitForFunction(() => !document.querySelector("#view-library")?.hidden);
    assert.equal(await on(), "view-library");
    await page.evaluate(() => localStorage.setItem("magpie.lastView", "library"));
    await page.goto(BASE);
    await page.waitForSelector("#nav button.on");
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

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a plugin's page left out of the sidebar stays out after a reload`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const context = await browser.newContext({ viewport: { width: 1100, height: 700 }, reducedMotion: "reduce" });
    const page = await context.newPage();
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", server({ plugins: ["alpha", "beta"] }));
    const hidden = () => page.evaluate(() => [...document.querySelectorAll("#nav button[data-view]")].filter((b) => b.hidden).map((b) => b.dataset.view).sort());
    const tabsIn = () => page.waitForSelector('#nav button[data-view="plugin-beta"]', { state: "attached" });
    await page.goto(BASE + "?view=settings");
    await tabsIn();
    await page.waitForSelector("#navPagesRow:not([hidden]) .nav-pages-pick");
    assert.deepEqual(await hidden(), [], "all shown at first");
    await page.locator(".nav-pages-pick").click();
    await page.locator('.nav-pages-menu .pm-item[data-v="plugin-alpha"]').click();
    await page.locator('.nav-pages-menu .pm-item[data-v="sessions"]').click();
    await page.keyboard.press("Escape");
    assert.deepEqual(await hidden(), ["plugin-alpha", "sessions"], "hidden at once");
    // a reload: the plugin's tab is made again, after the window's start
    await page.goto(BASE);
    await tabsIn();
    assert.deepEqual(await hidden(), ["plugin-alpha", "sessions"], "still hidden after a reload");
    assert.match(await page.locator("#nav").innerText(), /beta/);
    assert.doesNotMatch(await page.locator("#nav").innerText(), /alpha/);
    // the plugin pages read again (a plugin added, switched on or off)
    await page.evaluate(() => window.loadPluginPages());
    await page.waitForTimeout(300);
    assert.deepEqual(await hidden(), ["plugin-alpha", "sessions"], "still hidden after the pages are read again");
    // the window left on a plugin's page left out opens where it would
    // otherwise, as a built-in page left out does; a link still opens it
    await page.evaluate(() => localStorage.setItem("magpie.lastView", "plugin-alpha"));
    await page.goto(BASE);
    await tabsIn();
    await page.waitForTimeout(300);
    assert.equal(await page.evaluate(() => view), "agents");
    await page.goto(BASE + "?view=plugin-alpha");
    await page.waitForFunction(() => view === "plugin-alpha");
    assert.deepEqual(await hidden(), ["plugin-alpha", "sessions"]);
    // put back, it is in the sidebar again
    await page.goto(BASE + "?view=settings");
    await tabsIn();
    await page.locator(".nav-pages-pick").click();
    await page.locator('.nav-pages-menu .pm-item[data-v="plugin-alpha"]').click();
    await page.keyboard.press("Escape");
    assert.deepEqual(await hidden(), ["sessions"]);
    await page.goto(BASE);
    await tabsIn();
    assert.deepEqual(await hidden(), ["sessions"]);
    assert.deepEqual(errors, []);
  });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a kept order put on after the plugin tabs keeps a page in a built-in's place beside it`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const context = await browser.newContext({ viewport: { width: 1100, height: 700 }, reducedMotion: "reduce" });
    const page = await context.newPage();
    page.setDefaultTimeout(8000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    const ORDER = ["routing", "providers", "agents", "sessions", "library", "artifacts", "groups", "gateway", "usage", "masking", "plugins"];
    await page.addInitScript((o) => { try { localStorage.setItem("magpie.navOrder", JSON.stringify(o)); localStorage.setItem("magpie.navHidden", JSON.stringify(["plugin-glance"])); } catch { /* a frame has none */ } }, ORDER);
    // navorder.js held until the plugin tabs are drawn: the order is put on after them
    let go;
    const held = new Promise((r) => { go = r; });
    await page.route("**/*", server({ plugins: ["glance", { name: "repl", title: "Instead of Usage", replaces: "usage" }], held }));
    await page.goto(BASE, { waitUntil: "domcontentloaded" });
    await page.waitForSelector('#nav button[data-view="plugin-repl"]', { state: "attached" });
    await page.waitForFunction(() => document.querySelector('#nav button[data-view="plugin-glance"]')?.hidden === true);
    go();
    await page.waitForFunction((want) => [...document.querySelectorAll("#nav > button[data-view]:not([data-plugin-page])")].map((b) => b.dataset.view).join() === want.join(), ORDER);
    const seq = await page.evaluate(() => [...document.querySelectorAll("#nav > button[data-view]")].map((b) => b.dataset.view));
    assert.equal(seq[seq.indexOf("usage") + 1], "plugin-repl", "the page in Usage's place stays beside it: " + seq.join());
    assert.equal(await page.evaluate(() => document.querySelector('#nav button[data-view="plugin-glance"]').hidden), true);
    await page.evaluate(() => show("settings"));
    await page.waitForSelector("#navPagesRow:not([hidden]) .nav-pages-pick");
    assert.match(await page.locator(".nav-pages-pick").innerText(), /1 hidden/);
    assert.deepEqual(errors, []);
  });
}
