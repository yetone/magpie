// The Mac settings page lists registered terminal apps and keeps the choice
// when another preference is saved. The API is faked; no terminal is opened.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const terminalApps = [
  { id: "com.apple.Terminal", name: "Terminal" },
  { id: "com.mitchellh.ghostty", name: "Ghostty" },
];

function serve(lang, posts, terminalDefault = "com.apple.Terminal") {
  let settings = {
    theme: "light", lang, tray: "panel", sessionTerminal: "", terminalApps,
    terminalDefault, version: "test", dir: "/tmp/magpie",
    gateway: "http://127.0.0.1:3425", visionModels: [], imageGenModels: [],
    fx: { rate: 7.2, stale: false },
  };
  return async (route) => {
    const request = route.request();
    const pathname = new URL(request.url()).pathname;
    const json = (data) => route.fulfill({ json: data });
    if (pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (pathname === "/api/settings") {
      if (request.method() === "POST") {
        const body = request.postDataJSON();
        posts.push(body);
        settings = { ...settings, ...body };
      }
      return json(settings);
    }
    if (pathname === "/api/usage/quotas") return json([]);
    if (pathname === "/api/groups") return json({ groups: [], models: [] });
    if (pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, pathname === "/" ? "index.html" : pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: session terminal preference`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" });
      await context.addInitScript(() => Object.defineProperty(navigator, "platform", { get: () => "MacIntel" }));
      const page = await context.newPage();
      const errors = [], posts = [];
      page.on("pageerror", (error) => errors.push(error.message));
      await page.route("**/*", serve(lang, posts));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-session-terminal.png`) });
        }
        await browser.close();
      });

      await page.goto("http://magpie.test/");
      await page.locator("#prefs").click();
      const row = page.locator("#sessionTerminalRow");
      await row.waitFor({ state: "visible" });
      const select = page.locator("#sessionTerminalSelect");
      assert.deepEqual(await select.locator("option").allTextContents(),
        lang === "zh" ? ["系统默认（Terminal）", "Ghostty"] : ["System default (Terminal)", "Ghostty"]);
      assert.equal(await select.inputValue(), "system");

      await select.selectOption("com.mitchellh.ghostty");
      await page.waitForFunction(() => document.querySelector("#sessionTerminalSelect").value === "com.mitchellh.ghostty");
      await page.locator("#themeSegs .opt").last().click();
      await page.waitForFunction(() => document.documentElement.dataset.theme === "dark");
      assert.equal(posts.at(-1).sessionTerminal, "com.mitchellh.ghostty");
      await page.reload();
      await page.locator("#prefs").click();
      await row.waitFor({ state: "visible" });
      assert.equal(await select.inputValue(), "com.mitchellh.ghostty");
      const savedSystem = page.waitForResponse((response) => response.url().endsWith("/api/settings") && response.request().method() === "POST");
      await select.selectOption("system");
      await savedSystem;
      assert.equal(posts.at(-1).sessionTerminal, "");
      await page.reload();
      await page.locator("#prefs").click();
      await row.waitFor({ state: "visible" });
      assert.equal(await select.inputValue(), "system");
      await page.setViewportSize({ width: 520, height: 700 });
      assert.equal(await row.evaluate((element) => element.scrollWidth > element.clientWidth), false);
      assert.deepEqual(errors, []);
    });

    // an editor as the .command default: the server opens Terminal, and says so
    test(`${engine} ${lang}: an editor as the default reads as Terminal`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const context = await browser.newContext({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" });
      await context.addInitScript(() => Object.defineProperty(navigator, "platform", { get: () => "MacIntel" }));
      const page = await context.newPage();
      await page.route("**/*", serve(lang, [], "com.apple.TextEdit"));
      await page.goto("http://magpie.test/");
      await page.locator("#prefs").click();
      await page.locator("#sessionTerminalRow").waitFor({ state: "visible" });
      assert.deepEqual(await page.locator("#sessionTerminalSelect option").allTextContents(),
        lang === "zh" ? ["系统默认（Terminal）", "Ghostty"] : ["System default (Terminal)", "Ghostty"]);
    });
  }
}
