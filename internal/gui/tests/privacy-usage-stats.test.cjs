// Run with Node's test runner and Playwright on the module path; see README.md.
// Privacy has a switch for the agents, providers and models the daily
// event names, on until turned off, below Count me as a user, and gone
// while that is off: it rides on that event. Turning it off saves
// noUsageStats, and a save of another setting keeps it.
// English and Chinese; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function serve(lang, settings, saved) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") { const b = req.postDataJSON(); saved.push(b); Object.assign(settings, b); }
      return json(settings);
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { count: "Count me as a user", share: "Share the agents, providers and models I use", personal: "Mask personal data", on: "On", off: "Off" },
  zh: { count: "计入用户数", share: "分享我用的 agent、provider 和模型", personal: "脱敏个人信息", on: "开启", off: "关闭" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: Privacy switches off sharing what magpie is used with`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const errors = [];
      const settings = { theme: "light", lang, tray: "panel", currency: "usd" }, saved = [];
      const context = await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, settings, saved));
      await page.goto("http://magpie.test/?view=settings&tab=privacy");
      const row = (name) => page.locator("#redactList .row", { has: page.locator(".name", { hasText: new RegExp("^" + name + "$") }) });
      const isOn = (name) => page.waitForFunction(([name, on]) => [...document.querySelectorAll("#redactList .row")].some((r) => r.querySelector(".name")?.textContent === name && r.querySelector(".opt.on")?.textContent === on), [name, w.on]);
      const isOff = (name) => page.waitForFunction(([name, off]) => [...document.querySelectorAll("#redactList .row")].some((r) => r.querySelector(".name")?.textContent === name && r.querySelector(".opt.on")?.textContent === off), [name, w.off]);

      // on by default, right below Count me as a user
      await isOn(w.share);
      const names = await page.$$eval("#redactList .row .name", (ns) => ns.map((n) => n.textContent));
      assert.equal(names.indexOf(w.share), names.indexOf(w.count) + 1, names.join(" | "));

      await row(w.share).getByRole("button", { name: w.off, exact: true }).click();
      await isOff(w.share);
      assert.equal(saved.at(-1).noUsageStats, true, JSON.stringify(saved));
      assert.ok(!saved.at(-1).noStats);

      // another setting saved keeps it off
      await row(w.personal).getByRole("button", { name: w.on, exact: true }).click();
      await isOn(w.personal);
      assert.equal(saved.at(-1).noUsageStats, true, JSON.stringify(saved.at(-1)));

      // not counted at all: nothing to share, no switch
      await row(w.count).getByRole("button", { name: w.off, exact: true }).click();
      await isOff(w.count);
      await row(w.share).waitFor({ state: "detached" });
      assert.equal(saved.at(-1).noStats, true);
      await context.close();
      assert.deepEqual(errors, []);
    });
  }
}
