// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Usage › Advanced › Time zone (JackyFill on X): a magpie set to a
// zone of its own shows every time in it, whatever zone the browser keeps.
// The row is the app's own menu under Advanced; picking a zone saves
// timeZone and loads the page again, whose boot.js then names the zone and
// carries tzshim.js. With the shim, the page's Date and Intl answer as a
// browser kept in that zone answers: the same probe is run in a page of a
// context in Los Angeles with no shim, and in the app in a Tokyo context
// set to Los Angeles, and must give the same. Vectorpan on X keeps the
// computer in Los Angeles' time and wants magpie alone in China's: a page in
// a Los Angeles context, set to Asia/Shanghai, reads as one in Shanghai. No
// backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { openPart } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const LA = "America/Los_Angeles";
const SH = "Asia/Shanghai";

const usage = {
  calls: 5, errors: 0, input: 12000, output: 4000, cache_read: 500, cache_write: 100, reasoning: 200,
  unpriced: 0, cost: 12.34, bucket: "day",
  series: [{ label: "Mon", input: 6000, output: 2000, calls: 3, cost: 7 }],
  agents: [{ name: "Claude Code", calls: 5, cost: 12.34 }],
  models: [{ name: "claude-opus", calls: 5, cost: 12.34 }],
};

function settingsPayload(over) {
  return {
    theme: "light", lang: "en", tray: "panel", quotaLeft: false, currency: "usd", dayStart: 0,
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: "2026-09-30T00:00:00Z", stale: false },
    ...over,
  };
}

function server(lang, saved, shim, zone) {
  let cur = settingsPayload({ lang, ...(zone ? { timeZone: zone } : {}) });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") {
      // as api.go serves it: the zone named, then the shim, only when set
      const boot = { lang, theme: "light", web: false, ...(cur.timeZone ? { timeZone: cur.timeZone } : {}) };
      return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify(boot)};\n` + (cur.timeZone ? shim : "") });
    }
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: cur, fx: cur.fx });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") { cur = { ...cur, ...req.postDataJSON() }; saved.push(req.postDataJSON()); }
      return json(cur);
    }
    if (url.pathname === "/api/usage") return json(usage);
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// probe is what the page reads of the clock: a year of times every 7h13m
// (through both changes of clocks), times from parts across them, the
// setters, strings parsed with and without a zone, and the strings made.
function probe() {
  const out = [];
  for (let t = Date.UTC(2026, 0, 1); t < Date.UTC(2027, 0, 1); t += 7 * 36e5 + 13 * 6e4) {
    const d = new Date(t);
    out.push([d.getFullYear(), d.getMonth(), d.getDate(), d.getDay(), d.getHours(), d.getMinutes(), d.getSeconds(), d.getTimezoneOffset(), d.toString().replace(/ \(.*\)$/, ""), d.toDateString()]);
  }
  for (const [m, d, h, mi] of [[2, 8, 1, 59], [2, 8, 2, 0], [2, 8, 2, 30], [2, 8, 3, 0], [10, 1, 0, 59], [10, 1, 1, 0], [10, 1, 1, 30], [10, 1, 2, 0], [5, 15, 12, 0], [0, 1, 0, 0]]) {
    out.push(new Date(2026, m, d, h, mi).getTime());
  }
  out.push(new Date(2026, 8).getTime(), new Date(2026, 8, 30, 24, 0).getTime(), new Date(2026, 8, 30, 10, 0, 5, 7).getTime());
  const x = new Date(Date.UTC(2026, 2, 7, 20));
  x.setDate(8);
  out.push(x.getTime());
  x.setHours(2, 30);
  out.push(x.getTime());
  x.setMonth(10, 1);
  x.setHours(1, 30, 0, 0);
  out.push(x.getTime());
  x.setFullYear(2027);
  x.setMinutes(0);
  x.setSeconds(9);
  out.push(x.getTime());
  out.push(Date.parse("2026-09-30T00:00"), Date.parse("2026-09-30"), Date.parse("2026-09-30T00:00Z"), Date.parse("2026-09-30T00:00:00+08:00"),
    Date.parse("Sep 30 2026 10:00"), new Date("2026/09/30 10:00").getTime(), Date.parse("Wed, 30 Sep 2026 10:00:00 GMT"), String(Date.parse("not a date")));
  const at = new Date(Date.UTC(2026, 8, 30, 3));
  out.push(at.toLocaleString("en-US"), at.toLocaleDateString("en-US"), at.toLocaleTimeString("en-US"), at.toLocaleString("en-US", { timeZone: "Asia/Kolkata" }),
    new Intl.DateTimeFormat("en-US", { hour: "numeric", minute: "numeric" }).format(at), Intl.DateTimeFormat().resolvedOptions().timeZone,
    at.toISOString(), JSON.stringify(at), at.valueOf());
  out.push(typeof Date(), new Date(8.64e15 + 1).getTime(), String(new Date(NaN)), new Date(NaN).getHours(), new Date(at) - at, new Date() instanceof Date,
    Object.prototype.toString.call(new Date()), new Date(0).constructor === Date, Math.abs(Date.now() - new Date().getTime()) < 60000);
  return JSON.stringify(out);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a time zone of this magpie's own", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const shim = await fs.readFile(path.join(assets, "tzshim.js"), "utf8");

    // what a browser kept in Los Angeles reads, with no shim
    const native = await browser.newContext({ timezoneId: LA });
    const want = await (await native.newPage()).evaluate(probe);
    await native.close();

    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(lang, async () => {
        const errors = [], saved = [];
        const context = await browser.newContext({ viewport: { width: 900, height: 600 }, reducedMotion: "reduce", timezoneId: "Asia/Tokyo" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, saved, shim));

        await page.goto("http://magpie.test/?view=usage");
        await page.locator("#usageCost b").waitFor();
        // unset: the browser's own zone, Tokyo, and no shim
        assert.equal(await page.evaluate(() => new Date(Date.UTC(2026, 8, 30, 3)).getHours()), 12);
        assert.equal(await page.evaluate(() => "timeZone" in window.bootPrefs), false);

        await page.locator("#prefs").click();
        await openPart(page, "usage");
        await page.locator("#timeZoneVal .pref-pick").waitFor({ state: "attached" });
        const more = page.locator("#usageMore");
        assert.equal(await more.evaluate((d) => d.open), false, "Advanced starts folded");
        assert.equal(await page.locator("#timeZoneVal select").count(), 0, "never a native select");
        await more.locator("summary").click();
        const pick = page.locator("#timeZoneVal .pref-pick");
        const system = (await pick.textContent()).trim();
        assert.equal(system, { en: "System", zh: "跟随系统", "zh-TW": "跟隨系統", ja: "システム", de: "System" }[lang], "unset, the button says System");
        const label = (await page.locator("#timeZoneRow .name").textContent()).trim();
        assert.equal(label, { en: "Time zone", zh: "时区", "zh-TW": "時區", ja: "タイムゾーン", de: "Zeitzone" }[lang]);
        assert.equal(await pick.getAttribute("aria-label"), label);

        await pick.click();
        const menu = page.locator(".sess-menu");
        await menu.waitFor();
        assert.equal((await menu.locator(".pm-head").textContent()).trim(), label);
        await menu.locator(".sc-q").fill("Los_Angeles");
        const item = menu.locator(`.pm-item[title="${LA}"]`);
        assert.match((await item.locator(".pm-note").textContent()).trim(), /^UTC-0[78]:00$/);
        const reloaded = page.waitForEvent("load");
        await item.click();
        await reloaded;
        assert.equal(saved.at(-1).timeZone, LA, "the pick is saved");

        // loaded again where it was, in Settings › Usage: boot.js names the
        // zone and the page reads it
        await pick.waitFor();
        assert.match(page.url(), /view=settings&tab=usage/);
        assert.equal(await page.evaluate(() => window.bootPrefs.timeZone), LA);
        assert.equal(await page.evaluate(probe), want, "the page's clock reads as a browser in Los Angeles");
        assert.equal(await more.evaluate((d) => d.open), true, "a zone set shows Advanced open");
        assert.equal((await pick.textContent()).trim(), LA);

        // System again: saved as none, and the browser's zone is back
        await pick.click();
        await menu.waitFor();
        const back = page.waitForEvent("load");
        await menu.locator(".pm-item").first().click();
        await back;
        assert.equal(saved.at(-1).timeZone, "");
        await pick.waitFor({ state: "attached" });
        assert.equal(await page.evaluate(() => "timeZone" in window.bootPrefs), false);
        assert.equal(await page.evaluate(() => new Date(Date.UTC(2026, 8, 30, 3)).getHours()), 12);
        assert.deepEqual(errors, []);
        await context.close();
      });
    }
  });

  test(engine + ": a computer in Los Angeles, magpie in Shanghai", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const shim = await fs.readFile(path.join(assets, "tzshim.js"), "utf8");
    const native = await browser.newContext({ timezoneId: SH });
    const want = await (await native.newPage()).evaluate(probe);
    await native.close();
    const errors = [];
    const context = await browser.newContext({ viewport: { width: 900, height: 600 }, reducedMotion: "reduce", timezoneId: LA });
    const page = await context.newPage();
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", server("zh", [], shim, SH));
    await page.goto("http://magpie.test/?view=usage");
    await page.locator("#usageCost b").waitFor();
    assert.equal(await page.evaluate(() => window.bootPrefs.timeZone), SH);
    // 03:00 UTC on Sep 30 is 11:00 on the 30th in Shanghai, 20:00 on the 29th in LA
    assert.deepEqual(await page.evaluate(() => { const d = new Date(Date.UTC(2026, 8, 30, 3)); return [d.getDate(), d.getHours()]; }), [30, 11]);
    assert.equal(await page.evaluate(probe), want, "the page's clock reads as a browser in Shanghai");
    await page.locator("#prefs").click();
    await openPart(page, "usage");
    const pick = page.locator("#timeZoneVal .pref-pick");
    await pick.waitFor();
    assert.equal((await pick.textContent()).trim(), SH);
    assert.deepEqual(errors, []);
    await context.close();
  });
}
