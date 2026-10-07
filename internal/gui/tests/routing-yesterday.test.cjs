// Run with Node's test runner and Playwright on the module path; see README.md.
// Routing › Requests' day bar names the day before today "yesterday" when
// the clocks have just changed too. Yesterday was taken as 24 hours ago: the
// day after a 23-hour day that is two days back from 00:00 to 00:59, so the
// day before yesterday was named "yesterday" and yesterday its date; on a
// 25-hour day it is still today from 23:00, so no day was "yesterday".
// New York goes forward at 02:00, Santiago at midnight (00:00–00:59 never
// happens on the day it goes forward).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const yesterday = { en: "yesterday", zh: "昨天", ja: "昨日", de: "gestern" };
const today = { en: "today", zh: "今天", ja: "今日", de: "heute" };
const cases = [
  // 2026-03-08 had 23 hours in New York; 00:30 EDT on the 9th
  { zone: "America/New_York", now: "2026-03-09T04:30:00Z", days: ["2026-03-09", "2026-03-08", "2026-03-07"] },
  // 2026-09-06 had 23 hours in Santiago, from 01:00; 00:30 on the 7th
  { zone: "America/Santiago", now: "2026-09-07T03:30:00Z", days: ["2026-09-07", "2026-09-06", "2026-09-05"] },
  // 2026-11-01 has 25 hours in New York; 23:30 EST that day
  { zone: "America/New_York", now: "2026-11-02T04:30:00Z", days: ["2026-11-01", "2026-10-31", "2026-10-30"] },
];

function serve(lang, now, days) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "fixture", name: "Fixture", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now, seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: days.map((day, i) => ({ day, requests: 10 + i })), routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: just after the clocks change, yesterday is yesterday`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const c of cases) {
      for (const lang of ["en", "zh", "ja", "de"]) {
        const context = await browser.newContext({ viewport: { width: 1100, height: 760 }, timezoneId: c.zone, locale: "en-US" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.clock.setFixedTime(c.now);
        await page.route("**/*", serve(lang, c.now, c.days));
        await page.goto("http://magpie.test/?view=routing");
        const pills = page.locator(".rt-days .rt-day");
        await pills.nth(c.days.length).waitFor();
        // Live, then the days newest first: each pill's name, less its count
        const names = await pills.evaluateAll((bs) => bs.slice(1).map((b) => b.querySelector("span").textContent));
        const at = `${c.zone} at ${c.now}, ${lang}`;
        assert.equal(names[0], today[lang], `${at}: ${c.days[0]} is today: ${names}`);
        assert.equal(names[1], yesterday[lang], `${at}: ${c.days[1]} is yesterday: ${names}`);
        assert.notEqual(names[2], yesterday[lang], `${at}: ${c.days[2]} is not yesterday: ${names}`);
        assert.match(names[2], new RegExp(`\\b${Number(c.days[2].slice(8))}\\b`), `${at}: ${c.days[2]} is named by its date: ${names}`);
        assert.deepEqual(errors, []);
        await context.close();
      }
    }
  });
}
