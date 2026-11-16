// Run with Node's test runner and Playwright on the module path; see README.md.
// The tray panel's heatmap (eiyuuzaki2333 on X: a heatmap like GitHub's,
// "also in the tray form"): the Usage tab ends in the recent weeks of
// /api/usage/heatmap, as many as the panel's width has room for, Monday on
// top, today the last day, coloured by the tab's own Tokens / Cost /
// Requests switch. It is of the provider picked; the pointer on a day says
// its date and what it had; weeks without a call show no card. Every
// language, a 440px and a 320px panel, light and dark; no backend, the API
// is faked. MAGPIE_ASSETS runs it on other assets.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets");
const iso = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
const today = new Date();
today.setHours(0, 0, 0, 0);
const from = new Date(today);
from.setDate(from.getDate() - ((today.getDay() + 6) % 7) - 7 * 52);
const ago = (n) => iso(new Date(today.getFullYear(), today.getMonth(), today.getDate() - n));
// the last 60 days but every fifth: the busiest by tokens is today, by
// requests three days ago
const DAYS = [];
for (let n = 0; n < 60; n++) {
  if (n % 5 === 4) continue;
  const calls = n === 3 ? 900 : (n % 7) + 1;
  DAYS.push({ date: ago(n), calls, tokens: n === 0 ? 9e9 : n === 3 ? 1e4 : calls * 1e5, cost: calls * 0.5 });
}
const heatmap = (q, empty) => ({ from: iso(from), to: iso(today), days: empty ? [] : q.get("provider") === "openai" ? [{ date: ago(1), calls: 2, tokens: 3000, cost: 0.2 }] : DAYS });

const midnight = new Date(today);
const share = (id, name) => ({ id, name, icon: "generic", calls: 2, errors: 0, input: 1e5, output: 5e4, cache_read: 0, cache_write: 0, cost: 1 });
function ledger(q) {
  const series = Array.from({ length: 24 }, (_, i) => ({ label: String(i), time: new Date(midnight.getTime() + i * 3600e3).toISOString(), calls: i === 10 ? 4 : 0, errors: 0, input: i === 10 ? 1000 : 0, output: 0, cache_read: 0, cache_write: 0, cost: 0, by: { provider: {}, model: {}, modelAt: {} } }));
  return {
    period: q.get("period") || "today", rows: [], offset: 0, total: 1, calls: 4, errors: 0, input: 2e5, output: 1e5, cache_read: 0, cache_write: 0, reasoning: 0, cost: 2, unpriced: 0,
    ...(q.get("provider") ? { provider: q.get("provider") } : {}),
    bucket: "hour", series, by: { provider: [share("anthropic", "Claude"), share("openai", "OpenAI")], agent: [], model: [share("m", "m")], modelAt: [] },
    agents: [], providers: [{ id: "anthropic", name: "Claude", icon: "generic" }, { id: "openai", name: "OpenAI", icon: "generic" }],
  };
}

function serve(lang, theme, heats, empty) {
  const settings = { lang, theme };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/usage/requests") return json(ledger(url.searchParams));
    if (url.pathname === "/api/usage/heatmap") { heats.push(url.searchParams); return json(heatmap(url.searchParams, empty)); }
    if (url.pathname === "/api/gateway/trace") return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 0, errors: 0, input: 0, output: 0, reasoning: 0, unpriced: 0, cost: 0, bucket: "day", series: [], agents: [], models: [], path: "" });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], models: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname.startsWith("/api/")) return route.fulfill(url.pathname.startsWith("/api/window/") ? { status: 204 } : { json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const BROWSERS = process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"];
const HEAT = "#panelUsage .pu-heat";
// the class of the cell of a day
const level = (p, date) => p.locator(`${HEAT} i[data-date="${date}"]`).getAttribute("class");

async function open(browser, lang, theme, width, empty) {
  const ctx = await browser.newContext({ viewport: { width, height: 1100 }, reducedMotion: "reduce", colorScheme: theme });
  const errors = [], heats = [];
  const p = await ctx.newPage();
  p.setDefaultTimeout(5000);
  p.setDefaultNavigationTimeout(60000);
  p.on("pageerror", (e) => errors.push(e.message));
  await p.addInitScript(() => { try { localStorage.setItem("magpie.panelTab", "stats"); localStorage.setItem("magpie.panelUseMetric", "tokens"); localStorage.setItem("magpie.panelUseSplit", "provider"); } catch {} });
  await p.route("**/*", serve(lang, theme, heats, empty));
  await p.goto("http://magpie.test/?mode=panel");
  await p.locator("#panelUsage .pu-card .led-rank .rk").first().waitFor();
  return { ctx, p, errors, heats };
}

for (const engine of BROWSERS) {
  for (const lang of LANGS) {
    test(`${engine} ${lang}: the panel's Usage ends in the recent weeks' heatmap`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const [width, theme] of [[440, "light"], [320, "dark"]]) {
        const { ctx, p, errors, heats } = await open(browser, lang, theme, width);
        const at = `${width}px ${theme}`;
        await p.locator(HEAT + " .heat-grid i").first().waitFor();
        // the last card of the tab, after the chart's
        assert.equal(await p.locator("#panelUsage > :last-child").evaluate((e) => e.classList.contains("pu-heat")), true, at);
        // a card of its own kind: the chart's stays the tab's one .pu-card
        assert.equal(await p.locator("#panelUsage .pu-card").count(), 1, at);
        // whole weeks from a Monday up to today, as many as the width has room for
        const dates = await p.locator(HEAT + " .heat-grid i").evaluateAll((cs) => cs.map((c) => c.dataset.date));
        assert.equal(dates.at(-1), iso(today), at);
        const weeks = Math.ceil(dates.length / 7);
        assert.equal(new Date(dates[0] + "T00:00").getDay(), 1, "the first day a Monday");
        assert(weeks >= 8 && weeks <= 53, `${weeks} weeks at ${at}`);
        if (width === 440) assert(weeks >= 20, `${weeks} weeks fill the 440px panel`);
        // every cell inside the card, a square of at least 9px, nothing scrolled sideways
        const fit = await p.locator(HEAT).evaluate((card) => {
          const c = card.getBoundingClientRect();
          const cells = [...card.querySelectorAll(".heat-grid i")].map((i) => i.getBoundingClientRect());
          return { out: cells.filter((r) => r.right > c.right + 0.5 || r.left < c.left - 0.5).length, small: cells.filter((r) => r.width < 9 || Math.abs(r.width - r.height) > 1).length, mo: card.querySelectorAll(".mo").length };
        });
        assert.deepEqual(fit, { out: 0, small: 0, mo: fit.mo }, at);
        assert(fit.mo >= 1, "months named over the weeks");
        assert(await p.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), `no sideways scroll at ${at}`);
        // the five shades apart, in the accent's ramp
        const shades = await p.locator(HEAT + " .sess-legend i").evaluateAll((is) => new Set(is.map((i) => getComputedStyle(i).backgroundColor)).size);
        assert.equal(shades, 5, at);
        // a quiet day shows on the card, in the light panel too, whose pills are its white
        const quiet = await p.locator(HEAT).evaluate((card) => [getComputedStyle(card).backgroundColor, getComputedStyle(card.querySelector(".sess-legend i.l0")).backgroundColor]);
        assert.notEqual(quiet[1], quiet[0], `a quiet day apart from the card at ${at}`);

        // by tokens, today is the busiest; by requests, three days ago
        assert.equal(await level(p, ago(0)), "l4");
        assert.notEqual(await level(p, ago(3)), "l4");
        assert.equal(await level(p, ago(4)), "l0", "a quiet day");
        const asked = heats.length;
        await p.locator("#panelUsage .pu-card .led-bar .segs:not(.pu-split) .opt").nth(2).click();
        await p.waitForFunction((d) => document.querySelector(`#panelUsage .pu-heat i[data-date="${d}"]`)?.className === "l4", ago(3));
        assert.equal(heats.length, asked, "a switch reads nothing again");

        // the pointer on a day: its date and what it had, the requests first
        const cell = p.locator(`${HEAT} i[data-date="${ago(3)}"]`);
        const b = await cell.boundingBox();
        await p.mouse.move(b.x + b.width / 2, b.y + b.height / 2, { steps: 2 });
        const tip = p.locator(HEAT + " .heat-tip");
        await tip.waitFor();
        const text = await tip.textContent();
        assert.match(text, /900/, at);
        assert.match(text, new RegExp(String(new Date(today.getFullYear(), today.getMonth(), today.getDate() - 3).getDate())));
        const inside = await tip.evaluate((t) => { const r = t.getBoundingClientRect(); return r.left >= 0 && r.right <= innerWidth; });
        assert(inside, `the tip inside the panel at ${at}`);
        await p.mouse.move(2, 2);
        await tip.waitFor({ state: "hidden" });

        // one provider picked: its days only
        await p.locator("#panelUsage .led-rank .rk", { hasText: "OpenAI" }).click();
        await p.waitForFunction((d) => document.querySelector(`#panelUsage .pu-heat i[data-date="${d}"]`)?.className === "l4" && !document.querySelector(`#panelUsage .pu-heat .heat-grid :is(i.l1, i.l2, i.l3)`), ago(1));
        assert.equal(heats.at(-1).get("provider"), "openai");
        assert.deepEqual(errors, []);
        await ctx.close();
      }
    });
  }

  test(`${engine}: weeks without a call show no heatmap`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const { ctx, p, errors, heats } = await open(browser, "en", "light", 440, true);
    for (let i = 0; i < 50 && !heats.length; i++) await p.waitForTimeout(100);
    await p.waitForTimeout(300); // its answer drawn, or not
    assert(heats.length >= 1, "the heatmap was read");
    assert.equal(await p.locator(HEAT + ":visible").count(), 0);
    assert.deepEqual(errors, []);
    await ctx.close();
  });
}
