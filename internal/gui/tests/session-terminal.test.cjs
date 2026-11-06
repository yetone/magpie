// The Mac settings page lists registered terminal apps and keeps the choice
// when another preference is saved. On Windows and Linux (lileo0668 on
// Discord) it lists the terminals the server found, and no row when it
// found none. The API is faked; no terminal is opened.
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

function serve(lang, posts, terminalDefault = "com.apple.Terminal", apps = terminalApps) {
  let settings = {
    theme: "light", lang, tray: "panel", sessionTerminal: "", terminalApps: apps,
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

// The Session terminal row wheeled into view as a reader would: rows keep
// being added above it, so it sits under the window's foot, and a click
// Playwright scrolls in for is put back (a scroll that isn't the reader's,
// #1554) and lands on the foot
async function reach(page) {
  const view = await page.locator("#view-settings").boundingBox();
  await page.mouse.move(view.x + view.width / 2, view.y + view.height / 2);
  const rest = () => page.evaluate(() => new Promise((done) => {
    const v = document.querySelector("#view-settings");
    let last = -1;
    const look = () => { if (v.scrollTop === last) done(); else { last = v.scrollTop; setTimeout(look, 100); } };
    look();
  }));
  for (let i = 0; i < 60; i++) {
    const b = await page.locator("#sessionTerminalSelect").boundingBox();
    if (b && b.y > view.y && b.y + b.height < view.y + view.height) return;
    await page.mouse.wheel(0, b && b.y < view.y ? -40 : 40);
    await rest();
  }
  assert.fail("the Session terminal row never came into view");
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
      await reach(page);
      // the app's own menu, not a native select
      const select = page.locator("#sessionTerminalSelect");
      assert.equal(await page.locator("#sessionTerminalRow select").count(), 0, "no native select");
      const names = async () => {
        await select.click();
        await page.locator(".proto-menu").waitFor();
        const got = (await page.locator(".proto-menu .pm-item .pm-name").allTextContents()).map((s) => s.trim());
        await select.click();
        assert.equal(await page.locator(".proto-menu").count(), 0, "a second click closes it");
        return got;
      };
      const choose = async (name) => {
        await select.click();
        await page.locator(".proto-menu .pm-item", { hasText: name }).click();
        assert.equal(await page.locator(".proto-menu").count(), 0, "a pick closes it");
      };
      assert.deepEqual(await names(), lang === "zh" ? ["系统默认（Terminal）", "Ghostty"] : ["System default (Terminal)", "Ghostty"]);
      assert.equal(await select.getAttribute("data-value"), "system");
      assert.equal((await select.textContent()).trim(), lang === "zh" ? "系统默认（Terminal）" : "System default (Terminal)");
      const top = await page.evaluate(() => document.scrollingElement.scrollTop);

      // the page is drawn again once the save is in; a Theme button from
      // before then still sends the page as it was drawn, the old terminal
      // with it, so the Theme click below waits for the new buttons, as a
      // reader can only click what is on the page
      await page.evaluate(() => document.querySelectorAll("#themeSegs .opt").forEach((b) => { b.dataset.before = "1"; }));
      await choose("Ghostty");
      assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), top, "the page doesn't move");
      await page.waitForFunction(() => document.querySelector("#sessionTerminalSelect").dataset.value === "com.mitchellh.ghostty");
      assert.equal((await select.textContent()).trim(), "Ghostty");
      await page.waitForFunction(() => !document.querySelector("#themeSegs .opt[data-before]"));
      // another setting saved (Theme, scrolled away above) keeps the terminal
      await page.locator("#themeSegs .opt").last().evaluate((b) => b.click());
      await page.waitForFunction(() => document.documentElement.dataset.theme === "dark");
      assert.equal(posts.at(-1).sessionTerminal, "com.mitchellh.ghostty");
      await page.reload();
      await page.locator("#prefs").click();
      await row.waitFor({ state: "visible" });
      await reach(page);
      assert.equal(await select.getAttribute("data-value"), "com.mitchellh.ghostty");
      const savedSystem = page.waitForResponse((response) => response.url().endsWith("/api/settings") && response.request().method() === "POST");
      await choose(lang === "zh" ? "系统默认" : "System default");
      await savedSystem;
      assert.equal(posts.at(-1).sessionTerminal, "");
      await page.reload();
      await page.locator("#prefs").click();
      await row.waitFor({ state: "visible" });
      await reach(page);
      assert.equal(await select.getAttribute("data-value"), "system");
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
      await reach(page);
      await page.locator("#sessionTerminalSelect").click();
      assert.deepEqual((await page.locator(".proto-menu .pm-item .pm-name").allTextContents()).map((s) => s.trim()),
        lang === "zh" ? ["系统默认（Terminal）", "Ghostty"] : ["System default (Terminal)", "Ghostty"]);
    });
  }

  // Windows and Linux: the terminals found, the system's named; no Terminal.app
  const own = {
    Win32: { apps: [{ id: "wt", name: "Windows Terminal" }, { id: "pwsh", name: "PowerShell" }, { id: "powershell", name: "Windows PowerShell" }], def: "powershell", pick: "wt", zh: ["系统默认（Windows PowerShell）", "Windows Terminal", "PowerShell"], en: ["System default (Windows PowerShell)", "Windows Terminal", "PowerShell"] },
    "Linux x86_64": { apps: [{ id: "xfce4-terminal", name: "Xfce Terminal" }, { id: "kitty", name: "kitty" }], def: "xfce4-terminal", pick: "kitty", zh: ["系统默认（Xfce Terminal）", "kitty"], en: ["System default (Xfce Terminal)", "kitty"] },
  };
  for (const [platform, o] of Object.entries(own)) {
    for (const lang of ["en", "zh"]) {
      test(`${engine} ${lang} ${platform}: the terminals found here`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const context = await browser.newContext({ viewport: { width: 420, height: 700 }, reducedMotion: "reduce" });
        await context.addInitScript((p) => Object.defineProperty(navigator, "platform", { get: () => p }), platform);
        const page = await context.newPage();
        const errors = [], posts = [];
        page.on("pageerror", (error) => errors.push(error.message));
        await page.route("**/*", serve(lang, posts, o.def, o.apps));
        await page.goto("http://magpie.test/");
        await page.locator("#prefs").click();
        const row = page.locator("#sessionTerminalRow");
        await row.waitFor({ state: "visible" });
        await reach(page);
        const select = page.locator("#sessionTerminalSelect");
        await select.click();
        await page.locator(".proto-menu").waitFor();
        assert.deepEqual((await page.locator(".proto-menu .pm-item .pm-name").allTextContents()).map((s) => s.trim()), o[lang]);
        const saved = page.waitForResponse((r) => r.url().endsWith("/api/settings") && r.request().method() === "POST");
        await page.locator(".proto-menu .pm-item").nth(1).click();
        await saved;
        assert.equal(posts.at(-1).sessionTerminal, o.pick);
        assert.equal(await row.evaluate((element) => element.scrollWidth > element.clientWidth), false, "fits 420px");
        assert.deepEqual(errors, []);
      });
    }
    test(`${engine} ${platform}: no terminal found, no row`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const context = await browser.newContext({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" });
      await context.addInitScript((p) => Object.defineProperty(navigator, "platform", { get: () => p }), platform);
      const page = await context.newPage();
      await page.route("**/*", serve("en", [], "", []));
      await page.goto("http://magpie.test/");
      await page.locator("#prefs").click();
      await page.locator("#themeSegs").waitFor();
      assert.equal(await page.locator("#sessionTerminalRow").isHidden(), true);
    });
  }
}
