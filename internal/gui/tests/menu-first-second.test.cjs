// Run with Node's test runner and Playwright on the module path; see README.md.
// A row menu opened in the page's first second closes on a second click on
// its button, and on a click outside it. The usage page's guard for a long
// press (quotaPressing) counted from 0 when no long press had happened, so
// for that first second it ate every click and mousedown outside a menu.
// The page's clock is set back 10s here, so the whole test is in that
// first second; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function server() {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang: "en", theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a menu opened in the page's first second closes", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    const ctx = await browser.newContext({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" });
    await ctx.addInitScript(() => { const now = performance.now.bind(performance); performance.now = () => now() - 10000; });
    const page = await ctx.newPage();
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", server());
    await page.goto("http://magpie.test/");
    // the usage page's script is in
    await page.waitForFunction(() => { try { return typeof quotaCardMenus === "function"; } catch { return false; } });
    let ran = 0;
    await page.exposeFunction("ranItem", () => { ran++; });
    await page.evaluate(() => {
      const b = document.createElement("button");
      b.id = "probe";
      b.textContent = "More";
      b.style.cssText = "position:fixed;left:40px;top:120px;z-index:99";
      b.onclick = (e) => {
        e.stopPropagation();
        if (b.classList.contains("open")) return closeAgentMenu();
        openRowMenu(b, [{ name: "One", literal: true, icon: DOTS, run: () => ranItem() }]);
      };
      document.body.append(b);
    });
    assert.ok(await page.evaluate(() => performance.now() < 1000), "the page is in its first second");
    const probe = page.locator("#probe"), menu = page.locator(".row-menu");

    // a second click on its button closes it
    await probe.click();
    await menu.waitFor();
    await probe.click();
    await menu.waitFor({ state: "detached" });

    // so does a click outside it
    await probe.click();
    await menu.waitFor();
    await page.mouse.click(700, 600);
    await menu.waitFor({ state: "detached" });

    // its item still runs
    await probe.click();
    await menu.locator(".rm-item").click();
    await menu.waitFor({ state: "detached" });
    assert.equal(ran, 1);

    assert.deepEqual(errors, []);
    await ctx.close();
  });
}
