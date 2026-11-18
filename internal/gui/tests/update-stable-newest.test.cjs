// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings → About → Version on the Stable channel (ylspcm on X). The app
// installed from the site is newer than Stable's version, so every check
// said a bare "Up to date" while newer releases were out, and the user
// installed by hand each time. On Stable the row says it is up to date on
// Stable, names the newest version out on Every version, and offers to
// switch; the switch saves updateChannel "" and the row then reads a
// plain "Up to date". With nothing newer it says only "Up to date on
// Stable". No click moves the page; at 420 wide nothing scrolls sideways.
// Every language; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, openPart } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

const words = {
  en: { newest: "Up to date on Stable · 0.1.410 is out on Every version", stable: "Up to date on Stable", every: "Up to date", go: "Switch to Every version" },
  zh: { newest: "稳定版已是最新 · 尝鲜版已有 0.1.410", stable: "稳定版已是最新", every: "已是最新", go: "改用尝鲜版" },
  "zh-TW": { newest: "穩定版已是最新 · 搶先版已有 0.1.410", stable: "穩定版已是最新", every: "已是最新", go: "改用搶先版" },
  ja: { newest: "安定版では最新です · 先行版では 0.1.410 が出ています", stable: "安定版では最新です", every: "最新です", go: "先行版に切り替える" },
  de: { newest: "Aktuell auf Stabil · Unter Jede Version ist 0.1.410 erschienen", stable: "Aktuell auf Stabil", every: "Aktuell", go: "Zu Jede Version wechseln" },
};

// Daily's (fishmagic80 on X): the same, under its own name
const dailyWords = {
  en: { newest: "Up to date on Daily · 0.1.410 is out on Every version", daily: "Up to date on Daily" },
  zh: { newest: "每日版已是最新 · 尝鲜版已有 0.1.410", daily: "每日版已是最新" },
  "zh-TW": { newest: "每日版已是最新 · 搶先版已有 0.1.410", daily: "每日版已是最新" },
  ja: { newest: "デイリー版では最新です · 先行版では 0.1.410 が出ています", daily: "デイリー版では最新です" },
  de: { newest: "Aktuell auf Täglich · Unter Jede Version ist 0.1.410 erschienen", daily: "Aktuell auf Täglich" },
};

// newer than the channel's pick (realfxw on X: 0.1.4445 on Daily's
// 0.1.4395): the row says nothing comes until the channel passes it,
// not that it is up to date, and still offers the newest
const aheadWords = {
  en: { daily: "Newer than Daily's 0.1.390; it updates once Daily passes it · 0.1.410 is out on Every version", stable: "Newer than Stable's 0.1.390; it updates once Stable passes it" },
  zh: { daily: "比每日版的 0.1.390 新，每日版超过它后才会更新 · 尝鲜版已有 0.1.410", stable: "比稳定版的 0.1.390 新，稳定版超过它后才会更新" },
  "zh-TW": { daily: "比每日版的 0.1.390 新，每日版超過它後才會更新 · 搶先版已有 0.1.410", stable: "比穩定版的 0.1.390 新，穩定版超過它後才會更新" },
  ja: { daily: "デイリー版の 0.1.390 より新しいため、デイリー版が追い越すまで更新されません · 先行版では 0.1.410 が出ています", stable: "安定版の 0.1.390 より新しいため、安定版が追い越すまで更新されません" },
  de: { daily: "Neuer als 0.1.390 auf Täglich; Updates kommen, sobald Täglich diese Version überholt · Unter Jede Version ist 0.1.410 erschienen", stable: "Neuer als 0.1.390 auf Stabil; Updates kommen, sobald Stabil diese Version überholt" },
};

function settingsPayload(lang, ctl) {
  const s = {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
  };
  // as the backend sends them: left out while they are their defaults
  if (ctl.channel) s.updateChannel = ctl.channel;
  if (ctl.off) s.noAutoUpdate = true;
  return s;
}

function server(lang, ctl) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settingsPayload(lang, ctl), fx: settingsPayload(lang, ctl).fx });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") {
        const body = req.postDataJSON();
        ctl.channel = body.updateChannel || "";
        ctl.off = !!body.noAutoUpdate;
        ctl.saves.push(body);
      }
      return json(settingsPayload(lang, ctl));
    }
    if (url.pathname === "/api/update") {
      if (ctl.channel !== "stable" && ctl.channel !== "daily") return json({ state: "latest", current: "0.1.400", latest: "0.1.410" });
      return json({ state: "latest", current: "0.1.400", latest: "0.1.390", channel: ctl.channel, ...(ctl.newest ? { newest: ctl.newest } : {}), ...(ctl.ahead ? { ahead: true } : {}) });
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

// wheel a row into sight, as a user would; a click must then not move the page
async function showRow(page, row) {
  await page.locator("#view-settings").hover();
  for (let i = 0; i < 40; i++) {
    const box = await row.boundingBox();
    const view = await page.locator("#view-settings").boundingBox();
    if (box && view && box.y >= view.y + 4 && box.y + box.height <= view.y + view.height - 4) break;
    await page.mouse.wheel(0, 180);
    await page.waitForTimeout(16);
  }
  await page.waitForTimeout(250);
}
const scrolls = (page) => page.evaluate(() => [window.scrollY, document.scrollingElement.scrollTop, ...[...document.querySelectorAll(".view")].map((v) => v.scrollTop)].join(","));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Stable's up to date names the newest", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(words)) {
      const w = words[lang];
      for (const width of [900, 420]) {
        await t.test(`${lang} at ${width}`, async () => {
          const ctl = { channel: "stable", newest: "0.1.410", off: false, saves: [] };
          const errors = [];
          const page = await (await browser.newContext({ viewport: { width, height: 640 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
          await page.route("**/*", server(lang, ctl));
          await watchSettingsReads(page);
          await page.goto("http://magpie.test/?view=settings");
          await settingsDrawn(page);
          await openPart(page, "about");
          await page.locator("#setPage-about").waitFor({ state: "visible" });
          const row = page.locator("#about .row.pref", { has: page.locator("code", { hasText: /^0\.1\.400$/ }) });
          const sub = row.locator(".sub");
          await page.waitForFunction((s) => document.querySelector("#about .row.pref .sub.say")?.textContent === s, w.newest);
          assert.equal(await sub.textContent(), w.newest);
          const go = row.locator("button", { hasText: w.go });
          await go.waitFor();

          await showRow(page, row);
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), true, "no sideways scroll");
          const right = await go.evaluate((b) => b.getBoundingClientRect().right);
          assert.ok(right <= width, `the button ends at ${right}, past ${width}`);

          // the switch: every version saved, and the row a plain up to date
          const before = await scrolls(page);
          await go.click();
          await page.waitForFunction((s) => document.querySelector("#about .row.pref .sub.say")?.textContent === s, w.every);
          assert.equal(ctl.saves.at(-1).updateChannel, "");
          assert.equal(await row.locator("button", { hasText: w.go }).count(), 0);
          assert.equal(await scrolls(page), before, "the click must not scroll the page");

          // Stable with nothing newer: up to date on Stable, nothing to switch to
          ctl.channel = "stable";
          ctl.newest = "";
          await page.reload();
          await settingsDrawn(page);
          await openPart(page, "about");
          await page.waitForFunction((s) => document.querySelector("#about .row.pref .sub.say")?.textContent === s, w.stable);
          assert.equal(await row.locator("button", { hasText: w.go }).count(), 0);

          // Daily, the newest out on every version: named, and the switch
          ctl.channel = "daily";
          ctl.newest = "0.1.410";
          await page.reload();
          await settingsDrawn(page);
          await openPart(page, "about");
          await page.waitForFunction((s) => document.querySelector("#about .row.pref .sub.say")?.textContent === s, dailyWords[lang].newest);
          await row.locator("button", { hasText: w.go }).waitFor();
          // Daily with nothing newer
          ctl.newest = "";
          await page.reload();
          await settingsDrawn(page);
          await openPart(page, "about");
          await page.waitForFunction((s) => document.querySelector("#about .row.pref .sub.say")?.textContent === s, dailyWords[lang].daily);
          assert.equal(await row.locator("button", { hasText: w.go }).count(), 0);

          // ahead of Daily, the newest out: said, the switch offered, and
          // the row fits at 420 without a sideways scroll
          ctl.ahead = true;
          ctl.newest = "0.1.410";
          await page.reload();
          await settingsDrawn(page);
          await openPart(page, "about");
          await page.waitForFunction((s) => document.querySelector("#about .row.pref .sub.say")?.textContent === s, aheadWords[lang].daily);
          await row.locator("button", { hasText: w.go }).waitFor();
          await showRow(page, row);
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), true, "no sideways scroll ahead of Daily");
          const aheadRight = await row.locator("button", { hasText: w.go }).evaluate((b) => b.getBoundingClientRect().right);
          assert.ok(aheadRight <= width, `the button ends at ${aheadRight}, past ${width}`);
          // ahead of Stable, nothing newer
          ctl.channel = "stable";
          ctl.newest = "";
          await page.reload();
          await settingsDrawn(page);
          await openPart(page, "about");
          await page.waitForFunction((s) => document.querySelector("#about .row.pref .sub.say")?.textContent === s, aheadWords[lang].stable);
          assert.equal(await row.locator("button", { hasText: w.go }).count(), 0);
          ctl.ahead = false;
          assert.deepEqual(errors, []);
        });
      }
    }
  });
}
