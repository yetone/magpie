// Run with Node's test runner and Playwright on the module path; see README.md.
// The Claude Code editor's one "Claude Code runs as" row (Anotiaww on X: it
// was disabled in magpie web as a gateway while a second row, "interactive
// mode", beside it did the same job). One row, TUI or claude -p, in the app
// and in magpie web as a gateway; both choices can be pressed; pressing
// claude -p saves claudePrintMode with the rest of the settings kept and the
// page not moved, and TUI turns it back. In English, Chinese, Japanese and
// German, 1100 and 420 wide.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { provPrefsProviders, openProvPrefs, inEditor } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");


function serve(lang, saved, gateway) {
  const settings = { lang, theme: "light", redact: true, gatewayOn: gateway };
  const state = { agents: [], profiles: [], settings };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true,gateway:${gateway}};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/settings") {
      if (r.request().method() === "POST") {
        saved.push(JSON.parse(r.request().postData()));
        Object.assign(settings, saved.at(-1));
      }
      return json(settings);
    }
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json(provPrefsProviders(["claude"], { claude: "Claude Code" }));
    if (url.pathname === "/api/claude/mode") return json({ tui: !settings.claudePrintMode, why: settings.claudePrintMode ? "off" : "" });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) for (const width of [1100, 420]) {
    for (const gateway of [false, true]) {
      test(`${engine} ${lang} ${width}${gateway ? " gateway" : ""}: the Claude Code mode is one row that switches`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 } })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const saved = [];
        await page.route("**/*", serve(lang, saved, gateway));
        await openProvPrefs(page, "claude");
        const row = page.locator(".editor .claude-mode");
        await row.locator(".cc-pick").waitFor();
        assert.equal(await page.locator(".editor #claudeTuiRow").count(), 0, "no second row for the same setting");
        assert.equal(await row.locator(".cc-pick .opt").count(), 2, "TUI and claude -p");
        assert.deepEqual(await row.locator(".cc-pick .opt").evaluateAll((bs) => bs.map((b) => b.disabled)), [false, false], "both can be pressed");
        const pressed = () => row.locator(".cc-pick .opt").evaluateAll((bs) => bs.map((b) => b.getAttribute("aria-pressed")));
        assert.deepEqual(await pressed(), ["true", "false"], "TUI at first");

        const before = await page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]);
        await row.locator(".cc-pick .opt").nth(1).click();
        for (let i = 0; i < 50 && !saved.length; i++) await page.waitForTimeout(50);
        assert.equal(saved.length, 1, "saved once");
        assert.equal(saved[0].claudePrintMode, true);
        assert.equal(saved[0].redact, true, "the rest kept");
        assert.equal(saved[0].lang, lang);
        assert.deepEqual(await page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop]), before, "the click moved nothing");
        await page.waitForFunction(() => document.querySelectorAll(".editor .claude-mode .cc-pick .opt")[1]?.getAttribute("aria-pressed") === "true");
        await row.locator(".cc-pick .opt").first().click();
        for (let i = 0; i < 50 && saved.length < 2; i++) await page.waitForTimeout(50);
        assert.equal(saved.length, 2, "saved again");
        assert.equal(saved[1].claudePrintMode, false, "-p turned back off");
        await page.waitForFunction(() => document.querySelectorAll(".editor .claude-mode .cc-pick .opt")[0]?.getAttribute("aria-pressed") === "true");
        assert.deepEqual(errors, []);
      });
    }
  }
}
