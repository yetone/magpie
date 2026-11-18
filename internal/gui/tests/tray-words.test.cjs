// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Usage names the place beside the clock as each OS does: the
// menu bar on a Mac, the tray on Windows and Linux, in every row's name and
// description there. And "Menu bar refresh" goes when allowances are read
// only when asked and only cards are shown: the cards then keep the last
// reading, so how often it is shown again changes nothing. Every language,
// both engines; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { openPart } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

const quotas = [
  { provider: "claude", name: "Claude Code", icon: "claude-color", user: "a@b.c", plan: "Max",
    windows: [{ name: "5-hour", used: 42 }, { name: "Weekly", used: 18 }] },
];

// the row names checked, and the word each language has for the Mac's bar
const want = {
  en: { bar: /menu bar/i, mac: ["Allowances in the menu bar", "Menu bar refresh", "Menu bar cost", "Menu bar currency"],
    tray: ["Allowances in the tray", "Tray refresh", "Tray cost", "Tray currency"] },
  zh: { bar: /菜单栏/, mac: ["菜单栏显示额度", "菜单栏刷新频率", "菜单栏金额", "菜单栏币种"], tray: ["托盘显示额度", "托盘刷新频率", "托盘金额", "托盘币种"] },
  "zh-TW": { bar: /選單列/, mac: ["選單列顯示額度", "選單列重新整理頻率", "選單列金額", "選單列幣種"], tray: ["托盤顯示額度", "托盤重新整理頻率", "托盤金額", "托盤幣種"] },
  ja: { bar: /メニューバー/, mac: ["メニューバーに利用枠を表示", "メニューバーの更新間隔", "メニューバーの金額", "メニューバーの通貨"],
    tray: ["トレイに利用枠を表示", "トレイの更新間隔", "トレイの金額", "トレイの通貨"] },
  de: { bar: /Menüleiste/, mac: ["Kontingente in der Menüleiste", "Aktualisierung der Menüleiste", "Kosten in der Menüleiste", "Währung in der Menüleiste"],
    tray: ["Kontingente im Tray", "Aktualisierung des Trays", "Kosten im Tray", "Währung im Tray"] },
};

function settingsPayload(over) {
  return {
    theme: "light", lang: "en", tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsages: [], trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

function server(lang, over) {
  let cur = settingsPayload({ lang, ...over });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: cur.fx });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") cur = { ...cur, ...req.postDataJSON() };
      return json(cur);
    }
    if (url.pathname === "/api/settings/quota-reads") {
      cur = { ...cur, quotaReads: req.postDataJSON().reads };
      return json(cur);
    }
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const NAMES = ["#trayUsageRow", "#trayEveryRow", "#trayCostRow", "#trayCurrencyRow"];
const names = (page) => page.evaluate((ids) => ids.map((id) => document.querySelector(id + " .name").textContent.trim()), NAMES);
// every text of the Usage part but the Mac's own rows, which other systems hide
const words = (page) => page.evaluate(() => [...document.querySelectorAll("#setPage-usage .name, #setPage-usage .sub")]
  .filter((e) => !e.closest("#trayLogosRow, #trayBirdRow")).map((e) => e.textContent));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of (process.env.LANGS ? process.env.LANGS.split(",") : Object.keys(want))) {
    test(`${engine} ${lang}: the menu bar on a Mac, the tray elsewhere`, async (t) => {
      const w = want[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const platform of ["MacIntel", "Win32", "Linux x86_64"]) {
        await t.test(platform, async () => {
          const errors = [];
          const context = await browser.newContext({ viewport: { width: 1100, height: 700 }, reducedMotion: "reduce" });
          const page = await context.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.addInitScript((p) => Object.defineProperty(navigator, "platform", { get: () => p }), platform);
          await page.route("**/*", server(lang, { trayUsages: ["claude|a@b.c"], trayUsage: "claude|a@b.c" }));
          await page.goto("http://magpie.test/?view=usage");
          await page.locator("#prefs").click();
          await openPart(page, "usage");
          const mac = platform === "MacIntel";
          const expect = mac ? w.mac : w.tray;
          await page.waitForFunction((want) => document.querySelector("#trayUsageRow .name").textContent.trim() === want, expect[0]);
          assert.deepEqual(await names(page), expect);
          const said = (await words(page)).filter((s) => w.bar.test(s));
          if (mac) assert(said.length >= 4, "the Mac's rows say the menu bar: " + said.length);
          else assert.deepEqual(said, [], "no row says the menu bar on " + platform);

          // Menu bar refresh: shown while allowances are read by themselves
          assert.equal(await page.locator("#trayEveryRow").isVisible(), true, "refresh shown, reads automatic");
          await page.locator("#quotaReadsSegs button").nth(1).click();
          await page.waitForFunction(() => document.querySelector("#trayEveryRow").hidden);
          // a language switch or a redraw keeps the OS's word
          assert.deepEqual(await names(page), expect);
          await page.locator("#quotaReadsSegs button").nth(0).click();
          await page.waitForFunction(() => !document.querySelector("#trayEveryRow").hidden);
          assert.deepEqual(errors, []);
          await context.close();
        });
      }
    });

    test(`${engine} ${lang}: refresh stays for a total when allowances are read only when asked`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const context = await browser.newContext({ viewport: { width: 420, height: 700 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, { quotaReads: "asked", trayUsages: ["claude|a@b.c", "usage:today"], trayUsage: "claude|a@b.c" }));
      await page.goto("http://magpie.test/?view=usage");
      await page.locator("#prefs").click();
      await openPart(page, "usage");
      await page.locator("#trayUsagePick button").waitFor();
      assert.equal(await page.locator("#trayEveryRow").isHidden(), false, "today's total is read again, so how often stays");
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
      assert.deepEqual(errors, []);
      await context.close();
    });
  }
}
