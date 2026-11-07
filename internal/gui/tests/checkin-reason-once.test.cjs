// Run with Node's test runner and Playwright on the module path; see README.md.
// leeoswald368 (Discord): many Qoder accounts whose check-in failed for one
// long reason repeated it once per account, a wall of text on the Usage
// page's cards and in its provider editor's check-in line. The reason is now said once:
// on the first card of the vendor's that has it (the others keep it in the
// tooltip), and in the provider's editor once, after every account it covers. A
// different reason is still said on its own. English and Chinese, the
// window and the panel's 440px; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, provPrefsProviders, openProvPrefs } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const today = new Date(Date.now() + 8 * 3600e3).toISOString().slice(0, 10);
const WHY = "Qoder check-in: device identity runtime not found. Qoder gives the daily credits only to a device its own app identifies, and this machine has neither Qoder desktop nor Qoder CLI signed in; install one and sign in, and magpie checks in again the next day";
const OTHER = "this device has checked in today";
const checkins = [
  ...["a1", "a2", "a3", "a4"].map((user) => ({ user, by: "plugin:qoder", vendor: "Qoder", day: today, outcome: "ineligible", msg: WHY })),
  { user: "b1", by: "plugin:qoder", vendor: "Qoder", day: today, outcome: "ineligible", msg: OTHER },
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
  en: { ineligible: "Not eligible for the daily check-in", all: "a1, a2, a3, a4 is not eligible", other: "b1 is not eligible" },
  zh: { ineligible: "不符合签到条件", all: "a1、a2、a3、a4 不符合签到条件", other: "b1 不符合签到条件" },
};
const count = (s, x) => s.split(x).length - 1;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    for (const width of [900, 440]) {
      test(`${engine} ${lang} ${width}px: one check-in reason of many accounts is said once`, async (t) => {
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
        await page.locator(".wb-checkin").nth(4).waitFor();
        const says = (await page.locator(".wb-checkin .ci-say").allInnerTexts()).map((s) => s.trim());
        assert.deepEqual(says, [w.ineligible + " · " + WHY, w.ineligible, w.ineligible, w.ineligible, w.ineligible + " · " + OTHER]);
        // the others keep it where it is asked for
        for (let i = 1; i < 4; i++) assert.ok((await page.locator(".wb-checkin .ci-say").nth(i).getAttribute("title")).includes(WHY), "card " + i + "'s tooltip");
        assert.equal(count(await page.locator("#view-usage").innerText(), WHY), 1);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "no sideways scroll on Usage");

        // the editor's line: the accounts, then the reason once
        await openProvPrefs(page, "qoder");
        await settingsDrawn(page);
        const sub = page.locator('.editor .prov-prefs .ci-plugin[data-provider="qoder"] > .row.pref:first-child .sub');
        await sub.waitFor();
        const text = (await sub.innerText()).replace(/\s+/g, " ");
        assert.ok(text.includes(w.all + " · " + WHY), text);
        assert.ok(text.includes(w.other + " · " + OTHER), text);
        assert.equal(count(text, WHY), 1, text);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "no sideways scroll in the editor");
        assert.deepEqual(errors, []);
      });
    }
  }
}
