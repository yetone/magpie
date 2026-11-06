// Run with Node's test runner and Playwright on the module path; see README.md.
// Routing › Requests' day bar shows the days that fit and keeps the rest
// under its last pill (#961): the bar scrolled with its scrollbar hidden, so
// with 30 days kept the later ones were cut off at its edge and a mouse wheel
// couldn't reach them. Every day is in reach, none is cut, an earlier day
// picked from the menu names the pill, and nothing moves the page.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const ymd = (d) => [d.getFullYear(), d.getMonth() + 1, d.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
// the 30 days the gateway keeps, newest first, as /api/gateway/history lists them
const allDays = Array.from({ length: 30 }, (_, i) => ({ day: ymd(new Date(now.getTime() - i * 864e5)), requests: 100 + i }));
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const who = { id: "fixture-key", provider: "fixture", name: "Fixture", who: "key-1", kind: "key", model: "model-a", routing: "", used: 0 };
// a day's requests fill the window, as a day of 100 or more does: the bar
// held when a day comes in needs the room under it to scroll into
const routes = Array.from({ length: 12 }, (_, i) => ({
  id: 100 - i, seq: 100 - i, time: at(i), agent: "fixture", model: "model-a", provider: "fixture",
  order: [who], tries: [{ id: who.id, model: "model-a", start: at(i), done: true, status: 200, ms: 1200 }],
  done: true, status: 200, ms: 1200, tokens: 3000,
}));

function serve(lang, days, asked) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "fixture", name: "Fixture", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") {
      const d = url.searchParams.get("day");
      if (d) asked.push(d);
      return json({ cut: false, days, routes: d ? routes : [] });
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// what the bar shows: each pill in sight, its box against the bar's
const seen = (page) => page.locator(".rt-days").evaluate((bar) => {
  const b = bar.getBoundingClientRect();
  return [...bar.children].filter((x) => !x.hidden && x.getBoundingClientRect().width > 0).map((x) => {
    const r = x.getBoundingClientRect();
    return { more: x.classList.contains("rt-day-more"), text: x.innerText.replace(/\s+/g, " ").trim(), on: x.getAttribute("aria-pressed") === "true", cut: r.right > b.right + 0.5 || r.left < b.left - 0.5 };
  });
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    test(`${engine} ${lang}: every day the bar keeps is in reach, none cut off`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      // 733 is 1100 at the reporter's 150% text size, which zooms the window
      for (const width of [1100, 733, 440]) {
        const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], asked = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, allDays, asked));
        await page.goto("http://magpie.test/?view=routing");
        await page.locator(".rt-days .rt-day-more").waitFor();
        await page.waitForTimeout(200);

        let pills = await seen(page);
        assert(!pills.some((p) => p.cut), `${width}px: a pill is cut off at the bar's edge: ${JSON.stringify(pills)}`);
        assert(pills.at(-1).more, `${width}px: the last pill in sight holds the rest`);
        const inBar = pills.length - 2; // less Live and the pill
        assert(inBar >= 0 && inBar < allDays.length, `${width}px: ${inBar} days in the bar`);

        // the menu lists every day not in the bar, oldest last, with its count
        const more = page.locator(".rt-days .rt-day-more");
        await more.click();
        const items = page.locator(".proto-menu .pm-item");
        await items.first().waitFor();
        assert.equal(await items.count(), allDays.length - inBar, `${width}px: the menu holds the days the bar doesn't`);
        assert.match(await items.last().innerText(), new RegExp(String(allDays.at(-1).requests)));

        // the oldest, picked from the menu: asked for, and the pill named by
        // it and pressed, the bar held where it was as a click on a pill holds
        // it (what the day brings in above it is scrolled past)
        const was = { bar: await page.locator(".rt-days").evaluate((e) => e.getBoundingClientRect().top) };
        await items.last().click();
        await page.locator(".rt-req").first().waitFor();
        await page.waitForTimeout(300);
        assert.deepEqual(asked.slice(-1), [allDays.at(-1).day]);
        pills = await seen(page);
        const pill = pills.find((p) => p.more);
        assert(pill.on, `${width}px: the pill holding the day looked at is pressed`);
        assert.match(pill.text, new RegExp(String(allDays.at(-1).requests)), `${width}px: the pill is named by the day: ${pill.text}`);
        assert(!pills.some((p) => p.cut), `${width}px: picking a day cut a pill off`);
        assert(Math.abs(await page.locator(".rt-days").evaluate((e) => e.getBoundingClientRect().top) - was.bar) <= 1, `${width}px: picking a day moved the bar`);

        // Live, back in the bar: the pill no longer pressed
        await page.locator(".rt-days .rt-day").first().click();
        await page.waitForTimeout(300);
        pills = await seen(page);
        assert(pills[0].on && !pills.find((p) => p.more).on, `${width}px: Live picked again`);
        assert.deepEqual(errors, []);
        await context.close();
      }
    });
  }

  test(`${engine}: a few days fit, and no pill holds the rest`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 1100, height: 760 } })).newPage();
    await page.route("**/*", serve("en", allDays.slice(0, 3), []));
    await page.goto("http://magpie.test/?view=routing");
    await page.locator(".rt-days .rt-day").nth(3).waitFor();
    await page.waitForTimeout(200);
    assert.equal(await page.locator(".rt-days .rt-day-more").count(), 0);
    assert.equal((await seen(page)).length, 4);
  });

  test(`${engine}: the window narrowed, then widened, the bar fits again`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 1600, height: 760 } })).newPage();
    await page.route("**/*", serve("en", allDays.slice(0, 8), []));
    await page.goto("http://magpie.test/?view=routing");
    await page.locator(".rt-days .rt-day").nth(8).waitFor();
    await page.waitForTimeout(200);
    const wide = await seen(page);
    await page.setViewportSize({ width: 520, height: 760 });
    await page.waitForTimeout(300);
    const narrow = await seen(page);
    assert(narrow.at(-1).more && !narrow.some((p) => p.cut), JSON.stringify(narrow));
    await page.setViewportSize({ width: 1600, height: 760 });
    await page.waitForTimeout(300);
    assert.deepEqual(await seen(page), wide);
  });
}
