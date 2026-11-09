// Run with Node's test runner and Playwright on the module path; see README.md.
// #1902, magicLaLa: "workbuddy 每日签到能否根据每个账号设置是否开启自动签到".
// WorkBuddy's provider editor has, under Daily check-in, an Off/On row
// for each account it reaches (settings.checkinAccounts), set with
// POST /api/settings/checkin-account {key, on}; an account left out says so
// on its Usage card, where Check in now leaves it alone. Chromium and
// WebKit, the window at 1100 and 420, in English, Chinese, Traditional
// Chinese, Japanese and German; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, provPrefsProviders, openProvPrefs, inEditor } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const today = new Date(Date.now() + 8 * 3600e3).toISOString().slice(0, 10);

function serve(lang, asked) {
  const settings = { theme: "light", lang, quotaLeft: false, currency: "usd", workbuddy: true, workbuddyCheckin: true,
    checkinAccounts: [
      { key: "workbuddy|u1", by: "", user: "Ann", on: true },
      { key: "workbuddy|u2", by: "", user: "Bob", on: false },
      { key: "trae-cn|u9", by: "trae", user: "Cat", on: true },
    ] };
  const quotas = () => [
    { provider: "workbuddy", name: "WorkBuddy", plan: "Pro", user: "Ann", windows: [{ name: "Credits", used: 12 }], checkins: true,
      checkin: { user: "Ann", day: today, outcome: "claimed", credit: 50, streak: 3 } },
    { provider: "workbuddy", name: "WorkBuddy", plan: "Free", user: "Bob", windows: [{ name: "Credits", used: 2 }], checkins: true,
      checkinOff: !settings.checkinAccounts[1].on },
  ];
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings/checkin-account") {
      const b = route.request().postDataJSON();
      asked.push(b);
      settings.checkinAccounts = settings.checkinAccounts.map((a) => (a.key === b.key ? { ...a, on: b.on } : a));
      return json(settings);
    }
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json(provPrefsProviders(["workbuddy"], { workbuddy: "WorkBuddy" }));
    if (url.pathname === "/api/usage/quotas") return json(quotas());
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { on: "Checked in once a day while Daily check-in is on", off: "Left out of the daily check-in", card: "Auto check-in is off for this account" },
  zh: { on: "每日自动签到开启时，每天为它签到一次", off: "不参与每日自动签到", card: "此账号的自动签到已关闭" },
  "zh-TW": { on: "每日自動簽到開啟時，每天為它簽到一次", off: "不參與每日自動簽到", card: "此帳號的自動簽到已關閉" },
  ja: { on: "毎日のチェックインがオンの間、1 日 1 回チェックインします", off: "毎日のチェックインの対象外", card: "このアカウントの自動チェックインはオフです" },
  de: { on: "Einmal täglich eingecheckt, solange der tägliche Check-in an ist", off: "Vom täglichen Check-in ausgenommen", card: "Automatischer Check-in ist für dieses Konto aus" },
};


for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const lang of Object.keys(words)) {
      const w = words[lang];
      test(`${engine} ${width} ${lang}: each WorkBuddy account's check-in on its own in its provider's editor`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], asked = [];
        page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; errors.push(e.message); }); // a goto in WebKit can cut off a fetch (#1350)
        await page.route("**/*", serve(lang, asked));
        await watchSettingsReads(page);
        await openProvPrefs(page, "workbuddy");
        await settingsDrawn(page);
        const rows = page.locator(".editor .prov-prefs #wbList .ci-acct");
        await rows.first().waitFor();
        assert.equal(await rows.count(), 2, "a row an account, Trae CN's not among them");
        assert.deepEqual(await rows.locator(".name").allInnerTexts(), ["Ann", "Bob"]);
        assert.equal((await rows.nth(0).locator(".sub").innerText()).trim(), w.on);
        assert.equal((await rows.nth(1).locator(".sub").innerText()).trim(), w.off);
        assert.equal(await rows.nth(1).locator(".segs .opt.on").getAttribute("aria-pressed"), "true");
        assert.equal(await page.locator("#traeList .ci-acct").count(), 0, "Trae CN has one account: its switch is its own");
        for (let i = 0; i < 2; i++) {
          const box = await rows.nth(i).boundingBox();
          assert(box.width > 0 && box.x + box.width <= width, "the row fits");
        }
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
        if (process.env.ARTIFACT_DIR) await page.locator("#wbList").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `checkin-account-${engine}-${width}-${lang}.png`) });

        const on = rows.nth(1).locator(".segs .opt").nth(1);
        await inEditor(page, on);
        // in the editor, what was pressed stays under the pointer: the
        // editor scrolls to hold a row drawn again (modalHold)
        const top = () => on.evaluate((e) => e.getBoundingClientRect().top);
        const y = await top();
        const saved = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/settings/checkin-account");
        await on.click();
        await saved;
        assert.deepEqual(asked, [{ key: "workbuddy|u2", on: true }]);
        await page.waitForFunction((want) => document.querySelectorAll("#wbList .ci-acct .sub")[1]?.textContent.trim() === want, w.on);
        await page.evaluate(() => new Promise((ok) => requestAnimationFrame(() => requestAnimationFrame(ok))));
        const now = await top();
        assert(Math.abs(now - y) <= 1, `the switch moved from ${y} to ${now}`);
        assert.equal(await page.evaluate(() => scrollY), 0, "the page itself never scrolls");
        assert.equal(await page.locator("select").count(), 0);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${width} ${lang}: an account left out says so on its Usage card`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], asked = [];
        page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; errors.push(e.message); }); // a goto in WebKit can cut off a fetch (#1350)
        await page.route("**/*", serve(lang, asked));
        await watchSettingsReads(page);
        await page.goto("http://magpie.test/?view=usage");
        await settingsDrawn(page);
        const rows = page.locator(".subscription-card", { hasText: "WorkBuddy" }).locator(".wb-checkin");
        await rows.first().waitFor();
        assert.equal(await rows.count(), 2, "one line an account");
        const bob = rows.nth(1);
        assert.equal((await bob.locator(".ci-say").innerText()).trim(), w.card);
        assert.equal(await bob.getAttribute("data-state"), "none", "not waiting to be checked in");
        // Ann is in today and Bob is left out: nothing to press now
        assert.equal(await page.locator(".ci-now").count(), 0, "Check in now for an account left out");
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
        assert.deepEqual(errors, []);
      });
    }
  }
}
