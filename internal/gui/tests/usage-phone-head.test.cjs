// Run with Node's test runner and Playwright on the module path; see README.md.
// Usage's head on a phone (the owner, 10-31: 手机端这些肥大的按钮总觉得丑丑的).
// Every option of a choice was drawn 44px tall, so the tabs, the days and
// the interval stacked into three fat rows. Now an option is drawn 28px in
// a 32px strip and still taken 8px above and below it; the head is the
// tabs across the width, then one line: the days as a menu, the sum, and
// the refresh as a dot and its figure. The menu follows whichever tab is
// open and picks through that tab's own options. A desktop window keeps its
// strips. Chromium and WebKit, every language, 390 and 320px.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit, devices } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const iso = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;

// the reporter's page: a few Codex rows, a day of hourly points, a year's heatmap
const now = Date.now();
const R = (i, model, out) => ({ route_id: 900 + i, t: new Date(now - (i + 1) * 60e3).toISOString(), agent: "codex", agentName: "Codex", icon: "codex-color",
  provider: "openai", providerName: "openai", host: "chatgpt.com", ep: "/v1/responses", req: model, model, served: model, effort: "medium",
  in: 4000, out, cache_read: 110000, cache_write: 0, ms: 11000, ttft_ms: 6400, status: 200, rid: "r" + i, cost: 0.00175, priced: true });
const ROWS = [R(0, "gpt-6.1-sol", 240), R(1, "gpt-6.1-sol", 145), R(2, "codex-auto-review", 134), R(3, "gpt-6.1-sol", 93)];
const M = (id, calls, out) => ({ id, calls, input: 1, output: out, cache_read: 0, cache_write: 0, cost: 0.003, timed: calls, ttft_ms: 9000, decode_ms: 6000, decode_out: out });
const part = (x) => ({ calls: x.calls, tokens: x.output, cost: x.cost, timed: x.timed, ttft_ms: x.ttft_ms, decode_ms: x.decode_ms, decode_out: x.decode_out });
function HOURS() {
  const m = new Date(); m.setHours(0, 0, 0, 0);
  const upto = new Date().getHours();
  return Array.from({ length: 24 }, (_, h) => {
    const on = h <= upto && h >= 6, k = on ? (h % 5) + 1 : 0;
    return { label: String(h).padStart(2, "0"), time: new Date(m.getTime() + h * 3600e3).toISOString(), calls: k * 3, input: k * 100, output: k * 500, cache_read: k * 90000, cache_write: 0, cost: k * 0.01,
      timed: k, ttft_ms: k * 900, decode_ms: k * 4000, decode_out: k * 400,
      by: { model: on ? { "gpt-6.1-sol": part(M("gpt-6.1-sol", k * 2, k * 300)), "codex-auto-review": part(M("codex-auto-review", k, k * 200)) } : {} } };
  });
}
let reads = 0;
const LEDGER = (period) => ({
  period, rows: ROWS, offset: 0, total: ROWS.length, calls: ROWS.length, errors: 0, input: 2, output: 612, cache_read: 0, cache_write: 0,
  cost: 0.007 + ++reads * 0.001, unpriced: 0, timed: 4, ttft_ms: 18000, decode_ms: 12000, decode_out: 612,
  by: { model: [M("gpt-6.1-sol", 3, 478), M("codex-auto-review", 1, 134)] }, bucket: "hour", series: HOURS(),
  agents: [{ id: "codex", name: "Codex", icon: "codex-color" }],
});
function HEAT() {
  const to = new Date(), from = new Date(); from.setDate(from.getDate() - 364);
  const days = [];
  for (let i = 0; i < 40; i++) { const d = new Date(); d.setDate(d.getDate() - i); days.push({ date: iso(d), calls: 3 + i, tokens: 1000 * (i + 1), cost: 0.01 * i }); }
  return { from: iso(from), to: iso(to), days };
}

function serve(lang, web) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/usage/requests") return json(LEDGER(url.searchParams.get("period")));
    if (url.pathname === "/api/usage/heatmap") return json(HEAT());
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 0, errors: 0, input: 0, output: 0, reasoning: 0, unpriced: 0, cost: 0, bucket: "day", series: [], agents: [], models: [], path: "" });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], models: [], gateway: { running: false } });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}


// where each piece of the head is drawn, and what a finger at a point takes
const HEAD = () => {
  const r = (q) => { const e = document.querySelector(q); if (!e || !e.offsetWidth) return null; const b = e.getBoundingClientRect(); return { top: b.top, bottom: b.bottom, left: b.left, right: b.right, h: b.height }; };
  const opts = [...document.querySelectorAll("#usageTab .opt")].map((o) => {
    const b = o.getBoundingClientRect(), x = b.left + b.width / 2;
    const at = (y) => { const e = document.elementFromPoint(x, y); return !!e && (e === o || o.contains(e)); };
    return { h: b.height, above: at(b.top - 6), below: at(b.bottom + 6) };
  });
  return { tabs: r("#usageTab"), opts, menu: r("#usageRangeMenu"), menuText: document.querySelector("#usageRangeMenu").textContent,
    period: r("#period"), sess: r("#sessRange"), cost: r("#usageCost"), every: r("#usageEvery"), live: document.querySelector("#usageEvery").classList.contains("live"),
    wide: [document.documentElement, document.querySelector(".usage-head")].filter((e) => e.scrollWidth > e.clientWidth + 1).map((e) => `${e.className || e.tagName} ${e.scrollWidth}/${e.clientWidth}`) };
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  const phone = { ...devices["iPhone 13"] };
  if (engine === "chromium") delete phone.defaultBrowserType;
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: Usage's head on a phone is slim tabs over one line`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const width of [390, 320]) {
        const context = await browser.newContext({ ...phone, viewport: { width, height: 760 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.addInitScript(() => { localStorage.setItem("magpie.usageTab", "requests"); localStorage.setItem("magpie.usageEvery", "5"); });
        await page.route("**/*", serve(lang, true));
        await page.goto("http://magpie.test/?view=usage");
        await page.waitForFunction(() => document.querySelector("table.led tbody tr") && document.querySelector("#usageRangeMenu").offsetWidth);
        await page.waitForTimeout(200);
        const h = await page.evaluate(HEAD);
        const at = `${width}px`;
        // the tabs: one strip across the head, slim, each still taken at 44
        assert.ok(h.tabs.h <= 33, `${at}: the tabs' strip is ${h.tabs.h}px`);
        for (const o of h.opts) {
          assert.ok(o.h <= 29, `${at}: a tab is drawn ${o.h}px`);
          assert.ok(o.above && o.below, `${at}: a tab is taken 6px above and below it: ${JSON.stringify(o)}`);
        }
        // the days are a menu on the line under them, with the sum and the refresh
        assert.equal(h.period, null, `${at}: the days' strip is hidden`);
        assert.ok(h.menu && h.menu.top >= h.tabs.bottom, `${at}: the days' menu is under the tabs`);
        assert.equal(h.menuText, { en: "Today", zh: "今天", "zh-TW": "今天", ja: "今日", de: "Heute" }[lang] ?? h.menuText);
        assert.ok(h.cost && h.every, `${at}: the sum and the interval show`);
        const mid = (b) => (b.top + b.bottom) / 2;
        assert.ok(Math.abs(mid(h.menu) - mid(h.cost)) < 6 && Math.abs(mid(h.menu) - mid(h.every)) < 6, `${at}: menu, sum and interval share a line: ${JSON.stringify([h.menu, h.cost, h.every])}`);
        assert.ok(h.menu.right <= h.cost.left && h.cost.right <= h.every.left, `${at}: menu, then sum, then interval`);
        assert.equal(h.live, true, `${at}: reading every 5s shows its dot lit`);
        assert.deepEqual(h.wide, [], `${at}: nothing is wider than the phone`);

        // the filters under it: a picker as tall as the status strip beside
        // it, both on one middle, each taken at 44; the search and Export CSV
        // share the last line
        const f = await page.evaluate(() => {
          const r = (q) => { const e = document.querySelector(q); if (!e || !e.offsetWidth) return null; const b = e.getBoundingClientRect(); return { top: b.top, bottom: b.bottom, left: b.left, right: b.right, h: b.height, mid: b.top + b.height / 2 }; };
          const pick = document.querySelector("#ledgerPane .sess-tools .sess-pick:not([hidden])"), pb = pick?.getBoundingClientRect();
          // a strip wrapped under it takes the gap between them, the nearer reach
          const under = document.querySelector("#ledStatus").getBoundingClientRect().top >= pb.bottom;
          const hit = pb && (under ? [pb.top - 5] : [pb.top - 5, pb.bottom + 5]).every((y) => pick.contains(document.elementFromPoint(pb.left + pb.width / 2, y)));
          return { pick: pick && r("#" + pick.id), hit, strip: r("#ledStatus"), q: r("#ledQ"), exp: r("#ledExport") };
        });
        assert.ok(f.pick, `${at}: a picker shows`);
        assert.ok(f.pick.h <= 33 && Math.abs(f.pick.h - f.strip.h) <= 1, `${at}: the picker is as tall as the strip: ${f.pick.h} / ${f.strip.h}`);
        // beside it, or, where its words are too long (de, ja), under it at its left
        if (f.strip.top < f.pick.bottom) assert.ok(Math.abs(f.pick.mid - f.strip.mid) <= 1, `${at}: the picker and the strip share a middle: ${JSON.stringify([f.pick, f.strip])}`);
        else assert.ok(Math.abs(f.strip.left - f.pick.left) <= 1, `${at}: a strip under the picker starts at its left`);
        assert.ok(f.hit, `${at}: the picker is taken 5px above and below it`);
        assert.ok(Math.abs(f.q.mid - f.exp.mid) <= 2 && f.q.right <= f.exp.left, `${at}: the search, then Export CSV, on one line: ${JSON.stringify([f.q, f.exp])}`);

        // the menu picks through the strip's own options
        await page.locator("#usageRangeMenu").click();
        const items = page.locator(".proto-menu .pm-item");
        assert.ok(await items.count() >= 4, "every period is in the menu");
        await items.nth(2).click();
        await page.waitForFunction(() => document.querySelectorAll("#period .opt")[2].classList.contains("on"));
        assert.equal(await page.locator(".proto-menu").count(), 0, "a pick shuts the menu");
        const seven = await page.locator("#period .opt").nth(2).textContent();
        await page.waitForFunction((s) => document.querySelector("#usageRangeMenu").textContent.trim() === s.trim(), seven);

        // Sessions has its own range: the menu follows the tab
        await page.locator("#usageTab .opt").nth(2).click();
        await page.waitForFunction(() => !document.querySelector("#sessRange").hidden);
        const sess = await page.evaluate(() => document.querySelector("#sessRange .opt.on")?.textContent.trim());
        await page.waitForFunction((s) => document.querySelector("#usageRangeMenu").textContent.trim() === s, sess);
        await page.locator("#usageRangeMenu").click();
        assert.equal(await page.locator(".proto-menu .pm-item").count(), await page.locator("#sessRange .opt").count(), "Sessions' ranges are the menu's");
        await page.keyboard.press("Escape");
        assert.deepEqual(errors, []);
        await context.close();
      }
    });
  }

  test(`${engine}: a desktop window keeps the days' strip`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 1100, height: 800 } })).newPage();
    await page.addInitScript(() => localStorage.setItem("magpie.usageTab", "requests"));
    await page.route("**/*", serve("en", false));
    await page.goto("http://magpie.test/?view=usage");
    await page.waitForFunction(() => document.querySelector("#period .opt"));
    const h = await page.evaluate(HEAD);
    assert.ok(h.period, "the strip shows");
    assert.equal(h.menu, null, "no menu in its place");
    assert.ok(h.opts.every((o) => o.h <= 23), "the tabs' size is a desktop's");
  });
}
