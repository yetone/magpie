// Run with Node's test runner and Playwright on the module path; see README.md.
// Haoxiang581607 on X: "I used to see my three plugins (WorkBuddy, its
// international one and agy) in three columns, read WorkBuddy's credits and
// click Auto check-in right there". With the allowances as a table, an
// account's daily check-in was gone, and with it the switch. Now an
// account with a check-in says how today's went under its name, as its
// card does; the vendor's first row has Auto check-in (the same switch,
// POST /api/settings/plugin-checkin) and Check in now while an account
// isn't in. An account with no check-in (WorkBuddy AI, agy) has none. A
// provider of several accounts, folded, has the switch on its own row,
// and its accounts each their day once it is opened. A click never moves
// the page, and a narrow window scrolls the table in its own box. Both
// engines, every language, wide and narrow; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const today = new Date(Date.now() + 8 * 3600e3).toISOString().slice(0, 10);
const reset = new Date(Date.now() + 3 * 3600e3).toISOString();
// the reporter's three plugins, and a vendor of two accounts with its own
// check-in
const quotas = () => [
  { provider: "workbuddy", name: "WorkBuddy", icon: "workbuddy-color", plan: "Pro", user: "xiang@example.com",
    windows: [{ name: "Credits", used: 24, amount: 1200, limit: 5000, unit: "credits" }],
    checkins: true, checkinBy: "plugin:workbuddy", checkin: { user: "xiang@example.com", day: today, outcome: "claimed", credit: 50, streak: 3 } },
  { provider: "workbuddy-ai", name: "WorkBuddy AI", icon: "workbuddy-color", plan: "Free", user: "xiang.ai@example.com",
    windows: [{ name: "Credits", used: 40, amount: 40, limit: 100, unit: "credits" }] },
  { provider: "agy-cli", name: "Antigravity", icon: "antigravity-color", plan: "Google AI Pro", user: "xiang.agy@example.com",
    windows: [{ name: "Gemini Pro", family: "Gemini", used: 30, resetsAt: reset }, { name: "Claude", family: "Claude", used: 10, resetsAt: reset }] },
  { provider: "trae-cn", name: "Trae CN", plan: "Free", user: "hu", windows: [{ name: "Credits", used: 3 }],
    checkins: true, checkinBy: "plugin:trae-cn", checkin: { user: "hu", day: today, outcome: "claimed", credit: 100 } },
  { provider: "trae-cn", name: "Trae CN", plan: "Free", user: "mei", windows: [{ name: "Credits", used: 7 }],
    checkins: true, checkinBy: "plugin:trae-cn", checkin: { user: "mei", day: "2026-09-30", outcome: "claimed", credit: 100 } },
];

function serve(lang, asked) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", pluginCheckins: { workbuddy: false, "trae-cn": false } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/settings/plugin-checkin") {
      const body = route.request().postDataJSON();
      asked.push(["set", body]);
      settings.pluginCheckins = { ...settings.pluginCheckins, [body.provider]: body.on };
      return json(settings);
    }
    if (url.pathname === "/api/usage/plugin-checkin") {
      asked.push(["now", route.request().postDataJSON()]);
      return json([]);
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

const IN = { en: "Checked in today", zh: "今日已签到", "zh-TW": "今日已簽到", ja: "今日はチェックイン済み", de: "Heute eingecheckt" };
const STREAK = { en: "3-day streak", zh: "连续 3 天", "zh-TW": "連續 3 天", ja: "3 日連続", de: "3 Tage in Folge" };
const AUTO = { en: "Auto check-in", zh: "自动签到", "zh-TW": "自動簽到", ja: "自動チェックイン", de: "Automatischer Check-in" };
const NOW = { en: "Check in now", zh: "立即签到", "zh-TW": "立即簽到", ja: "今すぐチェックイン", de: "Jetzt einchecken" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: the table keeps each account's check-in and Auto check-in`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const ctx = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => localStorage.setItem("magpie.quotaView", JSON.stringify("table")));
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const asked = [];
        await page.route("**/*", serve(lang, asked));
        await page.goto("http://magpie.test/?view=usage");
        await page.waitForSelector("#subscriptionUsage .qt");

        const row = (p) => page.locator(`.qt tr[data-provider="${p}"]:not(.qt-group)`);
        // WorkBuddy: today's check-in under its name, and the switch
        const wb = row("workbuddy");
        await wb.locator(".wb-checkin").waitFor();
        const say = await wb.locator(".wb-checkin .ci-say").innerText();
        assert.ok(say.includes(IN[lang]) && say.includes("+50") && say.includes(STREAK[lang]), say);
        assert.equal(await wb.locator(".ci-auto").innerText(), AUTO[lang]);
        assert.equal(await wb.locator(".ci-auto").getAttribute("aria-pressed"), "false");
        // its credits are still in the table
        assert.ok((await wb.locator(".qt-w").allInnerTexts()).join(" ").includes("24%"));
        // WorkBuddy AI and agy have no check-in, so none is drawn
        for (const p of ["workbuddy-ai", "agy-cli"]) {
          assert.equal(await row(p).count(), 1, p);
          assert.equal(await row(p).locator(".wb-checkin").count(), 0, p + " has no check-in");
        }

        // Trae CN, two accounts folded: the switch on its row, and Check
        // in now for the one not in today; no account's day on it
        const group = page.locator('.qt tr.qt-group[data-provider="trae-cn"]');
        assert.equal(await group.locator(".ci-auto").innerText(), AUTO[lang]);
        assert.equal(await group.locator(".ci-now").innerText(), NOW[lang]);
        assert.equal(await group.locator(".ci-say").count(), 0);
        assert.equal(await row("trae-cn").count(), 0, "folded");

        // the switch turns the plugin's daily check-in on, in place
        const y = await page.evaluate(() => document.scrollingElement.scrollTop);
        await wb.locator(".ci-auto").click();
        await page.waitForFunction(() => document.querySelector('.qt tr[data-provider="workbuddy"] .ci-auto')?.getAttribute("aria-pressed") === "true");
        assert.deepEqual(asked.filter(([k]) => k === "set").at(-1), ["set", { provider: "workbuddy", on: true }]);
        assert.equal(await page.locator(".qt").count(), 1, "still a table");
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), y, "the page did not move");

        // the folded row's switch is its own, and doesn't open the row
        await group.locator(".ci-auto").click();
        await page.waitForFunction(() => document.querySelector('.qt tr.qt-group[data-provider="trae-cn"] .ci-auto')?.getAttribute("aria-pressed") === "true");
        assert.deepEqual(asked.filter(([k]) => k === "set").at(-1), ["set", { provider: "trae-cn", on: true }]);
        assert.equal(await row("trae-cn").count(), 0, "still folded");

        // opened: each account its day, the switch on the first alone
        await group.locator(".qt-fold").click();
        await row("trae-cn").first().waitFor();
        assert.equal(await row("trae-cn").count(), 2);
        assert.equal(await row("trae-cn").locator(".ci-say").count(), 2);
        assert.ok((await row("trae-cn").nth(0).locator(".ci-say").innerText()).includes(IN[lang]));
        assert.equal(await row("trae-cn").nth(0).locator(".ci-auto").count(), 1);
        assert.equal(await row("trae-cn").nth(1).locator(".ci-auto").count(), 0);
        assert.equal(await group.locator(".wb-checkin").count(), 0, "an open row's accounts say it");

        // a narrow window scrolls the table in its own box, never the page
        assert.ok(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth), "the page doesn't scroll sideways");
        // the line stays inside its cell
        const fits = await wb.evaluate((tr) => {
          const cell = tr.querySelector(".qt-sub").getBoundingClientRect(), line = tr.querySelector(".wb-checkin").getBoundingClientRect();
          return line.left >= cell.left - 0.5 && line.right <= cell.right + 0.5;
        });
        assert.ok(fits, "the check-in line fits its cell");
        assert.deepEqual(errors, []);
      });
    }
  }
}
