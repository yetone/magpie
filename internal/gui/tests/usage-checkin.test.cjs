// Run with Node's test runner and Playwright on the module path; see README.md.
// #694 (Dazzle-sys): "put the check-in settings on the Usage card, and show
// on the card whether today's check-in is done; having to look in Settings
// to know whether it checked in is wrong". A WorkBuddy (China) account's
// card says how its daily check-in went — today's with the credits and the
// streak, not eligible, failed, or not yet with the last day it was — a
// dot beside it; the card's first such row has Auto check-in, the same
// switch as Settings' Daily check-in (POST /api/settings/workbuddy-checkin),
// and Check in now while an account isn't in today
// (POST /api/usage/workbuddy-checkin). Another vendor's card has none of it.
// Clicks never move the page. A Trae CN account's card (Hu9956: TRAE cn
// 也有每天签到送100积分) has the same row, with its own switch
// (POST /api/settings/trae-checkin) and press (POST /api/usage/trae-checkin),
// the WorkBuddy one left as it was. English and Chinese; the API is faked
// here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const today = new Date(Date.now() + 8 * 3600e3).toISOString().slice(0, 10);
const quotas = () => [
  { provider: "workbuddy", name: "WorkBuddy", icon: "workbuddy-color", plan: "Pro", user: "李雷", windows: [{ name: "Credits", used: 8.9 }],
    checkins: true, checkin: { user: "李雷", day: today, outcome: "claimed", credit: 50, streak: 3 } },
  { provider: "workbuddy", name: "WorkBuddy", icon: "workbuddy-color", plan: "Free", user: "韩梅梅", windows: [{ name: "Credits", used: 2 }],
    checkins: true, checkin: { user: "韩梅梅", day: "2026-09-30", outcome: "claimed", credit: 20 } },
  { provider: "workbuddy", name: "WorkBuddy", icon: "workbuddy-color", plan: "Free", user: "Lucy", windows: [{ name: "Credits", used: 1 }],
    checkins: true, checkin: { user: "Lucy", day: today, outcome: "ineligible" } },
  { provider: "codex", name: "Codex", icon: "openai", plan: "Plus", windows: [{ name: "5 hours", used: 20 }] },
  { provider: "trae-cn", name: "Trae CN", plan: "Free", user: "hu", windows: [{ name: "Credits", used: 3 }],
    checkins: true, checkinBy: "trae", checkin: { user: "hu", day: "2026-09-30", outcome: "claimed", credit: 100 } },
];

function serve(lang, asked) {
  const settings = { theme: "light", lang, quotaLeft: false, currency: "usd", workbuddyCheckin: false, traeCheckin: false };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/settings/workbuddy-checkin") {
      asked.push(["set", route.request().postDataJSON()]);
      settings.workbuddyCheckin = route.request().postDataJSON().on;
      return json(settings);
    }
    if (url.pathname === "/api/settings/trae-checkin") {
      asked.push(["trae-set", route.request().postDataJSON()]);
      settings.traeCheckin = route.request().postDataJSON().on;
      return json(settings);
    }
    if (url.pathname === "/api/usage/trae-checkin") {
      asked.push(["trae-now"]);
      return json([{ user: "hu", by: "trae", day: today, outcome: "claimed", credit: 100 }]);
    }
    if (url.pathname === "/api/usage/workbuddy-checkin") {
      asked.push(["now"]);
      return json([{ user: "韩梅梅", day: today, outcome: "claimed", credit: 20 }]);
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
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
  en: { done: "Checked in today +50 · 3-day streak", off: "Auto check-in is off · last checked in 2026-09-30", wait: "Not checked in yet today · last checked in 2026-09-30", no: "Not eligible for the daily check-in", auto: "Auto check-in", now: "Check in now" },
  zh: { done: "今日已签到 +50 · 连续 3 天", off: "自动签到已关闭 · 上次签到于 2026-09-30", wait: "今日尚未签到 · 上次签到于 2026-09-30", no: "不符合签到条件", auto: "自动签到", now: "立即签到" },
};

const scrolls = (page) => page.evaluate(() => [scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)].join(","));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: the WorkBuddy card says how its check-in went`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 900 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [], asked = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, asked));
      await page.goto("http://magpie.test/?view=usage");
      const card = page.locator(".subscription-card", { hasText: "WorkBuddy" });
      // every account in full, so each row is there to read
      await card.locator(".wb-checkin").first().waitFor();
      const rows = card.locator(".wb-checkin");
      assert.equal(await rows.count(), 3, "one line an account");
      const says = (await rows.locator(".ci-say").allInnerTexts()).map((s) => s.trim());
      assert.deepEqual(says, [w.done, w.off, w.no]);
      assert.deepEqual(await rows.evaluateAll((rs) => rs.map((r) => r.dataset.state)), ["ok", "none", "none"]);
      assert.equal(await page.locator(".subscription-card", { hasText: "Codex" }).locator(".wb-checkin").count(), 0);
      // the switch once, on the first row, off as Settings has it
      const auto = card.locator(".ci-auto");
      assert.equal(await auto.count(), 1);
      assert.equal((await auto.innerText()).trim(), w.auto);
      assert.equal(await auto.getAttribute("aria-pressed"), "false");
      assert.equal(await card.locator(".ci-now").count(), 1, "韩梅梅 isn't in today");
      if (process.env.ARTIFACT_DIR) await card.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `usage-checkin-${engine}-${lang}.png`) });

      // turned on from the card: saved as Settings' is, the page left where it was
      const y = await scrolls(page);
      await auto.click();
      await page.waitForFunction(() => document.querySelector(".ci-auto")?.getAttribute("aria-pressed") === "true");
      assert.deepEqual(asked, [["set", { on: true }]]);
      assert.equal(await scrolls(page), y, "the toggle moved the page");
      assert.equal((await rows.nth(1).locator(".ci-say").innerText()).trim(), w.wait);
      assert.equal(await rows.nth(1).getAttribute("data-state"), "wait");

      // Check in now presses it, and the cards are read again
      const before = asked.length;
      const reread = page.waitForRequest((r) => new URL(r.url()).pathname === "/api/usage/quotas");
      await card.locator(".ci-now").click();
      await reread;
      assert.deepEqual(asked.slice(before), [["now"]]);
      assert.equal(await page.locator("select").count(), 0);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: the Trae CN card has its own check-in`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 900 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [], asked = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, asked));
      await page.goto("http://magpie.test/?view=usage");
      const card = page.locator(".subscription-card", { hasText: "Trae CN" });
      await card.locator(".wb-checkin").first().waitFor();
      assert.equal(await card.locator(".wb-checkin").count(), 1);
      assert.equal((await card.locator(".ci-say").innerText()).trim(), lang === "en" ? "Auto check-in is off · last checked in 2026-09-30" : "自动签到已关闭 · 上次签到于 2026-09-30");
      assert.match(await card.locator(".ci-say").getAttribute("title"), /Trae/);
      // its own switch and press, beside WorkBuddy's
      assert.equal(await page.locator(".ci-auto").count(), 2);
      const auto = card.locator(".ci-auto");
      assert.equal((await auto.innerText()).trim(), w.auto);
      assert.match(await auto.getAttribute("title"), /Trae CN/);
      const y = await scrolls(page);
      await auto.click();
      await page.waitForFunction(() => document.querySelectorAll(".ci-auto[aria-pressed=true]").length === 1);
      assert.equal(await auto.getAttribute("aria-pressed"), "true");
      assert.deepEqual(asked, [["trae-set", { on: true }]]);
      assert.equal(await scrolls(page), y, "the toggle moved the page");
      assert.equal(await page.locator(".subscription-card", { hasText: "WorkBuddy" }).locator(".ci-auto").getAttribute("aria-pressed"), "false", "WorkBuddy's switch changed too");
      const before = asked.length;
      const reread = page.waitForRequest((r) => new URL(r.url()).pathname === "/api/usage/quotas");
      await card.locator(".ci-now").click();
      await reread;
      assert.deepEqual(asked.slice(before), [["trae-now"]]);
      assert.deepEqual(errors, []);
    });
  }
}
