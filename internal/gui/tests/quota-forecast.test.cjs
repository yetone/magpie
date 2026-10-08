// Run with Node's test runner and Playwright on the module path; see README.md.
// The forecast on the allowance cards (the "forecast first" design): each
// window says what its forecast expects before its meter and its burn-down —
// "Lasts to reset" with the multiple it leaves, "Runs out in …" with the
// time (one sentence whichever layer answered; the history marker beside it
// says which), "Used up", or that too little has been read to say. The
// burned-down line is drawn with the soft fill under it and a dotted projection to where it runs out (or to the reset
// it lasts to), the now dot and the marker the axis labels; the projection is
// there only where the backend sent a forecast to project. One legend now
// stands in the allowances' head rather than on every card. The tray panel's
// card carries the same verdict, one compact line a window. The backend's
// numbers are the only input: it sends no display strings. A window with no
// forecast, state none, or no readings has no verdict in the tray or page. English and
// Chinese, light and dark, Chromium at least; the API is faked here, with a
// complete /api/usage body so the page's own errors stay visible to this test.
// Every plot's content is measured against its viewBox: the geometry insets
// the box by the room the now dot and the run-out / reset marker need.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const H = 3600e3;
const iso = (ms) => new Date(ms).toISOString();
// a cycle's readings, from a to b percent left, its start and its reset
const cycle = (start, end, from, to) => [0, 1, 2, 3, 4].map((i) => ({
  at: iso(start + ((end - start) * i) / 4), left: from + ((to - from) * i) / 4,
  start: iso(start), resetsAt: iso(end),
}));

// the plot's floor: the geometry insets the box by QUOTA_PAD (4) so the
// markers drawn on the line's ends are not cut by the plot's edge
const FLOOR = 118 - 4;

// a complete /api/usage body, as /api/usage answers it (the catch-all's {}
// made fmtCost throw, a red toast the assertions here could not see)
const series = ["Oct 1", "Oct 2", "Oct 3", "Oct 4", "Oct 5", "Oct 6"].map((label, i) => ({
  label, calls: 180 + i * 30, errors: i % 3, input: 500000 + i * 40000, output: 90000 + i * 8000,
  cache_read: 1400000 + i * 90000, cache_write: 60000 + i * 5000, reasoning: 14000 + i * 900, cost: 2.1 + i * 0.4, unpriced: 2,
}));
const usageBody = {
  period: "month", since: "2026-09-07T00:00:00.000Z",
  calls: 1284, errors: 6, input: 4100000, output: 812000, cache_read: 12400000, cache_write: 640000,
  reasoning: 120000, cost: 18.42, unpriced: 12, timed: 900, ttft_ms: 1200000, decode_ms: 3600000,
  decode_out: 700000, bucket: "day", series, agents: [], models: [], accounts: [], callerKeys: [], path: "~/.config/magpie",
};

function fixtures(now) {
  const r5 = now + 2 * H, rw = now + 3 * 24 * H, rm = now + 20 * 24 * H;
  const quotas = [
    { provider: "claude", name: "Claude Code", icon: "claude-color", plan: "Max 5x", user: "ada@example.com", windows: [
      { name: "5 hours", used: 30, resetsAt: iso(r5) },
      { name: "Weekly", used: 60, resetsAt: iso(rw) },
      { name: "Monthly", used: 40, resetsAt: iso(rm) },
      { name: "Daily", used: 10, resetsAt: iso(r5) },
    ] },
    { provider: "gemini", name: "Gemini CLI", icon: "gemini-color", user: "bob@example.com", windows: [
      { name: "Weekly", used: 100, resetsAt: iso(rw) },
      { name: "5 hours", used: 20, resetsAt: iso(r5) },
      // no readings of this one at all: it keeps its meter alone
      { name: "Monthly", used: 5, resetsAt: iso(rm) },
    ] },
  ];
  const history = [
    { provider: "claude", user: "ada@example.com", lines: [
      { name: "5 hours", points: cycle(r5 - 5 * H, now, 100, 70), forecast: {
        state: "ok", source: "even", left: 70, evenLeft: 58, ahead: 12, ratePerHour: 6,
        lastsToReset: true, etaSeconds: 0, leftAtReset: 20, headroom: 1.3, cycles: 0,
        cycleStart: iso(r5 - 5 * H), resetsAt: iso(r5) } },
      { name: "Weekly", points: cycle(rw - 7 * 24 * H, now, 100, 40), forecast: {
        state: "ok", source: "even", left: 40, evenLeft: 48, ahead: -8, ratePerHour: 2,
        lastsToReset: false, etaSeconds: 2.5 * H / 1000, leftAtReset: -10, headroom: 0.9, cycles: 0,
        cycleStart: iso(rw - 7 * 24 * H), resetsAt: iso(rw) } },
      { name: "Monthly", points: cycle(rm - 30 * 24 * H, now, 100, 60), forecast: {
        state: "ok", source: "history", left: 60, evenLeft: 59.8, ahead: 0.2, ratePerHour: 1,
        lastsToReset: false, etaSeconds: 34 * H / 1000, leftAtReset: -5, headroom: 0.95, cycles: 3,
        cycleStart: iso(rm - 30 * 24 * H), resetsAt: iso(rm) } },
      // magpie has read this window but the backend says nothing yet: no forecast
      { name: "Daily", points: cycle(r5 - 5 * H, r5, 100, 90).filter((p) => Date.parse(p.at) <= now) },
    ] },
    { provider: "gemini", user: "bob@example.com", lines: [
      { name: "Weekly", points: cycle(rw - 7 * 24 * H, now, 100, 0), forecast: {
        state: "spent", source: "even", left: 0, evenLeft: 0, ahead: 0, ratePerHour: 5,
        lastsToReset: false, etaSeconds: 0, leftAtReset: -40, headroom: 0, cycles: 0,
        cycleStart: iso(rw - 7 * 24 * H), resetsAt: iso(rw) } },
      { name: "5 hours", points: cycle(r5 - 5 * H, now, 100, 80), forecast: {
        state: "ok", source: "history", left: 80, evenLeft: 55, ahead: 25, ratePerHour: 1,
        lastsToReset: true, etaSeconds: 0, leftAtReset: 60, headroom: 1.4, cycles: 3,
        cycleStart: iso(r5 - 5 * H), resetsAt: iso(r5) } },
    ] },
  ];
  return { quotas, history };
}

function serve(lang, theme, panel, data) {
  const settings = { theme, lang, tray: "panel", quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (body) => route.fulfill({ json: body });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:${!panel}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json(data.quotas);
    if (url.pathname === "/api/usage/quotas/history") return json(data.history);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage") return json(usageBody);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: {
    five: "5 hours", week: "Weekly", monthly: "Monthly", daily: "Daily",
    lasts: "Lasts to reset · 1.3×", lastsHist: "Lasts to reset · 1.4×",
    runout: "Runs out in 2h 30m",
    histRunout: "Runs out in 1d 10h",
    spent: "Used up",
    ahead: "12% ahead", behind: "8% behind", even: "On pace",
    hist: "history", legend: ["Actual left", "Even burn", "Projected at this rate"],
    rest: "Left at reset: 20%", zero: "Runs out",
  },
  zh: {
    five: "5 小时", week: "每周", monthly: "每月", daily: "每日",
    lasts: "撑得到重置 · 1.3× 余量", lastsHist: "撑得到重置 · 1.4× 余量",
    runout: "2 小时 30 分后用完",
    histRunout: "1 天 10 小时后用完",
    spent: "已用完",
    ahead: "领先 12%", behind: "落后 8%", even: "匀速",
    hist: "历史", legend: ["实际剩余", "匀速消耗", "按当前速度预测"],
    rest: "重置时剩 20%", zero: "预计用完",
  },
  ja: {
    five: "5 時間", week: "週", monthly: "毎月", daily: "毎日",
    lasts: "リセットまで持つ · 1.3× 余裕", lastsHist: "リセットまで持つ · 1.4× 余裕",
    runout: "2 時間 30 分後に使い切り", histRunout: "1 日 10 時間後に使い切り",
    spent: "使い切り", ahead: "12% 余裕", behind: "8% 超過", even: "均等なペース",
    hist: "履歴", legend: ["実際の残量", "均等な消費", "現在のペースで予測"],
    rest: "リセット時の残量：20%", zero: "使い切り予測",
  },
  de: {
    five: "5 Stunden", week: "Wöchentlich", monthly: "Monatlich", daily: "Täglich",
    lasts: "Reicht bis zum Reset · 1.3× Reserve", lastsHist: "Reicht bis zum Reset · 1.4× Reserve",
    runout: "Leer in 2 Std. 30 Min.", histRunout: "Leer in 1 T. 10 Std.",
    spent: "Aufgebraucht", ahead: "12% voraus", behind: "8% zurück", even: "Im Plan",
    hist: "Verlauf", legend: ["Tatsächlicher Rest", "Gleichmäßiger Verbrauch", "Prognose bei diesem Tempo"],
    rest: "Rest beim Reset: 20%", zero: "Voraussichtlich leer",
  },
};

// the last y of a path's d, on the plots' 118-unit box, whose floor the
// geometry insets by QUOTA_PAD: where the projection ends says whether it
// runs out or lasts to the reset
const endY = (d) => parseFloat(d.trim().split(/[Ll]/).pop().trim().split(/\s+/)[1]);

// boxOf: a plot's content as its own user units, for the check that nothing
// (the now dot, the run-out / reset marker) is drawn past the viewBox
const boxOf = (loc) => loc.evaluate((svg) => {
  const vb = svg.getAttribute("viewBox").split(" ").map(Number);
  let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
  for (const c of svg.querySelectorAll("path, line, circle")) {
    const r = c.getBBox();
    minX = Math.min(minX, r.x); minY = Math.min(minY, r.y);
    maxX = Math.max(maxX, r.x + r.width); maxY = Math.max(maxY, r.y + r.height);
  }
  const round = (n) => Math.round(n * 10) / 10;
  return { box: [vb[2], vb[3]], content: [round(minX), round(minY), round(maxX), round(maxY)] };
});
const fits = ({ box, content }) => content[0] >= -0.5 && content[1] >= -0.5 && content[2] <= box[0] + 0.5 && content[3] <= box[1] + 0.5;

// a run-out the backend gave no usable time for says only that it runs out,
// without trailing off into an empty "in  left", and a window resetting in
// minutes shows its headroom capped rather than an absurd multiple
const runouts = {
  ja: { runout: "使い切り予測", capped: "リセットまで持つ · 10×+ 余裕 履歴" },
  de: { runout: "Voraussichtlich leer", capped: "Reicht bis zum Reset · 10×+ Reserve Verlauf" },
  en: { runout: "Runs out", capped: "Lasts to reset · 10×+ history" },
  zh: { runout: "预计用完", capped: "撑得到重置 · 10×+ 余量 历史" },
};
function noTimes(now) {
  const r5 = now + 2 * H;
  const quotas = [{ provider: "claude", name: "Claude Code", icon: "claude-color", plan: "Max 5x", user: "ada@example.com", windows: [
    { name: "5 hours", used: 96, resetsAt: iso(r5) },
    { name: "Daily", used: 1, resetsAt: iso(now + 5 * 60e3) },
  ] }];
  const history = [{ provider: "claude", user: "ada@example.com", lines: [
    { name: "5 hours", points: cycle(r5 - 5 * H, now, 100, 4), forecast: {
      state: "ok", source: "even", left: 4, evenLeft: 30, ahead: -26, ratePerHour: 20, lastsToReset: false,
      etaSeconds: 0, leftAtReset: -30, headroom: 0.1, cycles: 0, cycleStart: iso(r5 - 5 * H), resetsAt: iso(r5) } },
    { name: "Daily", points: cycle(now - 24 * H, now, 100, 99.9), forecast: {
      state: "ok", source: "history", left: 99.9, evenLeft: 0.4, ahead: 99, ratePerHour: 0.1, lastsToReset: true,
      etaSeconds: 0, leftAtReset: 99, headroom: 240.5, cycles: 0, cycleStart: iso(now - 24 * H), resetsAt: iso(now + 5 * 60e3) } },
  ] }];
  return { quotas, history };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    const w = runouts[lang];
    test(`${engine} ${lang}: a run-out with no time to say, and a headroom past ten`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1180, height: 900 }, reducedMotion: "reduce" })).newPage();
      const errors = [];
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      page.on("console", (m) => { if (m.type() === "error") errors.push("console: " + m.text()); });
      await page.route("**/*", serve(lang, "dark", false, noTimes(Date.now())));
      await page.goto("http://magpie.test/?view=usage");
      const card = page.locator(".subscription-card", { hasText: "Claude Code" });
      await card.locator(".quota-plot").first().waitFor();
      assert.deepEqual(await card.locator(".qv-text").allTextContents(), [w.runout, w.capped]);
      assert.deepEqual(await card.locator(".qv-text").evaluateAll((es) => es.map((e) => e.className)), ["qv-text k-warn", "qv-text k-safe"]);
      assert.equal(await page.locator(".status.err").count(), 0);
      assert.deepEqual(errors, []);
    });
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    const w = words[lang];
    test(`${engine} ${lang}: the allowance cards say what the forecast expects`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      const pages = [];
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          for (const [name, p] of pages) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-quota-forecast-${name}.png`) });
        }
        await browser.close();
      });
      const errors = [];
      const data = fixtures(Date.now());
      const open = async (name, url, theme, viewport) => {
        const page = await (await browser.newContext({ viewport, reducedMotion: "reduce" })).newPage();
        pages.push([name, page]);
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        page.on("console", (m) => { if (m.type() === "error") errors.push("console: " + m.text()); });
        await page.route("**/*", serve(lang, theme, url.includes("mode=panel"), data));
        await page.goto(url);
        return page;
      };

      for (const theme of ["light", "dark"]) {
        const page = await open(theme, "http://magpie.test/?view=usage", theme, { width: 1180, height: 900 });
        const card = page.locator(".subscription-card", { hasText: "Claude Code" });
        await card.locator(".quota-plot").first().waitFor();
        const gem = page.locator(".subscription-card", { hasText: "Gemini CLI" });
        await gem.locator(".quota-plot").first().waitFor();

        // one header a window: its name, the verdict, the delta at the right
        assert.deepEqual(await card.locator(".qv-badge").allTextContents(), [w.five, w.week, w.monthly, w.daily]);
        assert.deepEqual(await card.locator(".qv-text").allTextContents(),
          [w.lasts, w.runout, w.histRunout + " " + w.hist]);
        assert.deepEqual(await card.locator(".qv-text").evaluateAll((es) => es.map((e) => e.className)),
          ["qv-text k-safe", "qv-text k-warn", "qv-text k-warn"]);
        assert.deepEqual(await gem.locator(".qv-text").allTextContents(),
          [w.spent, w.lastsHist + " " + w.hist]);
        assert.deepEqual(await gem.locator(".qv-text").evaluateAll((es) => es.map((e) => e.className)),
          ["qv-text k-risk", "qv-text k-safe"]);
        // the history layer is marked, subtly, where it answered
        assert.equal(await card.locator(".qv-src").count(), 1);
        assert.equal(await gem.locator(".qv-src").count(), 1);

        // the delta from an even burn, at each header's right; none for a
        // window with nothing to compare
        assert.deepEqual(await card.locator(".qv-delta").allTextContents(), [w.ahead, w.behind, w.even]);
        assert.deepEqual(await card.locator(".qv-delta").evaluateAll((es) => es.map((e) => e.className)),
          ["qv-delta k-safe", "qv-delta k-warn", "qv-delta k-thin"]);
        assert.deepEqual(await gem.locator(".qv-delta").allTextContents(), [w.ahead.replace("12", "25")]);
        // a window of the same card with no readings keeps its meter alone
        assert.equal(await gem.locator(".quota").count(), 3);
        assert.equal(await gem.locator(".quota.has-plot").count(), 2);
        assert.equal(await gem.locator(".quota:not(.has-plot) .qv-row, .quota:not(.has-plot) .quota-plot").count(), 0);
        assert.equal(await gem.locator(".quota:not(.has-plot) .quota-labels").count(), 1);

        // a burn-down a window, under its header and above its meter: the
        // soft fill, the grid, the line, the dot for now
        const plots = card.locator(".quota-plot");
        assert.equal(await plots.count(), 4);
        assert.equal(await card.locator("path.qc-area").count(), 4);
        assert.equal(await card.locator("circle.qc-dot").count(), 4);
        assert.equal(await card.locator(".qv-row + .quota-plot").count(), 4, "the header is above its plot");
        const meter = plots.first().locator("xpath=following-sibling::div[contains(@class,'quota-labels')]");
        assert.equal(await meter.count(), 1, "the meter is under its plot");

        // the projection is drawn only where the backend sent a forecast to
        // project: to the floor where it runs out, to the edge where it lasts;
        // a window it said nothing of has none
        const proj = card.locator("path.qc-proj");
        assert.equal(await proj.count(), 3);
        assert.ok(endY(await plots.nth(0).locator("path.qc-proj").getAttribute("d")) < FLOOR, "lasts: dotted to the reset edge, above the floor");
        assert.equal(endY(await plots.nth(1).locator("path.qc-proj").getAttribute("d")), FLOOR, "runs out: dotted to the floor");
        assert.equal(endY(await plots.nth(2).locator("path.qc-proj").getAttribute("d")), FLOOR, "the history layer's run-out too");
        // nothing is drawn outside the box: the now dot (r 3) and the
        // run-out / reset marker (r 2.4) sit on the line's ends, and the
        // geometry keeps the room they need
        for (let i = 0; i < 4; i++) {
          const m = await boxOf(plots.nth(i).locator("svg"));
          assert.ok(fits(m), "plot " + i + " draws at " + m.content + " in a " + m.box + " box");
        }
        assert.equal(await plots.nth(3).locator("path.qc-proj").count(), 0, "no forecast, no projection");
        assert.equal(await plots.nth(3).locator("circle.qc-zero").count(), 0);
        assert.equal(await gem.locator("path.qc-proj").count(), 1, "the spent window projects nothing");

        // the axis marks the cycle's start and reset, now, and where it runs
        // out (or what it leaves at the reset)
        assert.equal(await plots.nth(0).locator(".qc-axis .axrest").textContent(), w.rest);
        assert.equal(await plots.nth(1).locator(".qc-axis .axzero").textContent(), w.zero);
        assert.equal(await plots.nth(3).locator(".qc-axis .axzero, .qc-axis .axrest").count(), 0);

        // one legend, in the allowances' head, no legend on a card
        const legend = page.locator("#quotaHead #quotaLegend");
        assert.equal(await legend.count(), 1);
        assert.equal(await legend.locator(".qc-key").count(), 3);
        assert.deepEqual(await legend.locator(".qc-key").allTextContents(), w.legend);
        assert.equal(await page.locator(".quota-windows .qc-legend, .quota-curve").count(), 0, "the cards carry no legend of their own");
        // the page's own error banner: a fixture with every field the real
        // /api/usage sends leaves it empty
        assert.equal((await page.locator(".status.err").allTextContents()).join(""), "");

        const strokes = await plots.nth(0).locator("path.qc-line").evaluate((e) => getComputedStyle(e).stroke);
        const c1 = await page.evaluate(() => { const s = document.createElement("i"); s.style.color = "var(--c1)"; document.body.append(s); const c = getComputedStyle(s).color; s.remove(); return c; });
        assert.equal(strokes, c1);

        if (theme === "light") {
          // the range pick turns every card's plots at once, and Off takes
          // them (and the legend) away while the verdicts stay
          const pick = page.locator("#quotaHead #quotaTrend");
          const choose = async (name) => {
            await pick.click();
            await page.locator(".sess-menu .pm-item").filter({ hasText: name }).click();
          };
          const before = await plots.first().locator("path.qc-line").getAttribute("d");
          await choose(({ en: "2 days", zh: "2 天", ja: "2 日", de: "2 Tage" })[lang]);
          assert.notEqual(await plots.first().locator("path.qc-line").getAttribute("d"), before, "the range redrew the plots");
          assert.equal(await page.evaluate(() => localStorage.getItem("magpie.quotaRange")), "2d");
          await choose(({ en: "Off", zh: "关闭", ja: "オフ", de: "Aus" })[lang]);
          assert.equal(await page.locator(".quota-plot").count(), 0);
          assert.equal(await legend.isVisible(), false, "no curves, no legend");
          assert.equal(await card.locator(".qv-row").count(), 4, "the verdicts stay");
          await choose(({ en: "Cycle", zh: "本周期", ja: "サイクル", de: "Zyklus" })[lang]);
          await card.locator(".quota-plot").first().waitFor();
          assert.equal(await legend.isVisible(), true);
        }
      }

      // the tray panel: the same verdict, one compact line a window, no chart.
      // The tray is narrow and scrolls, so the verdict wraps; measure the
      // rendered box in both themes, in every language, without scrolling a
      // card into view.
      for (const ptheme of ["dark", "light"]) {
        const panel = await open("panel-" + ptheme, "http://magpie.test/?mode=panel", ptheme, { width: 440, height: 700 });
        await panel.locator('#ptabs [data-ptab="usage"]').click();
        const pcard = panel.locator(".pq-card", { hasText: "ada@example.com" });
        await pcard.locator(".pq-verdict").first().waitFor();
        assert.deepEqual(await pcard.locator(".pq-verdict > b").allTextContents(), [w.five, w.week, w.monthly]);
        assert.deepEqual(await pcard.locator(".pq-verdict > span").allTextContents(),
          [w.lasts, w.runout, w.histRunout + " " + w.hist]);
        assert.deepEqual(await pcard.locator(".pq-verdict").evaluateAll((es) => es.map((e) => e.className)),
          ["pq-verdict k-safe", "pq-verdict k-warn", "pq-verdict k-warn"]);
        const bcard = panel.locator(".pq-card", { hasText: "bob@example.com" });
        assert.deepEqual(await bcard.locator(".pq-verdict > span").allTextContents(),
          [w.spent, w.lastsHist + " " + w.hist], "no line for the window it has no readings of");
        assert.deepEqual(await bcard.locator(".pq-verdict > b").allTextContents(), [w.week, w.five]);
        assert.equal(await panel.locator(".pq-card .quota-plot").count(), 0, "no chart in the tray");
        const spark = await boxOf(pcard.locator("svg.pq-spark"));
        assert.ok(fits(spark), "the tray's spark draws at " + spark.content + " in a " + spark.box + " box");
        // The longest line carries the multiplier and the history marker at
        // once; at 440px German's is wider than the row, so it must wrap
        // rather than be cut off (regression: it was ellipsized). Each
        // verdict's label and text stay inside their card, and no line
        // overflows its own box in either direction.
        const overflow = await panel.locator(".pq-card").evaluateAll((cards) => {
          const out = [];
          for (const card of cards) {
            const cb = card.getBoundingClientRect();
            for (const v of card.querySelectorAll(".pq-verdict")) {
              const vb = v.getBoundingClientRect();
              const label = v.querySelector(":scope > b");
              const text = v.querySelector(":scope > span");
              const tb = text && text.getBoundingClientRect();
              const lb = label && label.getBoundingClientRect();
              if (text && (text.scrollWidth > text.clientWidth + 1 || text.scrollHeight > text.clientHeight + 1)) out.push("clipped: " + text.textContent);
              if (vb.left < cb.left - 1 || vb.right > cb.right + 1) out.push("verdict past card edge: " + v.textContent);
              if (tb && (tb.left < cb.left - 1 || tb.right > cb.right + 1)) out.push("text past card edge: " + text.textContent);
              if (lb && (lb.left < cb.left - 1 || lb.right > cb.right + 1)) out.push("label past card edge: " + label.textContent);
            }
          }
          return out;
        });
        assert.deepEqual(overflow, [], "no tray verdict is clipped or leaves its card");
      }
      assert.deepEqual(errors, []);
    });
  }
}

// A reading 90 minutes old keeps its fixed crossing, even after that
// projection passed. The dot marks when the vendor was read, not now; no
// unobserved depletion is presented as an actual Used up state.
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: old readings keep their own time and a fixed projection`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    const page = await browser.newPage({ viewport: { width: 1180, height: 900 }, reducedMotion: "reduce" });
    const now = Date.now(), readAt = now - 90 * 60000, runsOutAt = now - 10 * 60000;
    const data = fixtures(now);
    const line = data.history[0].lines[0];
    const start = now - 4 * H, reset = now + H;
    line.points = cycle(start, readAt, 100, 40).map((p) => ({ ...p, start: iso(start), resetsAt: iso(reset) }));
    line.forecast = { ...line.forecast, left: 40, lastsToReset: false, etaSeconds: 0, asOf: iso(readAt), runsOutAt: iso(runsOutAt), cycleStart: iso(start), resetsAt: iso(reset) };
    await page.route("**/*", serve("en", "light", false, data));
    await page.goto("http://magpie.test/?view=usage");
    const plot = page.locator(".subscription-card", { hasText: "ada@example.com" }).locator(".quota-plot").first();
    await plot.waitFor();
    const geometry = await plot.locator("svg").evaluate((g) => {
      const dot = g.querySelector(".qc-dot"), now = g.querySelector(".qc-now"), projection = g.querySelector(".qc-proj"), zero = g.querySelector(".qc-zero");
      return { width: g.viewBox.baseVal.width, dot: +dot.getAttribute("cx"), now: +now.getAttribute("x1"), zero: +zero.getAttribute("cx"), projection: projection.getAttribute("d"), title: dot.textContent };
    });
    const X = (at) => 4 + (at - start) / (reset - start) * (geometry.width - 8);
    assert.ok(Math.abs(geometry.dot - X(readAt)) < 0.2);
    assert.ok(Math.abs(geometry.zero - X(runsOutAt)) < 0.2);
    assert.ok(geometry.dot < geometry.zero && geometry.zero < geometry.now);
    assert.ok(geometry.projection.startsWith("M" + geometry.dot.toFixed(1)), "projection begins at the observed point");
    assert.match(geometry.title, /40% left, read/);
    const verdict = page.locator(".subscription-card", { hasText: "ada@example.com" }).locator(".qv-text").first();
    assert.equal(await verdict.textContent(), "Runs out");
    assert.match(await verdict.getAttribute("class"), /k-warn/);
    assert.equal(await plot.locator(".qc-axis .axzero").textContent(), "Runs out");
  });
}

// #1004: a sub2api key given a 5-hour, day and 7-day limit answers its own
// windows now — 5 hours, 1 day, 7 days, in USD — so the Usage card must
// draw them like any other: a meter each, and the forecast where magpie
// has read them, with no card of its own to fall back to.
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "de"]) {
    test(`${engine} ${lang}: a sub2api key's own windows draw with the forecast`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      t.after(() => browser.close());
      const now = Date.now();
      const r5 = now + 2 * H, rd = now + 20 * H, rw = now + 5 * 24 * H;
      const usd = (amount, limit) => ({ amount, limit, unit: "USD" });
      const data = {
        quotas: [{ provider: "sub2api", name: "Sub2API", windows: [
          { name: "5 hours", used: 30, resetsAt: iso(r5), ...usd(90, 300) },
          { name: "1 day", used: 55, resetsAt: iso(rd), ...usd(165, 300) },
          { name: "7 days", used: 60, resetsAt: iso(rw), ...usd(480, 800) },
        ] }],
        history: [{ provider: "sub2api", user: "", lines: [
          { name: "5 hours", points: cycle(r5 - 5 * H, now, 100, 70), forecast: {
            state: "ok", source: "even", left: 70, evenLeft: 58, ahead: 12, ratePerHour: 6,
            lastsToReset: true, etaSeconds: 0, leftAtReset: 20, headroom: 1.3, cycles: 0,
            cycleStart: iso(r5 - 5 * H), resetsAt: iso(r5) } },
          // a day is one cycle: too short for the history layer (two days and
          // three completed cycles at least), so its forecast is the even burn
          { name: "1 day", points: cycle(rd - 24 * H, now, 100, 45), forecast: {
            state: "ok", source: "even", left: 45, evenLeft: 45, ahead: 0, ratePerHour: 2,
            lastsToReset: false, etaSeconds: 6 * H / 1000, leftAtReset: -5, headroom: 0.9, cycles: 0,
            cycleStart: iso(rd - 24 * H), resetsAt: iso(rd) } },
          // a week is long enough to blend its completed cycles, so its line
          // carries the history marker
          { name: "7 days", points: cycle(rw - 7 * 24 * H, now, 100, 40), forecast: {
            state: "ok", source: "history", left: 40, evenLeft: 48, ahead: -8, ratePerHour: 1,
            lastsToReset: false, etaSeconds: 3 * 24 * H / 1000, leftAtReset: -10, headroom: 0.8, cycles: 3,
            cycleStart: iso(rw - 7 * 24 * H), resetsAt: iso(rw) } },
        ] }],
      };
      const page = await (await browser.newContext({ viewport: { width: 1180, height: 900 }, reducedMotion: "reduce" })).newPage();
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      page.on("console", (m) => { if (m.type() === "error") errors.push("console: " + m.text()); });
      await page.route("**/*", serve(lang, "light", false, data));
      await page.goto("http://magpie.test/?view=usage");
      const card = page.locator(".subscription-card", { hasText: "Sub2API" });
      await card.locator(".quota-plot").first().waitFor();
      const names = { en: ["5 hours", "1 day", "7 days"], de: ["5 Stunden", "1 Tag", "7 Tage"] }[lang];
      assert.deepEqual(await card.locator(".qv-badge").allTextContents(), names, "the key's own windows are named");
      assert.equal(await card.locator(".quota-plot").count(), 3, "each window has its own burn-down");
      assert.equal(await card.locator("circle.qc-dot title").count(), 3, "each latest reading keeps its time");
      const verdicts = await card.locator(".qv-text").evaluateAll((es) => es.map((e) => e.className));
      assert.deepEqual(verdicts, ["qv-text k-safe", "qv-text k-warn", "qv-text k-warn"], "the forecast reaches a key's windows");
      assert.equal(await card.locator(".qv-text").nth(2).locator(".qv-src").textContent(), words[lang].hist, "the week's line carries the history marker");
      assert.equal(await page.locator(".status.err").count(), 0);
      assert.deepEqual(errors, []);
    });
  }
}

// Plenty of readings without cycle timing, untouched cycles, young cycles,
// and one reading all keep their observed curve without a false verdict.
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    test(`${engine} ${lang}: a window without a projection shows only observations`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      t.after(() => browser.close());
      const now = Date.now(), reset = now + 2 * H;
      const names = ["Untouched", "Unknown cycle", "One reading", "Young cycle"];
      const data = {
        quotas: [{ provider: "codex", name: "Codex", user: "idle@example.com", windows: names.map((name) => ({ name, used: 0, resetsAt: iso(reset) })) }],
        history: [{ provider: "codex", user: "idle@example.com", lines: [
          { name: names[0], points: Array.from({ length: 37 }, (_, i) => ({ at: iso(now - (36 - i) * 60000), left: 100, start: iso(now - 3 * H), resetsAt: iso(reset) })), forecast: { state: "none", left: 100, cycleStart: iso(now - 3 * H), resetsAt: iso(reset) } },
          { name: names[1], points: Array.from({ length: 15 }, (_, i) => ({ at: iso(now - (14 - i) * 60000), left: 85 - i })) },
          { name: names[2], points: [{ at: iso(now), left: 80 }] },
          { name: names[3], points: cycle(now - 12 * 60000, now, 100, 99), forecast: { state: "none", left: 99, cycleStart: iso(now - 12 * 60000), resetsAt: iso(reset) } },
        ] }],
      };
      for (const panel of [false, true]) {
        const page = await (await browser.newContext({ viewport: { width: panel ? 440 : 1000, height: 800 }, reducedMotion: "reduce" })).newPage();
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, "light", panel, data));
        await page.goto("http://magpie.test/" + (panel ? "?mode=panel" : "?view=usage"));
        if (panel) await page.locator('#ptabs [data-ptab="usage"]').click();
        const card = page.locator(panel ? ".pq-card" : ".subscription-card", { hasText: panel ? "idle@example.com" : "Codex" });
        await card.waitFor();
        assert.equal(await card.locator(".qv-text, .pq-verdict").count(), 0, "no verdict when there is no projection, regardless of the reason");
        if (!panel) {
          assert.equal(await card.locator(".quota-plot").count(), 4, "all observed curves remain reachable");
          assert.equal(await card.locator("circle.qc-dot title").count(), 4, "each latest reading still has its time");
          assert.equal(await card.locator("path.qc-proj").count(), 0);
        }
        assert.deepEqual(errors, []);
        await page.close();
      }
    });

    test(`${engine} ${lang}: family toggles draw at the attached width and refit counts`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      t.after(() => browser.close());
      const now = Date.now(), reset = now + 3 * H;
      const windows = ["Gemini", "Claude"].flatMap((family) => ["High", "Low"].map((level, i) => ({
        name: `${family} 3.7 Pro advanced thinking model (${level})`, family, used: 30 - i * 10, resetsAt: iso(reset),
        amount: 1234567890, limit: 9876543210, unit: "credits",
      })));
      const data = {
        quotas: [{ provider: "antigravity", name: "Antigravity", user: "families@example.com", windows }],
        history: [{ provider: "antigravity", user: "families@example.com", lines: windows.map((w) => ({
          name: w.name, points: cycle(now - 2 * H, now, 100, 100 - w.used),
        })) }],
      };
      const page = await (await browser.newContext({ viewport: { width: 560, height: 800 }, reducedMotion: "reduce" })).newPage();
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, "light", false, data));
      await page.goto("http://magpie.test/?view=usage");
      const card = page.locator(".subscription-card", { hasText: "Antigravity" });
      const toggle = card.locator(".quota-every");
      await toggle.waitFor();
      // A tall neighboring card can hold the wall's dimensions steady. Keep
      // that case explicit so a container resize cannot repair this toggle.
      await page.locator("#subscriptionUsage").evaluate((e) => e.style.minHeight = e.clientHeight + 1800 + "px");
      await page.waitForTimeout(100);
      const wall = await page.locator("#subscriptionUsage").boundingBox();
      for (const count of [4, 2, 4]) {
        await toggle.click();
        await page.waitForTimeout(100);
        assert.equal(await card.locator(".quota-plot").count(), count);
        assert.equal((await page.locator("#subscriptionUsage").boundingBox()).height, wall.height, "wall size stays unchanged across the toggle");
        const plots = await card.locator(".quota-plot").evaluateAll((es) => es.map((e) => ({
          width: e.clientWidth, drawn: Number(e.querySelector("svg").getAttribute("viewBox").split(" ")[2]),
        })));
        for (const p of plots) assert.equal(p.drawn, Math.max(240, Math.round(p.width)), `attached plot width: ${JSON.stringify(p)}`);
        const labels = await card.locator(".quota-windows").evaluate((g) => {
          const needsStack = [...g.querySelectorAll(".quota-labels")].some((l) => {
            const [name, n] = l.children;
            const countWidth = [...n.children].reduce((w, c) => w + c.getBoundingClientRect().width, 0) + 4 * (n.children.length - 1);
            return name.getBoundingClientRect().width + 6 + countWidth > l.clientWidth;
          });
          return { needsStack, stacked: g.classList.contains("stacked") };
        });
        assert.equal(labels.stacked, labels.needsStack, "counts refit only when they cannot sit beside names");
        const cut = await card.locator(".quota-n").evaluateAll((es) => es.some((e) => e.scrollWidth > e.clientWidth || [...e.children].some((c) => c.scrollWidth > c.clientWidth)));
        assert.equal(cut, false, "the count remains whole");
      }
      assert.deepEqual(errors, []);
    });
  }
}
