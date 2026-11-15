// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings → About → Update channel (ssharpcc on X): Every version, the
// default, takes each release as it comes out; Stable takes one a week
// and an urgent one at once, from the update feed's ?channel=stable. The
// choice is saved as updateChannel ("" or "stable"), kept by every other
// save, and survives a reload. No click moves the page; at 420 wide the
// row fits without a sideways scroll. Every language; no backend, the API
// is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, openPart } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

const words = {
  en: { name: "Update channel", every: "Every version", stable: "Stable", auto: "Automatic updates", off: "Off",
    everySub: "Every version as soon as it is out", stableSub: "One version a week" },
  zh: { name: "更新渠道", every: "尝鲜版", stable: "稳定版", auto: "自动更新", off: "关闭", everySub: "每个新版本一发布就更新", stableSub: "每周一个版本" },
  "zh-TW": { name: "更新管道", every: "搶先版", stable: "穩定版", auto: "自動更新", off: "關閉", everySub: "每個新版本一發布就更新", stableSub: "每週一個版本" },
  ja: { name: "アップデートチャネル", every: "先行版", stable: "安定版", auto: "自動アップデート", off: "オフ", everySub: "新しいバージョンが出るたびに更新します", stableSub: "週に 1 回" },
  de: { name: "Update-Kanal", every: "Jede Version", stable: "Stabil", auto: "Automatische Updates", off: "Aus", everySub: "Jede Version, sobald sie erscheint", stableSub: "Eine Version pro Woche" },
};

// Daily (fishmagic80 on X): one a day, between the two
const dailyWords = {
  en: { daily: "Daily", dailySub: "One version a day" },
  zh: { daily: "每日版", dailySub: "每天一个版本" },
  "zh-TW": { daily: "每日版", dailySub: "每天一個版本" },
  ja: { daily: "デイリー版", dailySub: "1 日 1 回" },
  de: { daily: "Täglich", dailySub: "Eine Version pro Tag" },
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
    if (url.pathname === "/api/update") return json({ state: "latest", current: "0.1.400", latest: "0.1.400" });
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
  test(engine + ": the update channel", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(words)) {
      const w = words[lang];
      for (const width of [900, 420]) {
        await t.test(`${lang} at ${width}: Every version by default, Stable saved and kept`, async () => {
          const ctl = { channel: "", off: false, saves: [] };
          const errors = [];
          const page = await (await browser.newContext({ viewport: { width, height: 640 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
          await page.route("**/*", server(lang, ctl));
          await watchSettingsReads(page);
          await page.goto("http://magpie.test/?view=settings");
          await settingsDrawn(page);
          const aboutTab = async () => {
            await openPart(page, "about");
            await page.locator("#setPage-about").waitFor({ state: "visible" });
          };
          await aboutTab();
          const rowOf = (name) => page.locator("#about .row.pref", { has: page.locator(".name", { hasText: new RegExp("^" + name + "$") }) });
          const channel = rowOf(w.name), auto = rowOf(w.auto);
          await channel.waitFor();
          assert.deepEqual((await channel.locator(".opt").allInnerTexts()).map((s) => s.trim()), [w.every, dailyWords[lang].daily, w.stable]);
          assert.equal((await channel.locator(".opt.on").innerText()).trim(), w.every);
          await channel.locator(".sub", { hasText: w.everySub }).waitFor();

          // the row fits: its switch inside the window, nothing scrolls sideways
          await showRow(page, channel);
          const right = await channel.locator(".opt").last().evaluate((o) => o.getBoundingClientRect().right);
          assert.ok(right <= width, `the switch ends at ${right}, past ${width}`);
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), true, "no sideways scroll");

          // Stable
          let before = await scrolls(page);
          await channel.locator(".opt", { hasText: w.stable }).click();
          await channel.locator(".sub", { hasText: w.stableSub }).waitFor();
          assert.equal((await channel.locator(".opt.on").innerText()).trim(), w.stable);
          assert.equal(ctl.saves.at(-1).updateChannel, "stable");
          assert.equal(await scrolls(page), before, "the click must not scroll the page");

          // another setting's save keeps it
          await showRow(page, auto);
          before = await scrolls(page);
          await auto.locator(".opt", { hasText: w.off }).click();
          await page.waitForFunction(() => document.querySelector(".update-auto-row .opt.on") && document.querySelector(".update-auto-row .opt.on") === document.querySelector(".update-auto-row .opt"));
          assert.equal(ctl.saves.at(-1).noAutoUpdate, true);
          assert.equal(ctl.saves.at(-1).updateChannel, "stable");
          assert.equal(await scrolls(page), before, "the click must not scroll the page");
          // and with automatic updates off it is still lit: Check asks on it
          assert.equal(await channel.evaluate((r) => r.classList.contains("off")), false);

          // after a reload: still Stable
          await page.reload();
          await aboutTab();
          await channel.waitFor();
          assert.equal((await channel.locator(".opt.on").innerText()).trim(), w.stable);

          // Daily
          await showRow(page, channel);
          before = await scrolls(page);
          await channel.locator(".opt", { hasText: dailyWords[lang].daily }).click();
          await channel.locator(".sub", { hasText: dailyWords[lang].dailySub }).waitFor();
          assert.equal((await channel.locator(".opt.on").innerText()).trim(), dailyWords[lang].daily);
          assert.equal(ctl.saves.at(-1).updateChannel, "daily");
          assert.equal(await scrolls(page), before, "the click must not scroll the page");
          await page.reload();
          await aboutTab();
          await channel.waitFor();
          assert.equal((await channel.locator(".opt.on").innerText()).trim(), dailyWords[lang].daily);

          // back to every version
          await showRow(page, channel);
          before = await scrolls(page);
          await channel.locator(".opt", { hasText: w.every }).click();
          await channel.locator(".sub", { hasText: w.everySub }).waitFor();
          assert.equal(ctl.saves.at(-1).updateChannel, "");
          assert.equal(await scrolls(page), before, "the click must not scroll the page");
          assert.deepEqual(errors, []);
        });
      }
    }
  });
}
