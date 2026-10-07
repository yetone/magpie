// Run with Node's test runner and Playwright on the module path; see README.md.
// Library → RTK's chart of what RTK saved, where the clocks skip 00:00 two
// weeks before today: the browser put that day's 00:00 at 01:00, each day
// after it kept the 01:00, and the chart ended a day short, with no bar for
// today and today's commands left out of its total; past 92 days, by week,
// the week that begins today had no bar. Now 30 days are 30 bars, 90 days 90
// and All 15 weeks, each ending with today. No backend: the API is faked
// here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
// the day the clocks skip 00:00, and noon two weeks later in UTC: today
const zones = {
  "America/Santiago": ["2026-09-06", "2026-09-20T15:00:00Z"],
  "America/Havana": ["2026-03-08", "2026-03-22T16:00:00Z"],
  "Atlantic/Azores": ["2026-03-29", "2026-04-12T12:00:00Z"],
  "Asia/Beirut": ["2026-03-29", "2026-04-12T09:00:00Z"],
  "Africa/Cairo": ["2026-04-24", "2026-05-08T09:00:00Z"],
};
const daysBefore = (date, n) => new Date(Date.parse(date + "T12:00:00Z") - n * 864e5).toISOString().slice(0, 10);

function server(days) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang: "en", theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: { dir: "/home/u/.magpie/library", backups: "/home/u/.magpie/backups", home: "/home/u", agents: [{ id: "codex", name: "Codex", icon: "", skills: "/home/u/.codex/skills", mcp: "/home/u/.codex/mcp.json" }], instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [], servers: [] } });
    if (url.pathname === "/api/library/rtk") return route.fulfill({ json: { path: "/home/u/.local/bin/rtk", version: "0.51.0", url: "https://www.rtk-ai.app", agents: [{ id: "codex", name: "Codex", icon: "", on: true }], gain: { commands: 10, input: 3000, saved: 2400, pct: 80 }, days } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": RTK's chart ends with today past a skipped midnight", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const [zone, [skipped, now]] of Object.entries(zones)) {
      await t.test(zone, async (t) => {
        const today = now.slice(0, 10), [, m, d] = today.split("-").map(Number), label = `${m}/${d}`;
        // the first day 14 weeks back, so All is by week and a week begins today
        const days = [
          { date: daysBefore(today, 98), commands: 2, input: 1000, saved: 800 },
          { date: skipped, commands: 3, input: 1000, saved: 800 },
          { date: today, commands: 5, input: 1000, saved: 800 },
        ];
        const ctx = await browser.newContext({ viewport: { width: 980, height: 800 }, timezoneId: zone });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "rtk"); localStorage.setItem("magpie.rtkRange", "30"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.clock.setFixedTime(now);
        await page.route("**/*", server(days));
        await page.goto("http://magpie.test/");
        const clock = await page.evaluate((s) => { const [y, m, d] = s.split("-").map(Number); return [new Date().getHours(), new Date(y, m - 1, d).getHours()]; }, skipped);
        assert.deepEqual(clock, [12, 1], `${zone} isn't at noon, or doesn't skip 00:00 on ${skipped}`);
        await page.locator('button[data-view="library"]').click();
        const chart = page.locator("#view-library .lib-rtk-chart");
        // each range: how many bars, what the last is, the commands in the total
        for (const [range, bars, last, commands] of [["30 days", 30, label, 8], ["90 days", 90, label, 8], ["All", 15, "week of " + label, 10]]) {
          await t.test(range, async () => {
            await chart.locator(".segs button", { hasText: range }).click();
            await chart.locator(".segs button.on", { hasText: range }).waitFor();
            const bar = chart.locator(".bars > .bar");
            const total = await chart.locator(".lib-rtk-foot > span").last().textContent();
            const got = {
              bars: await bar.count(),
              last: (await bar.last().getAttribute("title")).split(" · ")[0],
              commands: +(total.match(/over (\d+) commands/)?.[1] ?? 0),
            };
            assert.deepEqual(got, { bars, last, commands }, `${range} doesn't end with today, ${label}: ${total}`);
          });
        }
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
