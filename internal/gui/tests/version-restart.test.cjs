// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › About's Version row restarts magpie as it is (yansheng1003 on
// X: "请在刷新旁边增加一个重启按钮"):
// - up to date, the row has Restart only beside Check, on
//   one line at 400px
//   too; a click posts /api/restart with the tab the window is on and says
//   Restarting…;
// - in a browser (magpie web), the page reloads once another run of
//   magpie answers /api/update, of the same version;
// - a restart that can't be arranged says so and gives the buttons back;
// - an update downloaded has its own Restart to update, and no second
//   Restart;
// - with automatic updates off and nothing checked yet, the button beside
//   Check says it doesn't update (realfxw on X restarted again and again
//   on a bare Restart, waiting for an update).
// Every language, both engines; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: { version: "Version", latest: "Up to date", check: "Check", restart: "Restart only", off: "Automatic updates are off", restarting: "Restarting…", failed: "Couldn't restart magpie", update: "Restart to update" },
  zh: { version: "版本", latest: "已是最新", check: "检查", restart: "仅重启", off: "自动更新已关闭", restarting: "正在重启…", failed: "无法重启 magpie", update: "重启以更新" },
  "zh-TW": { version: "版本", latest: "已是最新", check: "檢查", restart: "僅重啟", off: "自動更新已關閉", restarting: "正在重啟…", failed: "無法重啟 magpie", update: "重啟以更新" },
  ja: { version: "バージョン", latest: "最新です", check: "確認", restart: "再起動のみ", off: "自動アップデートはオフです", restarting: "再起動中…", failed: "magpie を再起動できませんでした", update: "再起動してアップデート" },
  de: { version: "Version", latest: "Aktuell", check: "Prüfen", restart: "Nur neu starten", off: "Automatische Updates sind ausgeschaltet", restarting: "Neustart…", failed: "magpie konnte nicht neu gestartet werden", update: "Zum Aktualisieren neu starten" },
};

function server(lang, ctl) {
  const settings = {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", proxy: "", redact: false, redactWords: [],
    noAutoUpdate: !!ctl.off, version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425", proxyNow: "none", proxySource: "none",
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [], fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
  };
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data, status) => route.fulfill({ json: data, status: status || 200 });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${!!ctl.web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: settings.fx });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/update") return json(ctl.update || { state: "latest", current: "0.1.400", run: ctl.run });
    if (url.pathname === "/api/restart" && req.method() === "POST") {
      ctl.posts.push(req.postDataJSON());
      if (ctl.fail) return json({ error: "magpie couldn't start again: quit it and open it again" }, 400);
      return route.fulfill({ status: 204, body: "" });
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

async function open(browser, lang, ctl, width) {
  const errors = [];
  const page = await (await browser.newContext({ viewport: { width, height: 700 } })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
  await page.route("**/*", server(lang, ctl));
  await watchSettingsReads(page);
  await page.goto("http://magpie.test/?view=settings&tab=about");
  await settingsDrawn(page);
  return { page, errors };
}

const versionRow = (page, lang) => page.locator("#about .row.pref", { has: page.locator(".name", { hasText: words[lang].version }) }).first();
const buttons = (row) => row.locator(".val button").evaluateAll((bs) => bs.map((b) => b.textContent));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the Version row restarts magpie as it is", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(words)) {
      const w = words[lang];

      for (const width of [900, 400]) {
        await t.test(`${lang} ${width}px: Restart beside Check, and a click restarts`, async () => {
          const ctl = { posts: [], run: 111 };
          const { page, errors } = await open(browser, lang, ctl, width);
          const row = versionRow(page, lang);
          await row.locator(".sub", { hasText: w.latest }).waitFor();
          assert.deepEqual(await buttons(row), [w.check, w.restart]);
          const tops = await row.locator(".val button").evaluateAll((bs) => bs.map((b) => Math.round(b.getBoundingClientRect().top)));
          assert.equal(new Set(tops).size, 1, `the buttons wrap: tops ${tops}`);
          const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
          assert.ok(over <= 0, `the page scrolls sideways by ${over}px`);
          await row.locator(".val button", { hasText: w.restart }).click();
          await row.locator(".sub", { hasText: w.restarting }).waitFor();
          assert.deepEqual(ctl.posts, [{ view: "settings" }]);
          assert.ok(await row.locator(".val button").evaluateAll((bs) => bs.every((b) => b.disabled)), "the buttons stay live while it restarts");
          assert.deepEqual(errors, []);
          await page.context().close();
        });
      }

      await t.test(lang + ": in a browser, the page reloads once the next run answers", async () => {
        const ctl = { posts: [], run: 111, web: true };
        const { page, errors } = await open(browser, lang, ctl, 900);
        const row = versionRow(page, lang);
        await row.locator(".sub", { hasText: w.latest }).waitFor();
        let loads = 0;
        page.on("load", () => loads++);
        await row.locator(".val button", { hasText: w.restart }).click();
        await row.locator(".sub", { hasText: w.restarting }).waitFor();
        assert.equal(ctl.posts.length, 1); // the tab it names is the server's to ignore (restartAppFrom)
        await page.waitForTimeout(1500); // the same run still answers: no reload
        assert.equal(loads, 0, "reloaded while the same run answered");
        ctl.run = 222;
        await page.waitForEvent("load", { timeout: 5000 });
        assert.deepEqual(errors, []);
        await page.context().close();
      });

      await t.test(lang + ": a restart that can't be arranged says so", async () => {
        const ctl = { posts: [], run: 111, fail: true };
        const { page, errors } = await open(browser, lang, ctl, 900);
        const row = versionRow(page, lang);
        await row.locator(".sub", { hasText: w.latest }).waitFor();
        await row.locator(".val button", { hasText: w.restart }).click();
        await row.locator(".sub", { hasText: w.failed }).waitFor();
        assert.ok(await row.locator(".val button").evaluateAll((bs) => bs.every((b) => !b.disabled)), "the buttons weren't given back");
        assert.deepEqual(errors, []);
        await page.context().close();
      });

      // an update on offer restarts by its own path, and a narrow row has
      // no room for a third button (update-download's 420px)
      await t.test(lang + ": no Restart beside an update on offer", async () => {
        for (const update of [
          { state: "found", current: "0.1.400", latest: "0.1.401", size: 3e7 },
          { state: "available", current: "0.1.400", latest: "0.1.401" },
          { state: "latest", current: "0.1.400", channel: "stable", newest: "0.1.401" },
          { state: "error", current: "0.1.400", latest: "0.1.401", error: "timed out" },
        ]) {
          const ctl = { posts: [], update: { ...update, run: 111 } };
          const { page, errors } = await open(browser, lang, ctl, 400);
          const row = versionRow(page, lang);
          await row.locator(".val button").first().waitFor();
          assert.ok(!(await buttons(row)).includes(w.restart), update.state + ": " + (await buttons(row)).join(", "));
          assert.deepEqual(errors, []);
          await page.context().close();
        }
      });

      for (const width of [900, 400]) {
        await t.test(`${lang} ${width}px: automatic updates off, the button beside Check says it doesn't update`, async () => {
          const ctl = { posts: [], off: true, update: { state: "", current: "0.1.400", run: 111 } };
          const { page, errors } = await open(browser, lang, ctl, width);
          const row = versionRow(page, lang);
          await row.locator(".sub", { hasText: w.off }).waitFor();
          assert.deepEqual(await buttons(row), [w.check, w.restart]);
          const tops = await row.locator(".val button").evaluateAll((bs) => bs.map((b) => Math.round(b.getBoundingClientRect().top)));
          assert.equal(new Set(tops).size, 1, `the buttons wrap: tops ${tops}`);
          const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
          assert.ok(over <= 0, `the page scrolls sideways by ${over}px`);
          assert.deepEqual(errors, []);
          await page.context().close();
        });
      }

      await t.test(lang + ": an update downloaded has no second Restart", async () => {
        const ctl = { posts: [], update: { state: "ready", current: "0.1.400", latest: "0.1.401", run: 111 } };
        const { page, errors } = await open(browser, lang, ctl, 900);
        const row = versionRow(page, lang);
        await row.locator(".val button", { hasText: w.update }).waitFor();
        assert.deepEqual(await buttons(row), [w.update]);
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });
}
