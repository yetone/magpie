// Run with Node's test runner and Playwright on the module path; see README.md.
// The "Provider in model names" setting's third way (#92: 自定义名称后面又带上了
// · 供应商): "Not on names I set" between Off and On, lit when settings say
// plainOwnNames; On and then it post settings/plain-names with mode on and
// own on their own, neither click scrolls the Settings page, and the three
// fit the row, wide and narrow. English and Chinese, Chromium and WebKit; no
// backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function settingsPayload(over) {
  return {
    theme: "light", lang: "en", tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

function server(lang, posted) {
  let cur = settingsPayload({ lang, plainOwnNames: true });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: cur.fx });
    if (url.pathname === "/api/settings/plain-names") {
      const body = req.postDataJSON();
      posted.push(body);
      cur = { ...cur, plainNames: body.mode === "off", plainOwnNames: body.mode === "own" };
      return json(cur);
    }
    if (url.pathname === "/api/settings") return json(cur);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const want = {
  en: { labels: ["Off", "Not on names I set", "On"], sub: /^Agents’ lists put each model’s provider after its name, or not after names you set$/ },
  zh: { labels: ["关闭", "自定义名称不带供应商", "开启"], sub: /^写给 agent 的模型列表在模型名后带上供应商，或自定义的名称不带$/ },
};
const view = (page) => page.locator("#view-settings").evaluate((v) => v.scrollTop);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a name the user gave can go without its provider`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 900, height: 480 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      const errors = [], posted = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, posted));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-model-suffix-own.png`) });
        }
        await browser.close();
      });
      await page.goto("http://magpie.test/?view=settings");
      const segs = page.locator("#plainNamesSegs .opt");
      await segs.first().waitFor();
      const row = page.locator(".row.pref", { has: page.locator("#plainNamesSegs") });
      assert.match(await row.locator(".sub").textContent(), want[lang].sub);
      assert.equal(await row.locator(".sub").evaluate((e) => e.scrollWidth <= e.clientWidth), true, "the hint is cut short");
      assert.deepEqual((await segs.allTextContents()).map((s) => s.trim()), want[lang].labels);
      const own = segs.nth(1);
      assert.equal(await own.evaluate((b) => b.classList.contains("on")), true, "plainOwnNames lights the middle one");

      // the three stay inside the row, wide and narrow
      for (const width of [900, 560]) {
        await page.setViewportSize({ width, height: 480 });
        const r = await row.boundingBox(), s = await page.locator("#plainNamesSegs .segs").boundingBox();
        assert(s.x >= r.x - 0.5 && s.x + s.width <= r.x + r.width + 0.5, `${width}px: the options overflow the row`);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, `${width}px: the page scrolls sideways`);
      }
      await page.setViewportSize({ width: 900, height: 480 });

      // scrolled by a wheel till the row is mid-view, On and then the
      // middle one move nothing and are posted on their own
      const box = await page.locator("#view-settings").boundingBox();
      await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
      const top = () => page.locator("#plainNamesSegs").evaluate((e) => e.getBoundingClientRect().top);
      for (let i = 0; i < 40 && (await top()) > 240; i++) { await page.mouse.wheel(0, 100); await page.waitForTimeout(30); }
      const before = await view(page);
      assert(before > 0, "the settings list must scroll to the row");
      await segs.nth(2).click();
      await page.locator("#plainNamesSegs .opt.on", { hasText: want[lang].labels[2] }).waitFor();
      assert.deepEqual(posted, [{ mode: "on" }]);
      await page.locator("#plainNamesSegs .opt", { hasText: want[lang].labels[1] }).click();
      await page.locator("#plainNamesSegs .opt.on", { hasText: want[lang].labels[1] }).waitFor();
      await page.waitForTimeout(400);
      assert.deepEqual(posted, [{ mode: "on" }, { mode: "own" }]);
      assert.equal(await view(page), before, "a click scrolled the page");
      assert.deepEqual(errors, []);
    });
  }
}
