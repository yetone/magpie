// Run with Node's test runner and Playwright on the module path; see README.md.
// times_ang on X: "magpie 可以调整自动签到的时间吗". Under a daily check-in
// that is on, a Check-in time row: Off (as each vendor's day starts) or
// On at 09:00, a time typed then saved as settings.checkinAt through
// POST /api/settings, and Off again saves ""; a plugin's check-in group
// has the same row, a check-in turned off none. An account not checked in
// yet says the time on its Usage card. Chromium and WebKit, the window at
// 1100 and 420, in English, Chinese, Traditional Chinese, Japanese and
// German; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, provPrefsProviders, openProvPrefs, inEditor } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const yesterday = new Date(Date.now() + 8 * 3600e3 - 86400e3).toISOString().slice(0, 10);

function serve(lang, saved, at) {
  const settings = { theme: "light", lang, quotaLeft: false, currency: "usd", workbuddy: true, workbuddyCheckin: true, checkinAt: at,
    trae: true, traeCheckin: false,
    checkinPlugins: [{ id: "autoclaw", name: "AutoClaw", on: true, checkins: [] }],
    checkinAccounts: [{ key: "workbuddy|u1", by: "", user: "Ann", on: true }] };
  const quotas = () => [
    { provider: "workbuddy", name: "WorkBuddy", plan: "Pro", user: "Ann", windows: [{ name: "Credits", used: 12 }], checkins: true,
      checkin: { user: "Ann", day: yesterday, outcome: "claimed", credit: 50 } },
  ];
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") {
      if (route.request().method() === "POST") {
        const b = route.request().postDataJSON();
        saved.push(b.checkinAt);
        settings.checkinAt = b.checkinAt;
      }
      return json(settings);
    }
    if (url.pathname === "/api/providers") return json(provPrefsProviders(["workbuddy", "autoclaw"], { workbuddy: "WorkBuddy", autoclaw: "AutoClaw" }));
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
  en: { name: "Check-in time", off: "As soon as each vendor's day starts", on: "From 21:30 each day, for every vendor's check-in", card: "Not checked in yet today · checks in from 21:30" },
  zh: { name: "签到时间", off: "每个服务商的新一天一开始就签到", on: "每天 21:30 起签到，所有服务商的签到都按这个时间", card: "今日尚未签到 · 21:30 起签到" },
  "zh-TW": { name: "簽到時間", off: "每個服務商的新一天一開始就簽到", on: "每天 21:30 起簽到，所有服務商的簽到都按這個時間", card: "今日尚未簽到 · 21:30 起簽到" },
  ja: { name: "チェックインの時刻", off: "各サービスの日付が変わったらすぐにチェックイン", on: "毎日 21:30 からチェックイン（すべてのサービスのチェックインに共通）", card: "今日はまだチェックインしていません · 21:30 からチェックイン" },
  de: { name: "Check-in-Zeit", off: "Sobald der Tag des jeweiligen Anbieters beginnt", on: "Täglich ab 21:30, für den Check-in jedes Anbieters", card: "Heute noch nicht eingecheckt · Check-in ab 21:30" },
};

function launch(engine) {
  return engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const lang of Object.keys(words)) {
      const w = words[lang];
      test(`${engine} ${width} ${lang}: the check-in's time of day is set under Daily check-in`, async (t) => {
        const browser = await launch(engine);
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], saved = [];
        page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; errors.push(e.message); });
        await page.route("**/*", serve(lang, saved, ""));
        await watchSettingsReads(page);
        await openProvPrefs(page, "workbuddy");
        await settingsDrawn(page);
        const row = page.locator(".editor .prov-prefs #wbList .ci-at-row");
        await row.waitFor();
        assert.equal((await row.locator(".name").innerText()).trim(), w.name);
        assert.equal((await row.locator(".sub").innerText()).trim(), w.off);
        assert.equal(await row.locator("input.at").count(), 0, "no time while Off");
        assert.equal(await page.locator("#traeList .ci-at-row").count(), 0, "a check-in turned off has none");
        assert.equal(await page.locator('.ci-plugin[data-provider="autoclaw"] .ci-at-row').count(), 1, "a plugin's check-in has one");

        const on = row.locator(".segs .opt").nth(1);
        await inEditor(page, on);
        await on.click();
        await page.waitForFunction(() => document.querySelector(".editor #wbList .ci-at-row input.at")?.value === "09:00");
        assert.deepEqual(saved, ["09:00"]);
        const time = page.locator(".editor #wbList .ci-at-row input.at");
        await time.fill("21:30");
        await time.press("Enter");
        await page.waitForFunction((want) => document.querySelector(".editor #wbList .ci-at-row .sub")?.textContent.trim() === want, w.on);
        assert.deepEqual(saved, ["09:00", "21:30"]);
        for (const box of [await page.locator(".editor #wbList .ci-at-row").boundingBox(), await time.boundingBox()]) {
          assert(box.width > 0 && box.x + box.width <= width, "the row fits");
        }
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
        if (process.env.ARTIFACT_DIR) await page.locator(".editor #wbList").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `checkin-at-${engine}-${width}-${lang}.png`) });

        await page.locator(".editor #wbList .ci-at-row .segs .opt").nth(0).click();
        await page.waitForFunction((want) => document.querySelector(".editor #wbList .ci-at-row .sub")?.textContent.trim() === want, w.off);
        assert.deepEqual(saved, ["09:00", "21:30", ""]);
        assert.equal(await page.locator("select").count(), 0);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${width} ${lang}: a card not checked in yet says the time`, async (t) => {
        const browser = await launch(engine);
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; errors.push(e.message); });
        await page.route("**/*", serve(lang, [], "21:30"));
        await watchSettingsReads(page);
        await page.goto("http://magpie.test/?view=usage");
        await settingsDrawn(page);
        const say = page.locator(".subscription-card", { hasText: "WorkBuddy" }).locator(".wb-checkin .ci-say");
        await say.first().waitFor();
        assert.equal((await say.first().innerText()).trim().split(" · ").slice(0, 2).join(" · "), w.card);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no sideways scroll");
        assert.deepEqual(errors, []);
      });
    }
  }
}
