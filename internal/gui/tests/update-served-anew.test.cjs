// Run with Node's test runner and Playwright on the module path; see README.md.
// visamzh on X: the app's window and magpie web open together. One restarted
// to update; the other's page ran on as the old one, its Update pill still
// offering the version already in. A browser's page (magpie web, the app's
// page) now reloads once another version of magpie answers in its place:
// when it is clicked back into, and, left on screen, within a minute. The
// app's own window doesn't, its process making a new one. Every language,
// both engines, wide and narrow; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const langs = ["en", "zh", "zh-TW", "ja", "de"];

function settingsPayload(lang) {
  return {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
  };
}

// ctl.version is the magpie answering: 0.1.400 with 0.1.401 downloaded,
// until the other side's update has it restart as 0.1.401
function server(lang, web, ctl) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/") ctl.pages++;
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web},version:"${ctl.version}"};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settingsPayload(lang), fx: settingsPayload(lang).fx });
    if (url.pathname === "/api/settings") return json(settingsPayload(lang));
    if (url.pathname === "/api/update") {
      // the badge's and the Version row's reads answer a few ms apart, as
      // on a busy machine: a page that reloads for each one is loaded twice
      if (ctl.version !== "0.1.400" && ctl.reads++) await new Promise((r) => setTimeout(r, 5));
      return json(ctl.version === "0.1.400"
        ? { state: "ready", current: "0.1.400", latest: "0.1.401" }
        : { state: "latest", current: "0.1.401", latest: "0.1.401" });
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png", ".json": "application/json" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const pill = (page) => page.locator("#update");

async function open(browser, width, lang, web, ctl, clock) {
  const errors = [];
  const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
  if (clock) await page.clock.install();
  await page.route("**/*", server(lang, web, ctl));
  await page.goto("http://magpie.test/?view=settings");
  await pill(page).waitFor({ state: "visible" });
  await page.evaluate(() => { window.oldPage = true; });
  return { page, errors };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a page whose magpie restarted into another's update reloads", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const width of [1100, 420]) for (const lang of langs) {
      await t.test(`${lang} at ${width}: clicked back into`, async () => {
        const ctl = { version: "0.1.400", pages: 0, reads: 0 };
        const { page, errors } = await open(browser, width, lang, true, ctl);
        // the app's window put 0.1.401 in, and magpie web restarted into it
        ctl.version = "0.1.401";
        await page.evaluate(() => window.dispatchEvent(new Event("focus")));
        await page.waitForFunction(() => !window.oldPage && document.readyState === "complete");
        await page.waitForTimeout(400);
        assert.equal(await pill(page).isHidden(), true, "the reloaded page offers no update");
        assert.equal(ctl.pages, 2, "loaded again once");
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
    for (const lang of langs) {
      await t.test(`${lang}: left on screen, within a minute`, async () => {
        const ctl = { version: "0.1.400", pages: 0, reads: 0 };
        const { page, errors } = await open(browser, 1100, lang, true, ctl, true);
        ctl.version = "0.1.401";
        await page.clock.runFor(61e3);
        await page.waitForFunction(() => !window.oldPage, null, { polling: 50 });
        assert.equal(ctl.pages, 2);
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
    await t.test("the app's own window doesn't reload", async () => {
      const ctl = { version: "0.1.400", pages: 0, reads: 0 };
      const { page, errors } = await open(browser, 1100, "en", false, ctl);
      ctl.version = "0.1.401";
      await page.evaluate(() => window.dispatchEvent(new Event("focus")));
      await page.waitForTimeout(600);
      assert.equal(await page.evaluate(() => window.oldPage), true);
      assert.equal(ctl.pages, 1);
      assert.deepEqual(errors, []);
      await page.context().close();
    });
  });
}
