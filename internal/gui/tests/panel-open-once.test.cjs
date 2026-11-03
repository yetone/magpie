// Run with Node's test runner and Playwright on the module path; see README.md.
// The tray panel opens the main window from one place: the header's
// open-as-a-window icon. Its foot had an "Open magpie" button too, which did
// the same (yogurt36 on Discord); the foot keeps Profiles, the status line
// and Quit, which are its own. The icon is named for what it opens, the main
// window: isaac_bai on Discord looked for the foot's button and took the
// icon's "Open as a window" for this panel in a window.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function handler(lang, calls) {
  const state = { agents: [{ id: "codex", name: "codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/window/main" || url.pathname === "/api/window/quit") { calls.push(url.pathname); return route.fulfill({ status: 204 }); }
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: the tray panel opens the window from one button`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 380, height: 520 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(8000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const calls = [];
      await page.route("**/*", handler(lang, calls));
      await page.goto("http://magpie.test/?mode=panel");
      await page.locator("#agents .row.agent").first().waitFor();

      // the header's icon is named for what it opens, in the panel's language
      const name = { en: "Open the main window", zh: "打开主窗口", "zh-TW": "開啟主視窗", ja: "メインウィンドウを開く", de: "Hauptfenster öffnen" }[lang];
      assert.equal(await page.locator(".top #open").getAttribute("title"), name, "the header icon's name");
      assert.equal(await page.locator(".top").getByRole("button", { name, exact: true }).count(), 1, "found by its name");

      // the header's icon opens the window
      await page.locator(".top #open").click();
      for (let i = 0; i < 100 && !calls.length; i++) await page.waitForTimeout(50);
      assert.deepEqual(calls, ["/api/window/main"], "the header's icon opens the window");

      // and the foot has no second way to it: Profiles and Quit are its only buttons
      const foot = await page.locator(".foot button").evaluateAll((bs) => bs.map((b) => b.id));
      assert.deepEqual(foot, ["profBtn", "quit"], "the foot's buttons");

      // the foot keeps Quit, worded in the panel's language
      const quit = page.locator(".foot #quit");
      assert(await quit.isVisible(), "Quit stays in the foot");
      const want = { en: "Quit", zh: "退出", "zh-TW": "退出", ja: "終了", de: "Beenden" }[lang];
      assert.equal((await quit.textContent()).trim(), want);
      assert.equal(await page.locator("#openMain").count(), 0);
      assert.deepEqual(errors, []);
    });
  }
}
