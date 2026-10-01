// Run with Node's test runner and Playwright on the module path; see README.md.
// The "Provider in model names" setting (#335: 希望 Codex 模型列表里的显示名可以
// 不带 · routing group / · 提供商 后缀): on by default; Off posts
// settings/plain-names on its own (so the agents' lists are written again)
// with mode off and lights Off, On posts it back, and neither click scrolls
// the Settings page. English and Chinese, Chromium and WebKit; no backend, the API is faked here.
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
  let cur = settingsPayload({ lang });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: cur.fx });
    if (url.pathname === "/api/settings/plain-names") {
      const body = req.postDataJSON();
      posted.push(["plain-names", body]);
      cur = { ...cur, plainNames: body.mode === "off", plainOwnNames: body.mode === "own" };
      return json(cur);
    }
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") {
        const body = req.postDataJSON();
        posted.push(["settings", body]);
        cur = { ...cur, ...body };
      }
      return json(cur);
    }
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
  en: { name: "Provider in model names", sub: /provider after its name/, off: "Off", own: "Not on names I set", on: "On" },
  zh: { name: "模型名带供应商", sub: /自定义的名称不带/, off: "关闭", own: "自定义名称不带供应商", on: "开启" },
};
const view = (page) => page.locator("#view-settings").evaluate((v) => v.scrollTop);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: the agents' lists name models with their providers, or not`, async (t) => {
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
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-plain-names.png`) });
        }
        await browser.close();
      });
      await page.goto("http://magpie.test/?view=settings");
      const segs = page.locator("#plainNamesSegs .opt");
      await segs.first().waitFor();
      const row = page.locator(".row.pref", { has: page.locator("#plainNamesSegs") });
      assert.equal((await row.locator(".name").textContent()).trim(), want[lang].name);
      assert.match(await row.locator(".sub").textContent(), want[lang].sub);
      assert.deepEqual((await segs.allTextContents()).map((s) => s.trim()), [want[lang].off, want[lang].own, want[lang].on]);
      assert.equal(await segs.nth(2).evaluate((b) => b.classList.contains("on")), true, "on by default");

      // scrolled by a wheel till the row is mid-view (a real wheel, so the
      // reader's-scroll guard lets it stick), Off moves nothing and is
      // posted on its own
      const box = await page.locator("#view-settings").boundingBox();
      await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
      const top = () => page.locator("#plainNamesSegs").evaluate((e) => e.getBoundingClientRect().top);
      for (let i = 0; i < 40 && (await top()) > 240; i++) { await page.mouse.wheel(0, 100); await page.waitForTimeout(30); }
      const before = await view(page);
      assert(before > 0, "the settings list must scroll to the row");
      await segs.nth(0).click();
      await page.locator("#plainNamesSegs .opt.on", { hasText: want[lang].off }).waitFor();
      await page.waitForTimeout(400);
      assert.equal(await view(page), before, "the click scrolled the page");
      assert.deepEqual(posted, [["plain-names", { mode: "off" }]]);

      // back on
      await segs.nth(2).click();
      await page.locator("#plainNamesSegs .opt.on", { hasText: want[lang].on }).waitFor();
      assert.deepEqual(posted.at(-1), ["plain-names", { mode: "on" }]);
      assert.equal(await view(page), before, "the click scrolled the page");
      assert.deepEqual(errors, []);
    });
  }
}
