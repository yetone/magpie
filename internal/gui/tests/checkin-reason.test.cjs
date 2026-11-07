// Run with Node's test runner and Playwright on the module path; see README.md.
// plugins#304 (ReneXiong): Qoder's plugin read this machine's own device
// identity, sent it, and was offered no daily credits; it said why in its
// message, and magpie showed a bare "No check-in event now". An inactive or
// ineligible check-in now shows the plugin's (or vendor's) message beside
// the label, on the account's Usage card, wrapped rather than cut off, and
// in its provider editor's check-in line; one without a message reads as before.
// English, Simplified and Traditional Chinese, Japanese and German, the
// window and the panel's 440px; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, provPrefsProviders, openProvPrefs } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const today = new Date(Date.now() + 8 * 3600e3).toISOString().slice(0, 10);
const WHY = "Qoder check-in: Qoder offered this device's identity, read from this machine's own Qoder runtime-info, no daily credits campaign right now. Qoder decides per device whether to offer it; if Qoder desktop shows today's credits, claim them there";
const NOPE = "this device has checked in today";
const checkins = [
  { user: "arno", by: "plugin:qoder", vendor: "Qoder", day: today, outcome: "inactive", msg: WHY },
  { user: "lei", by: "plugin:qoder", vendor: "Qoder", day: today, outcome: "ineligible", msg: NOPE },
  { user: "ann", by: "plugin:qoder", vendor: "Qoder", day: today, outcome: "inactive" },
];
const quotas = () => checkins.map((c) => ({
  provider: "plugin:qoder", name: "Qoder", plan: "Pro", user: c.user, windows: [{ name: "Credits", used: 1 }],
  checkins: true, checkinBy: "plugin:qoder", checkin: c,
}));

function serve(lang) {
  const settings = { theme: "light", lang, quotaLeft: false, currency: "usd", pluginCheckins: { qoder: true },
    checkinPlugins: [{ id: "qoder", name: "Qoder", on: true, checkins }] };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") {
      const { checkinPlugins, ...saved } = settings;
      return json({ agents: [], profiles: [], settings: saved });
    }
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json(provPrefsProviders(["qoder"], { qoder: "Qoder" }));
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
  en: { inactive: "No check-in event now", ineligible: "Not eligible for the daily check-in", user: "arno: no check-in event now", nope: "lei is not eligible" },
  zh: { inactive: "当前没有签到活动", ineligible: "不符合签到条件", user: "arno：当前没有签到活动", nope: "lei 不符合签到条件" },
  "zh-TW": { inactive: "目前沒有簽到活動", ineligible: "不符合簽到條件", user: "arno：目前沒有簽到活動", nope: "lei 不符合簽到條件" },
  ja: { inactive: "現在チェックインイベントはありません", ineligible: "毎日のチェックインの対象外です", user: "arno：現在チェックインイベントはありません", nope: "lei は対象外です" },
  de: { inactive: "Derzeit keine Check-in-Aktion", ineligible: "Nicht berechtigt für den täglichen Check-in", user: "arno: derzeit keine Check-in-Aktion", nope: "lei ist nicht berechtigt" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    const w = words[lang];
    for (const width of [900, 440]) {
      test(`${engine} ${lang} ${width}px: an inactive or ineligible check-in says the plugin's reason`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; errors.push(e.message); }); // a goto in WebKit can cut off a fetch (#1350)
        await page.route("**/*", serve(lang));
        await watchSettingsReads(page);
        await page.goto("http://magpie.test/?view=usage");
        await settingsDrawn(page);
        const of = (user) => page.locator(".wb-checkin").nth(checkins.findIndex((c) => c.user === user));
        await page.locator(".wb-checkin").nth(2).waitFor();
        const says = (await page.locator(".wb-checkin .ci-say").allInnerTexts()).map((s) => s.trim());
        assert.equal(says[0], w.inactive + " · " + WHY, "the plugin's reason beside the label");
        assert.equal(says[1], w.ineligible + " · " + NOPE);
        assert.equal(says[2], w.inactive, "no message reads as before");
        // the reason wraps in the row: cut off with "…" it read as none
        const span = page.locator(".wb-checkin .ci-say > span").first();
        const fits = await span.evaluate((e) => e.scrollWidth <= e.clientWidth + 1 && e.getBoundingClientRect().height > 20);
        assert.ok(fits, "the reason wraps rather than being cut off");
        assert.equal(await of("arno").getAttribute("data-why"), "");
        assert.equal(await of("ann").getAttribute("data-why"), null);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "no sideways scroll on Usage");

        // the check-in line under the plugin's switch, in Qoder's editor, says it too
        await openProvPrefs(page, "qoder");
        await settingsDrawn(page);
        const sub = page.locator('.editor .prov-prefs .ci-plugin[data-provider="qoder"] > .row.pref:first-child .sub');
        await sub.waitFor();
        const text = (await sub.innerText()).replace(/\s+/g, " ");
        assert.ok(text.includes(w.user + " · " + WHY), text);
        assert.ok(text.includes(w.nope + " · " + NOPE), text);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "no sideways scroll in the editor");
        assert.deepEqual(errors, []);
      });
    }
  }
}
