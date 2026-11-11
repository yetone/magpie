// Run with Node's test runner and Playwright on the module path; see README.md.
// One longest context for every model (settings.contextCap, WJaunita45604
// on X): Settings > Models > Routing has a Longest context field, empty
// ("None") by default, no secondary text. A size typed (272k, 1m) and
// committed with Enter posts settings/context-cap on its own and shows as
// set; empty or "off" posts 0; something that is not a number of tokens is
// refused with nothing posted; no typing scrolls the page.
// English and Chinese, Chromium and WebKit; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, wheelTo } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function server(lang, posted) {
  let cur = { theme: "light", lang, tray: "panel", currency: "usd", version: "0.1.900", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425", proxyNow: "none", proxySource: "none", visionModels: [], imageGenModels: [], titleModels: [], workbuddyCheckins: [], lanURLs: [], redactWords: [], trayUsageEvery: 3, fx: { rate: 7.2, at: new Date().toISOString(), stale: false } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: cur.fx });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/settings/context-cap") {
      const body = req.postDataJSON();
      posted.push(["context-cap", body]);
      cur = { ...cur, contextCap: body.at || undefined };
      return json(cur);
    }
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") posted.push(["settings", req.postDataJSON()]);
      return json(cur);
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const want = {
  en: { name: "Longest context", none: "None", bad: "Longest context: lots is not a number of tokens like 272k" },
  zh: { name: "最长上下文", none: "无", bad: "最长上下文：lots 不是 272k 这样的 token 数" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(want)) {
    const w = want[lang];
    test(`${engine} ${lang}: one longest context for every model`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 420 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posted = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, posted));
      await watchSettingsReads(page);
      await page.goto("http://magpie.test/?view=settings&tab=models");
      await settingsDrawn(page);
      const row = page.locator("#contextCapRow"), field = row.locator("input.context-cap");
      await field.waitFor();
      assert.equal((await row.locator(".name").textContent()).trim(), w.name);
      assert.equal(await row.locator(".sub").count(), 0, "no secondary text");
      assert.equal(await field.getAttribute("placeholder"), w.none);
      assert.equal(await field.inputValue(), "");
      assert.equal(await row.locator("select").count(), 0, "no native select");
      const view = () => page.locator("#view-settings").evaluate((v) => v.scrollTop);
      await wheelTo(page, field, 160); // in sight first, so Playwright's own scrolling isn't counted
      const before = await view();
      await field.fill("lots");
      await field.press("Enter");
      await page.getByText(w.bad).waitFor();
      assert.deepEqual(posted, []);
      await row.locator("input.context-cap").fill("272k");
      await row.locator("input.context-cap").press("Enter");
      await page.waitForFunction(() => document.querySelector("#contextCapRow input")?.value === "272k" && !document.querySelector("#contextCapRow input:focus"));
      assert.deepEqual(posted, [["context-cap", { at: 272000 }]]);
      await row.locator("input.context-cap").fill("off");
      await row.locator("input.context-cap").press("Enter");
      await page.waitForFunction(() => document.querySelector("#contextCapRow input")?.value === "");
      assert.deepEqual(posted.at(-1), ["context-cap", { at: 0 }]);
      assert(!posted.some((p) => p[0] === "settings"), "set on its own, not with the other preferences");
      assert.equal(await view(), before, "typing scrolled the page");
      assert(await row.boundingBox().then((b) => b.height < 80), "the row stays one line");
      assert.deepEqual(errors, []);
    });
  }
}
