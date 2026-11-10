// Run with Node's test runner and Playwright on the module path; see README.md.
// BESTLJP0412 on X: a restart by itself (Settings › Restart automatically)
// came while the user was adding a provider. The page tells the app of the
// user's input (POST /api/update/using, at most every 20s, at once when a
// dialog opens or closes), and the wait it holds says so in Settings ›
// Version and on the Update pill. Every language, both engines; no backend,
// the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

const words = {
  en: { version: "Version", using: "waiting while you use magpie", idle: "restarts once the gateway is idle", pill: "Waiting to update" },
  zh: { version: "版本", using: "正在使用 magpie，稍后再重启", idle: "网关空闲后自动重启", pill: "等待更新" },
  "zh-TW": { version: "版本", using: "正在使用 magpie，稍後再重啟", idle: "閘道空閒後自動重啟", pill: "等待更新" },
  ja: { version: "バージョン", using: "magpie の使用中は待機します", idle: "ゲートウェイがアイドルになったら再起動します", pill: null },
  de: { version: "Version", using: "wartet, solange du magpie benutzt", idle: "startet neu, sobald der Zugangspunkt frei ist", pill: null },
};

function settingsPayload(lang) {
  return {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "", autoRestart: true,
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
  };
}

function server(lang, ctl) {
  const cur = settingsPayload(lang);
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: cur, fx: cur.fx });
    if (url.pathname === "/api/settings") return json(cur);
    if (url.pathname === "/api/update") return json({ ...ctl.update });
    if (url.pathname === "/api/update/using") {
      ctl.used.push(req.postDataJSON());
      return route.fulfill({ status: 204 });
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const fresh = () => ({
  update: { state: "ready", current: "0.1.400", latest: "0.1.401", waiting: true, using: true },
  used: [],
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a restart by itself waits while magpie is used", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const open = async (lang, ctl, query, width) => {
      const errors = [];
      const page = await (await browser.newContext({ viewport: { width, height: 560 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
      await page.route("**/*", server(lang, ctl));
      await watchSettingsReads(page);
      await page.goto("http://magpie.test/" + query);
      return { page, errors };
    };

    await t.test("input is told, at most every 20s, and at once when a dialog opens or closes", async () => {
      const ctl = fresh();
      const { page, errors } = await open("en", ctl, "?view=settings&tab=about", 900);
      await settingsDrawn(page);
      assert.deepEqual(ctl.used, [], "loading the page is no input");
      const told = async (n) => {
        for (let i = 0; i < 100 && ctl.used.length < n; i++) await page.waitForTimeout(20);
        assert.equal(ctl.used.length, n);
      };
      await page.keyboard.press("Shift");
      await told(1);
      assert.deepEqual(ctl.used[0], { open: false });
      await page.mouse.click(5, 300);
      await page.mouse.wheel(0, 40);
      await page.keyboard.press("Shift");
      await page.waitForTimeout(200);
      assert.equal(ctl.used.length, 1, "more input within 20s says nothing new");
      await page.evaluate(() => { document.querySelector("#modal").hidden = false; });
      await page.keyboard.press("Shift");
      await told(2);
      assert.deepEqual(ctl.used[1], { open: true });
      await page.evaluate(() => { document.querySelector("#modal").hidden = true; });
      await page.mouse.click(5, 300);
      await told(3);
      assert.deepEqual(ctl.used[2], { open: false });
      assert.deepEqual(errors, []);
    });

    for (const [lang, w] of Object.entries(words)) {
      await t.test(lang + ": Settings › Version and the pill say the restart waits for the user", async () => {
        const ctl = fresh();
        const { page, errors } = await open(lang, ctl, "?view=settings&tab=about", 900);
        await settingsDrawn(page);
        const row = page.locator("#about .row.pref", { has: page.locator(".name", { hasText: w.version }) }).first();
        await page.waitForFunction((t) => [...document.querySelectorAll("#about .row.pref .sub")].some((s) => s.textContent.includes(t)), w.using);
        const sub = (await row.locator(".sub").textContent()).trim();
        assert.ok(sub.endsWith(" · " + w.idle + " · " + w.using), sub);
        if (lang === "en") assert.equal(sub, "0.1.401 is downloaded · restarts once the gateway is idle · waiting while you use magpie");
        const panel = await open(lang, fresh(), "?mode=panel", 420);
        const pill = panel.page.locator("#update");
        await pill.waitFor({ state: "visible" });
        await panel.page.waitForFunction(() => (document.querySelector("#update")?.title || "").length > 0);
        if (w.pill) assert.equal((await pill.locator("span").first().textContent()).trim(), w.pill);
        assert.ok((await pill.getAttribute("title")).includes(w.using), await pill.getAttribute("title"));
        if (lang === "en") assert.equal(await pill.getAttribute("title"), "magpie restarts to update to 0.1.401 once the agents' requests through the gateway have finished: waiting while you use magpie. Click to restart now.");
        assert.deepEqual(errors, []);
        assert.deepEqual(panel.errors, []);
      });
    }
  });
}
