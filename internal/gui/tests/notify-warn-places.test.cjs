// Run with Node's test runner and Playwright on the module path; see README.md.
// When the system blocks magpie's notifications, or can't show them, every
// row that sets an alert says so beside it: Settings › Usage's alerts did,
// the Masking alert in Settings › Privacy didn't, and on a computer with
// a pointer both were held out of sight with the line explaining the row,
// for the card the pointer brings. And Bark's row says it gets every
// notification magpie shows, even while the system's are off, since
// sendAs pushes to Bark whatever the system does with it. Every
// language, wide and narrow, both engines; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function server(lang, notifyProblem) {
  const cur = {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", dock: false, dockWindow: false, proxy: "",
    redact: true, redactPersonal: false, redactWords: [], codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "",
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "", version: "0.1.900", dir: "~/.config/magpie",
    gateway: "http://127.0.0.1:3425", proxyNow: "none", proxySource: "none", visionModels: [], imageGenModels: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false }, turnDoneAlert: 0, featuresOff: [],
    maskAlert: true, usageAlert: 80, notifyProblem,
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false,featuresOff:[]};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: cur.fx });
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

const words = {
  en: { denied: "Notifications are turned off for magpie in the system's settings", unavailable: "Notifications can't be shown on this system",
    bark: "Every notification magpie shows goes here too, even while the system's notifications are off" },
  zh: { denied: "系统设置里关闭了 magpie 的通知", bark: "magpie 显示的每条通知也会发到这里，系统通知关闭时也照发" },
  "zh-TW": { denied: "系統設定裡關閉了 magpie 的通知", bark: "magpie 顯示的每則通知也會發到這裡，系統通知關閉時也照發" },
  ja: { denied: "システム設定で magpie の通知がオフになっています", bark: "magpie が表示する通知はすべてここにも届きます。システムの通知がオフでも届きます" },
  de: { denied: "Benachrichtigungen für magpie sind in den Systemeinstellungen ausgeschaltet",
    bark: "Jede Benachrichtigung von magpie kommt auch hierher, auch wenn die des Systems aus sind" },
};

// shown on the page: a line held out of sight for the hover card is 1px
const seen = (page, sel) => page.locator(sel).evaluate((e) => {
  const r = e.getBoundingClientRect();
  return r.width > 40 && r.height > 8 && getComputedStyle(e.closest(".sub")).clipPath === "none";
});

async function open(browser, width, lang, problem, tab) {
  const context = await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" });
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", server(lang, problem));
  await watchSettingsReads(page);
  await page.goto("http://magpie.test/?view=settings&tab=" + tab);
  await settingsDrawn(page);
  return { page, errors, context };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of (process.env.LANGS ? process.env.LANGS.split(",") : Object.keys(words))) {
    test(`${engine} ${lang}: every alert row says when notifications won't show, and Bark says it gets them all`, async (t) => {
      const w = words[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const width of [900, 380]) {
        await t.test(`${width}px`, async () => {
          // blocked: the Masking alert says so, as the usage alert does
          let { page, errors, context } = await open(browser, width, lang, "denied", "privacy");
          await page.locator("#maskAlertRow .sub").waitFor();
          assert.equal(await page.locator("#maskAlertRow .sub .warn").textContent(), w.denied, "masking alert");
          // in sight, not held for the hover card with the line it is in
          assert.ok(await seen(page, "#maskAlertRow .sub .warn"), "the masking alert's warning in sight");
          assert.deepEqual(errors, []);
          await context.close();

          ({ page, errors, context } = await open(browser, width, lang, "denied", "usage"));
          await page.locator("#usageAlertSub .warn").waitFor({ state: "attached" });
          assert.equal(await page.locator("#usageAlertSub .warn").textContent(), w.denied, "usage alert");
          assert.ok(await seen(page, "#usageAlertSub .warn"), "the usage alert's warning in sight");
          assert.equal((await page.locator("#barkRow .sub").textContent()).trim(), w.bark, "Bark's row");
          const over = await page.evaluate(() => [...document.querySelectorAll("#barkRow *")]
            .filter((e) => e.getBoundingClientRect().right > innerWidth + 1).map((e) => e.className || e.id));
          assert.deepEqual(over, [], "nothing past the edge");
          assert.deepEqual(errors, []);
          await context.close();

          // nothing wrong: nothing said
          ({ page, errors, context } = await open(browser, width, lang, "", "privacy"));
          await page.locator("#maskAlertRow .sub").waitFor();
          assert.equal(await page.locator("#maskAlertRow .warn").count(), 0, "no warning when notifications show");
          await context.close();
          if (w.unavailable) {
            ({ page, errors, context } = await open(browser, width, lang, "unavailable", "privacy"));
            await page.locator("#maskAlertRow .sub").waitFor();
            assert.equal(await page.locator("#maskAlertRow .warn").textContent(), w.unavailable);
            await context.close();
          }
        });
      }
    });
  }
}
