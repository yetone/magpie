// Run with Node's test runner and Playwright on the module path; see README.md.
// What was left over time (#651): under an account's meters on the Usage
// page, a line a window from magpie's readings — broken where the 5-hour
// window started again — a dashed even burn from each window's start to its
// reset, and an upright line at now. One pick in the allowances' head, the
// app's own menu, turns every card at once: "2 days" / "Cycle", or "Off"
// (ARNO on Discord: the curves made the cards cluttered), which draws no
// curve on any card, the tray's thin line included; each account can pick
// an earlier recorded cycle in Cycle mode. The global pick is remembered
// across a reload, and no click moves the page. Several accounts on a card
// are set apart by a rule, a curve's legend before the next one too. An
// account with no readings has no curve. The tray's card has a low line of
// its first window's cycle, with no now line (#1601), and a click into the
// tray's list rings nothing.
// English and Chinese, light and dark; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const vm = require("node:vm");
const { chromium, webkit } = require("playwright");

const assets = process.env.MAGPIE_QUOTA_ASSETS || path.resolve(__dirname, "../assets");
const H = 3600e3;
const iso = (ms) => new Date(ms).toISOString();

function fixtures(now) {
  const r5 = now + 2 * H, rw = now + 3 * 24 * H;
  const quotas = [
    { provider: "codex", name: "Codex", icon: "openai", plan: "Plus", user: "a@x.com", windows: [
      { name: "5 hours", used: 40, resetsAt: iso(r5) },
      { name: "Weekly", used: 30, resetsAt: iso(rw) },
    ] },
    { provider: "codex", name: "Codex", icon: "openai", plan: "Pro", user: "b@x.com", windows: [
      { name: "5 hours", used: 10, resetsAt: iso(r5) },
    ] },
    { provider: "kimi", name: "Kimi", icon: "kimi-color", windows: [{ name: "Weekly", used: 10, resetsAt: iso(rw) }] },
  ];
  const five = [], week = [];
  // the 5-hour window before the last reset, then this one
  for (let m = 0; m <= 180; m += 30) five.push({ at: iso(r5 - 10 * H + m * 60e3), left: 100 - m / 3, start: iso(r5 - 10 * H), resetsAt: iso(r5 - 5 * H) });
  for (let m = 0; m <= 180; m += 30) five.push({ at: iso(r5 - 5 * H + m * 60e3), left: 100 - m / 3, start: iso(r5 - 5 * H), resetsAt: iso(r5) });
  for (let h = 90; h >= 0; h -= 6) week.push({ at: iso(now - h * H), left: 70 + h / 9, start: iso(rw - 168 * H), resetsAt: iso(rw) });
  const history = [
    { provider: "codex", user: "a@x.com", lines: [{ name: "5 hours", points: five }, { name: "Weekly", points: week }] },
    { provider: "codex", user: "b@x.com", lines: [{ name: "5 hours", points: five.slice(-4) }] },
  ];
  return { quotas, history };
}

const cycleWords = {
  en: { choose: "Choose a recorded cycle", past: "Past cycle", missing: "No readings in this cycle", duration: "Recorded duration:", reading: "Last reading", ended: "Cycle ended" },
  zh: { choose: "选择已记录的周期", past: "历史周期", missing: "本周期无记录", duration: "已记录时长：", reading: "最后采样于", ended: "周期结束于" },
  "zh-TW": { choose: "選擇已記錄的週期", past: "歷史週期", missing: "此週期無記錄", duration: "已記錄時長：", reading: "最後取樣於", ended: "週期結束於" },
  ja: { choose: "記録されたサイクルを選択", past: "過去のサイクル", missing: "このサイクルの記録なし", duration: "記録された期間:", reading: "最終記録:", ended: "サイクル終了:" },
  de: { choose: "Aufgezeichneten Zyklus wählen", past: "Vergangener Zyklus", missing: "Keine Messwerte in diesem Zyklus", duration: "Erfasste Dauer:", reading: "Letzte Messung:", ended: "Zyklusende:" },
};
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, w] of Object.entries(cycleWords)) {
    test(`${engine} ${lang}: recorded quota cycles include manual and unscheduled resets`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const context = await browser.newContext({ viewport: { width: 440, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const now = Date.now(), D = 24 * H;
      const data = fixtures(now);
      data.quotas[0].readAt = iso(now);
      data.quotas[0].windows[1].holds = { tokens: 100000, cost: 12, priced: true, used: 30, routed: { calls: 10, tokens: 30000, cost: 3.6 } };
      const point = (day, left, reset) => ({ at: iso(now + day * D), left, start: iso(now + (reset - 7) * D), resetsAt: iso(now + reset * D) });
      // An early manual reset, an official refill with the SAME deadline,
      // then an ordinary week. None is found by subtracting seven days
      // from today's deadline. Only the manual cycle has old 5-hour data.
      data.quotas[0].windows[1].resetsAt = iso(now + D);
      data.history[0].lines[1].points = [
        point(-16, 90, -10), point(-14, 20, -10),
        point(-13, 100, -6), point(-12, 70, -6),
        point(-11, 100, -6), point(-9, 30, -6),
        point(-5, 95, 1), point(-3, 80, 1), point(-1 / 24, 65, 1),
      ];
      const shortReset = now - 12 * D + H;
      data.history[0].lines[0].points.unshift({ at: iso(now - 12 * D), left: 12, start: iso(shortReset - 5 * H), resetsAt: iso(shortReset) });
      const historicalHolds = (tokens, used) => ({ tokens, priced: false, used, since: iso(now - 13 * D), routed: { calls: 2, tokens: tokens * used / 100 } });
      data.historyHolds = (url) => {
        const end = Date.parse(url.searchParams.get("to"));
        const short = Math.abs(end - (now - 11 * D)) < 1000;
        const used = short ? 30 : Math.abs(end - (now - 6 * D)) < 1000 ? 70 : 80;
        return [{ provider: "codex", user: "a@x.com", windows: [
          ...(short ? [{ name: "5 hours", holds: historicalHolds(12000, 88) }] : []),
          { name: "Weekly", holds: historicalHolds(700000 + used, used) },
        ] }];
      };
      const reads = [];
      const handler = serve(lang, "light", false, data);
      await page.route("**/*", (r) => { if (r.request().url().includes("/quotas/history")) reads.push(r.request().url()); return handler(r); });
      await page.goto("http://magpie.test/?view=usage");
      const card = page.locator('.subscription-card[data-key="codex"]');
      const curve = card.locator(".quota-curve").first();
      await curve.waitFor();
      assert.equal(await curve.locator("button.qc-range").count(), 1, "each account can choose a recorded cycle");
      assert.ok(reads.some((url) => new URL(url).searchParams.get("days") === "45"), "all retained history is requested");
      await card.locator(".quota-more").click();
      const other = card.locator(".quota-curve").nth(1);
      const otherAxis = await other.locator(".qc-axis").textContent();
      const currentAxis = await curve.locator(".qc-axis").textContent();
      const currentLegend = await curve.locator(".qc-key").allTextContents();
      const meters = card.locator(".quota-windows").first();
      const currentMeters = await meters.locator(".quota-n").allTextContents();
      assert.equal(await meters.locator(".quota-holds").count(), 1, "the current view keeps upstream's whole-window estimate");
      const otherMeters = await card.locator(".quota-windows").nth(1).locator(".quota-n").allTextContents();
      const pick = curve.getByRole("button", { name: w.choose });
      // this cycle's pace is said; a past cycle's is over and says none (#2079)
      assert.notEqual(await curve.locator(".qc-pace").textContent(), "", "the current cycle tells its pace");
      for (const [index, left] of [[1, 30], [2, 70], [3, 20]]) {
        const before = await page.locator("#view-usage").evaluate((v) => v.scrollTop);
        await pick.click();
        const items = page.locator(".sess-menu .pm-item");
        assert.equal(await items.count(), 4, "current + three observed cycles");
        const notes = await items.locator(".pm-note").allTextContents();
        assert.ok(notes.slice(1).every((note) => note.startsWith(w.duration)), "menu describes the observed duration, not the window's nominal name");
        assert.notEqual(notes[1], notes[2], "a regular week and an early reset have different lengths");
        await items.nth(index).click();
        assert.equal(await pick.textContent(), w.past);
        assert.equal(await curve.locator(".qc-pace").textContent(), "", "a past cycle has no pace to tell");
        const hasShort = index === 2;
        await page.waitForFunction((n) => document.querySelector('.subscription-card[data-key="codex"] .quota-windows').querySelectorAll(".quota-holds").length === n, hasShort ? 2 : 1);
        const estimates = await meters.locator(".quota-holds").allTextContents();
        assert.ok(estimates.every((text) => !text.includes("100K") && !text.includes("$12")), "today's estimate is never used for history");
        assert.equal(await meters.locator(".quota-holds").count(), hasShort ? 2 : 1, "historical estimates follow the sampled windows");
        assert.deepEqual(await curve.locator(".qc-key b").allTextContents(), hasShort ? ["12%", left + "%"] : [left + "%"], "the historical legend uses that cycle's final reading");
        assert.match(await meters.locator(".quota-n").nth(1).textContent(), new RegExp((100 - left) + "%"), "the weekly meter follows the historical selection");
        if (hasShort) assert.match(await meters.locator(".quota-n").first().textContent(), /88%/, "the 5-hour meter uses its last reading inside that cycle");
        else assert.equal(await meters.locator(".quota-n").first().textContent(), w.missing, "missing history never shows today's meter");
        assert.ok((await meters.locator(".quota-reset").allTextContents()).every((text) => text.startsWith(w.ended)), "historical dates aren't current countdowns");
        assert.ok((await card.locator(".quota-read").first().textContent()).includes(w.reading));
        assert.equal(await card.locator(".quota-autoreset").count(), 1, "only the other account retains current controls");
        assert.equal(await curve.locator("line.qc-now").count(), 0, "now is outside a completed cycle");
        assert.equal(await curve.locator("line.qc-even").count(), hasShort ? 2 : 1, "the historical even pace belongs to the selected cycle");
        assert.equal(await curve.locator("path.qc-line").count(), hasShort ? 2 : 1, "no fabricated short-window readings");
        assert.notEqual(await curve.locator(".qc-axis").textContent(), currentAxis);
        assert.equal(await other.locator(".qc-axis").textContent(), otherAxis, "another account stays on its own cycle");
        assert.deepEqual(await card.locator(".quota-windows").nth(1).locator(".quota-n").allTextContents(), otherMeters);
        assert.equal(await page.locator("#view-usage").evaluate((v) => v.scrollTop), before, "choosing a cycle doesn't scroll");
        const coords = await curve.locator("path.qc-line").first().getAttribute("d");
        assert.ok(!/NaN|Infinity/.test(coords), coords);
      }
      await meters.locator(".quota-n").nth(1).click();
      assert.match(await meters.locator(".quota-n").nth(1).textContent(), /20%/, "Left is also the selected cycle's historical value");
      assert.equal(await meters.locator(".quota-n").first().textContent(), w.missing);
      await meters.locator(".quota-n").nth(1).click();
      assert.match(await meters.locator(".quota-n").nth(1).textContent(), /80%/);
      await page.locator("#quotaTrend").click();
      await page.locator(".sess-menu .pm-item").nth(1).click();
      assert.deepEqual(await meters.locator(".quota-n").allTextContents(), currentMeters, "2 days restores current meters");
      await page.locator("#quotaTrend").click();
      await page.locator(".sess-menu .pm-item").nth(2).click();
      assert.match(await meters.locator(".quota-n").nth(1).textContent(), /80%/, "Cycle resumes the account's selected historical period");
      await card.locator(".quota-refresh").first().click();
      await page.waitForFunction(() => !document.querySelector(".quota-refresh.busy"));
      assert.equal(await pick.textContent(), w.past, "a refresh preserves the selected historical cycle");
      assert.deepEqual(await curve.locator(".qc-key b").allTextContents(), ["20%"]);
      await pick.click();
      await page.locator(".sess-menu .pm-item").first().click();
      assert.equal(await curve.locator(".qc-axis").textContent(), currentAxis);
      assert.deepEqual(await curve.locator(".qc-key").allTextContents(), currentLegend);
      assert.deepEqual(await meters.locator(".quota-n").allTextContents(), currentMeters, "returning to this cycle restores today's meters");
      assert.equal(await meters.locator(".quota-holds").count(), 1, "returning to this cycle restores upstream's whole-window estimate");
      assert.deepEqual(await page.evaluate(() => ({ page: document.documentElement.scrollWidth > innerWidth, cards: [...document.querySelectorAll(".subscription-card")].some((c) => c.scrollWidth > c.clientWidth + 1) })), { page: false, cards: false });
      assert.deepEqual(errors, []);
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await pick.click();
        await page.locator(".sess-menu .pm-item").nth(2).click();
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-quota-history.png`) });
      }
    });
  }
}

test("quotaEta: said only from a falling run of this cycle, never for one that is over (#2079)", async () => {
  const source = await fs.readFile(path.join(assets, "app.js"), "utf8");
  const start = source.indexOf("const quotaEtaSpan");
  const end = source.indexOf("\n}\n", source.indexOf("function quotaEta(l, now) {"));
  const inVm = vm.runInNewContext(source.slice(start, end + 2) + "; quotaEta");
  const eta = (l, now) => JSON.parse(JSON.stringify(inVm(l, now))); // out of the vm's realm
  const now = Date.parse("2026-10-14T12:00:00Z"), D = 86400e3;
  const line = (cycle, ...pts) => ({ cycle, points: pts.map(([d, left]) => ({ at: now + d * D, left })) });
  const open = { start: now - 5 * D, reset: now + 2 * D };
  // 10 a day from 60: empty in 4 days, after the reset two days off
  assert.deepEqual(eta(line(open, [-2, 60], [-1, 50], [0, 40]), now), { out: now + 4 * D, lasts: true });
  // 20 a day from 40: empty in 2 days less a little, before that reset
  const r = eta(line({ ...open, reset: now + 3 * D }, [-2, 80], [-1, 60], [0, 40]), now);
  assert.equal(r.lasts, false); assert.equal(Math.round((r.out - now) / 36e5), 48);
  assert.equal(eta(line(open, [-1, 50], [0, 40]), now), null, "two readings say nothing");
  assert.equal(eta(line(open, [-1 / 48, 50], [-1 / 96, 45], [0, 40]), now), null, "half an hour says nothing");
  assert.equal(eta(line(open, [-2, 20], [-1, 10], [0, 0]), now), null, "nothing left says nothing");
  assert.deepEqual(eta(line(open, [-2, 50], [-1, 50], [0, 50]), now), { lasts: true, out: null }, "not falling lasts the cycle");
  // a cycle that is over (a past one picked) says nothing, flat or falling
  const past = { start: now - 9 * D, reset: now - 2 * D };
  assert.equal(eta(line(past, [-5, 50], [-4, 50], [-3, 50]), now), null);
  assert.equal(eta(line(past, [-5, 60], [-4, 50], [-3, 40]), now), null);
});

test("quotaCycle: an early reset starts the cycle though the card's reset has milliseconds history doesn't (steven_4422)", async () => {
  const source = await fs.readFile(path.join(assets, "app.js"), "utf8");
  const piece = (from, to) => source.slice(source.indexOf(from), source.indexOf("\n}\n", source.indexOf(to)) + 2);
  const quotaCycle = vm.runInNewContext(piece("function quotaCycleBreak(a, b) {", "function quotaCycleBreak(a, b) {") + "\n" +
    piece("const quotaCyclesRead", "function quotaRecordedCycles(l) {") + "\n" + piece("function quotaCycle(l, now) {", "function quotaCycle(l, now) {") + "; quotaCycle");
  const T = (s) => Date.parse(s);
  const start = T("2026-11-04T10:40:00Z"), reset = T("2026-11-11T10:40:00Z");
  // a weekly window refilled early at 02:00 on the 9th, its deadline kept
  const points = [["2026-11-08T12:00:00Z", 30], ["2026-11-09T01:50:00Z", 20], ["2026-11-09T02:05:00Z", 100], ["2026-11-09T12:00:00Z", 90]]
    .map(([at, left]) => ({ at: T(at), left, start, reset }));
  const now = T("2026-11-09T13:00:00Z");
  for (const resetsAt of ["2026-11-11T10:40:00Z", "2026-11-11T10:40:00.437Z", "2026-11-11T10:40:02Z"]) {
    const c = quotaCycle({ window: { resetsAt }, points }, now);
    assert.equal(new Date(c.start).toISOString(), "2026-11-09T02:05:00.000Z", resetsAt);
  }
  // a deadline the card moved by a day is another cycle: the vendor's start, carried along
  const c = quotaCycle({ window: { resetsAt: "2026-11-12T10:40:00Z" }, points }, now);
  assert.equal(new Date(c.start).toISOString(), "2026-11-05T10:40:00.000Z");
});

test("quota curve distinguishes fixed refills, rolling recovery and clock rounding", async () => {
  const source = await fs.readFile(path.join(assets, "app.js"), "utf8");
  const start = source.indexOf("function quotaCycleBreak(a, b) {");
  const end = source.indexOf("\n}\n", start);
  const check = vm.runInNewContext(source.slice(start, end + 2) + "; quotaCycleBreak");
  const at = Date.UTC(2026, 9, 1), reset = at + 7 * 24 * H;
  const a = { at, reset, left: 20 };
  for (const [name, gap, moved, left, want] of [
    ["fixed deadline refill", 60e3, 0, 100, true],
    ["rounded fixed refill", 10 * 60e3, 60e3, 100, true],
    ["manual restart", 60e3, 24 * H, 100, true],
    ["earlier official restart", 60e3, -24 * H, 100, true],
    ["normal expiry", 7 * 24 * H, 7 * 24 * H, 100, true],
    ["small correction", 60e3, 0, 24, false],
    ["clock rounding", 60e3, 60e3, 20, false],
    ["rolling recovery", H, H, 80, false],
    ["fast rolling recovery", 60e3, 60e3, 80, false],
  ]) assert.equal(check(a, { at: at + gap, reset: reset + moved, left }), want, name);
});

function serve(lang, theme, panel, data) {
  const settings = { theme, lang, tray: "panel", quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (body) => route.fulfill({ json: body });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:${!panel}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/settings/quota-left") { settings.quotaLeft = route.request().postDataJSON().on; return json(settings); }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json(data.quotas);
    if (url.pathname === "/api/usage/quotas/history/holds") return json(data.historyHolds ? await data.historyHolds(url) : []);
    if (url.pathname === "/api/usage/quotas/history") return json(data.history);
    if (url.pathname === "/api/usage") return json({ calls: 0, input: 0, output: 0, cost: 0, agents: [], accounts: [], models: [], series: [], days: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { head: "Left over time", two: "2 days", cycle: "Cycle", off: "Off", trend: (r) => "Trends: " + r, five: "5 hours", week: "Weekly", dashes: /Dashed: an even pace/,
    lasts: "Lasts past the reset at this pace", runsOut: /^Runs out .+ at this pace$/ },
  zh: { head: "剩余额度走势", two: "2 天", cycle: "本周期", off: "关闭", trend: (r) => "走势：" + r, five: "5 小时", week: "每周", dashes: /虚线：匀速用量/,
    lasts: "按此速度，能撑到重置", runsOut: /^按此速度，预计 .+ 用完$/ },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: quota cards draw what was left over time`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const pages = [];
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          for (const [name, p] of pages) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-quota-curve-${name}.png`) });
        }
        await browser.close();
      });
      const errors = [];
      const data = fixtures(Date.now());
      const open = async (name, url, theme, viewport, over) => {
        const page = await (await browser.newContext({ viewport, reducedMotion: "reduce" })).newPage();
        pages.push([name, page]);
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, theme, url.includes("mode=panel"), over || data));
        await page.goto(url);
        return page;
      };

      const strokes = {};
      for (const theme of ["light", "dark"]) {
        const page = await open(theme, "http://magpie.test/?view=usage", theme, { width: 900, height: 620 });
        const card = page.locator(".subscription-card", { hasText: "Codex" });
        const curve = card.locator(".quota-curve").first();
        await curve.waitFor();
        assert.equal(await curve.locator(".qc-head > span").first().textContent(), w.head);
        // the range is picked once, in the allowances' head, not on every card
        const pick = page.locator("#quotaHead #quotaTrend");
        assert.equal(await pick.textContent(), w.trend(w.cycle), "Cycle first");
        assert.equal(await page.locator(".quota-curve .segs, .quota-curve button:not(.qc-range), .quota-curve select").count(), 0, "only the account's cycle picker on a card");
        assert.equal(await curve.locator(".qc-range").textContent(), w.cycle);
        // the window whose reset is furthest off, in words: a 52px plot
        // leaves the reader to work out which side of the dashes it is on,
        // and that is the only question the curve is asked (#2079)
        assert.equal(await curve.locator(".qc-pace").textContent(), w.lasts);
        // a line a window, now; the 5-hour window, a comb over the week's
        // cycle, faint and without its upright even burn
        assert.equal(await curve.locator("path.qc-line").count(), 2);
        assert.equal(await curve.locator("line.qc-even").count(), 1);
        assert.match(await curve.getAttribute("title"), w.dashes, "hovering the plot says what the dashes are");
        assert.equal(await curve.locator("path.qc-line.qc-short").getAttribute("data-name"), "5 hours");
        assert.equal(await curve.locator("line.qc-now").count(), 1);
        const five = await curve.locator('path.qc-line[data-name="5 hours"]').getAttribute("d");
        assert.equal((five.match(/M/g) || []).length, 1, "the 5-hour line is its current cycle only, no comb of earlier drops: " + five);
        const legend = await curve.locator(".qc-key").allTextContents();
        assert.deepEqual(legend, [w.five + "40%", w.week + "70%"]);
        assert.equal(await page.locator(".subscription-card", { hasText: "Kimi" }).locator(".quota-curve").count(), 0, "no readings, no curve");
        // the colours are the theme's chart colours, nothing outside the card
        strokes[theme] = await curve.locator("path.qc-line").first().evaluate((e) => getComputedStyle(e).stroke);
        const c1 = await page.evaluate(() => { const s = document.createElement("i"); s.style.color = "var(--c1)"; document.body.append(s); const c = getComputedStyle(s).color; s.remove(); return c; });
        assert.equal(strokes[theme], c1);
        const box = await card.boundingBox(), plot = await curve.locator(".qc-plot").boundingBox();
        assert.ok(plot.x >= box.x && plot.x + plot.width <= box.x + box.width + 0.5, "the plot fits its card");
        const left = await curve.evaluate((e) => [...e.querySelectorAll("*")].filter((x) => parseFloat(getComputedStyle(x).borderLeftWidth) > 0).length);
        assert.equal(left, 0, "no left borders");

        if (theme === "light") {
          // several accounts: the next one is set apart from a curve's legend
          await card.locator(".quota-more").click();
          const second = card.locator(".subscription-account", { hasText: "b@x.com" });
          // (after the curve, a Codex account's Auto-use row, #719, then its
          // Use credits row, which closes its section)
          assert.deepEqual(await second.evaluate((e) => [e.previousElementSibling.previousElementSibling.previousElementSibling.className, e.previousElementSibling.previousElementSibling.className, e.previousElementSibling.className]), ["quota-curve", "quota-autoreset ar-split", "quota-credits on"]);
          assert.notEqual(await second.evaluate((e) => getComputedStyle(e).borderTopStyle), "none", "a rule between the accounts");
          const choose = async (name) => {
            await pick.click();
            const items = page.locator(".sess-menu .pm-item");
            assert.deepEqual(await items.locator(".pm-name").allTextContents(), [w.off, w.two, w.cycle]);
            await items.filter({ hasText: name }).click();
          };
          // 2 days: the range turns, the page doesn't move, the pick is kept
          const scroller = await page.evaluate(() => { const s = document.scrollingElement; s.scrollTop = 40; return s.scrollTop; });
          const before = await curve.locator(".qc-axis").textContent();
          await choose(w.two);
          assert.equal(await pick.textContent(), w.trend(w.two));
          assert.equal(await curve.locator(".qc-range").textContent(), w.two);
          assert.notEqual(await curve.locator(".qc-axis").textContent(), before);
          assert.notEqual(await curve.locator('path.qc-line[data-name="5 hours"]').getAttribute("d"), five);
          assert.equal(await curve.locator("line.qc-even").count(), 2, "over two days the 5-hour window has its even burn");
          assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), scroller, "the click moved the page");
          assert.equal(await page.evaluate(() => localStorage.getItem("magpie.quotaRange")), "2d");
          // Off: no card draws a curve, the pick stays where it was, kept
          // across a reload; back on, the curves come back
          const at = (await pick.boundingBox()).y;
          await choose(w.off);
          assert.equal(await page.locator(".quota-curve").count(), 0);
          assert.equal(await pick.textContent(), w.trend(w.off));
          assert.equal((await pick.boundingBox()).y, at, "the pick moved");
          assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), scroller, "the click moved the page");
          assert.equal(await page.evaluate(() => localStorage.getItem("magpie.quotaRange")), "off");
          await page.reload();
          await page.locator(".subscription-card", { hasText: "Codex" }).locator(".quota-windows").first().waitFor();
          assert.equal(await page.locator(".quota-curve").count(), 0, "Off is kept across a restart");
          assert.equal(await pick.textContent(), w.trend(w.off));
          await choose(w.cycle);
          await curve.waitFor();
          assert.equal(await page.evaluate(() => localStorage.getItem("magpie.quotaRange")), "cycle");
        }
      }
      assert.notEqual(strokes.light, strokes.dark, "dark has its own line colour");

      // the same card, a week emptying before its reset: the words turn
      // around, and a reading taken this cycle is all a pace needs (#2079)
      const rw = Date.parse(data.quotas[0].windows[1].resetsAt), t0 = Date.now();
      data.history[0].lines[1].points = [
        { at: iso(t0 - 48 * H), left: 60, start: iso(rw - 168 * H), resetsAt: iso(rw) },
        { at: iso(t0 - 24 * H), left: 40, start: iso(rw - 168 * H), resetsAt: iso(rw) },
        { at: iso(t0), left: 20, start: iso(rw - 168 * H), resetsAt: iso(rw) },
      ];
      const counting = await open("counting", "http://magpie.test/?view=usage", "light", { width: 900, height: 620 });
      const countingCurve = counting.locator(".subscription-card", { hasText: "Codex" }).locator(".quota-curve").first();
      await countingCurve.waitFor();
      assert.match(await countingCurve.locator(".qc-pace").textContent(), w.runsOut);

      // the tray: a thin line of the 5-hour window's cycle, across the card
      // Its own readings, three over three hours: clearly slower than the
      // window's even burn, so the pace has one answer rather than landing
      // exactly on the deadline (a tie, and a tie is no test) (#2079)
      const p0 = Date.now();
      data.history[0].lines[0].points = [
        { at: iso(p0 - 3 * H), left: 100, start: iso(p0 - 3 * H), resetsAt: iso(p0 + 2 * H) },
        { at: iso(p0 - 2 * H), left: 85, start: iso(p0 - 3 * H), resetsAt: iso(p0 + 2 * H) },
        { at: iso(p0 - 1 * H), left: 70, start: iso(p0 - 3 * H), resetsAt: iso(p0 + 2 * H) },
      ];
      const panel = await open("panel", "http://magpie.test/?mode=panel", "dark", { width: 440, height: 640 });
      await panel.locator('#ptabs [data-ptab="usage"]').click();
      const pcard = panel.locator(".pq-card", { hasText: "a@x.com" });
      const spark = pcard.locator(".pq-spark");
      await spark.waitFor();
      assert.equal(await spark.locator("path.qc-line").count(), 1);
      assert.equal((((await spark.locator("path.qc-line").getAttribute("d")).match(/M/g)) || []).length, 1, "the current cycle only");
      assert.equal(await spark.locator("line.qc-even").count(), 1);
      // Both windows begun: the line names both reset times, and a pace
      // after it would read as the week's, so none is said there (#2079)
      assert.equal(await pcard.locator(".pq-pace").count(), 0, "no pace beside another window's deadline");
      // the 5-hour window the only one begun: its pace, by its deadline
      const single = JSON.parse(JSON.stringify(data));
      single.quotas[0].windows[1].used = 0;
      const one = await open("one", "http://magpie.test/?mode=panel", "dark", { width: 440, height: 640 }, single);
      await one.locator('#ptabs [data-ptab="usage"]').click();
      const ocard = one.locator(".pq-card", { hasText: "a@x.com" });
      await ocard.waitFor();
      assert.equal(await ocard.locator(".pq-pace").textContent(), " · " + w.lasts);
      // a window magpie has read nothing of this cycle says nothing, rather
      // than reading a pace from nothing (#2079)
      const quiet = JSON.parse(JSON.stringify(single));
      quiet.history[0].lines[0].points = [];
      const silent = await open("silent", "http://magpie.test/?mode=panel", "dark", { width: 440, height: 640 }, quiet);
      await silent.locator('#ptabs [data-ptab="usage"]').click();
      const scard = silent.locator(".pq-card", { hasText: "a@x.com" });
      await scard.waitFor();
      assert.equal(await scard.locator(".pq-pace").count(), 0, "nothing to say, nothing said");
      assert.ok((await scard.locator(".pq-sub").textContent()).includes(lang === "zh" ? "重置于" : "Resets"), "the deadline is still named");
      // the line ends at now already; in 16px a dotted now line read as a
      // stray mark under the curve (#1601)
      assert.equal(await spark.locator("line.qc-now").count(), 0, "no now line in the thin spark");
      // measured in one go once drawn: the cards are drawn again when the
      // allowances read on opening the tab come back, and a card looked up
      // in one call and measured in the next could be the one taken out
      // (red under load, on main too)
      const { sb, cb } = await panel.waitForFunction(() => {
        const c = [...document.querySelectorAll("#panelQuota .pq-card")].find((c) => c.textContent.includes("a@x.com"));
        const s = c?.querySelector(".pq-spark");
        if (!s || !s.getBoundingClientRect().width) return null;
        const box = (e) => { const r = e.getBoundingClientRect(); return { width: r.width, height: r.height }; };
        return { sb: box(s), cb: box(c) };
      }).then((h) => h.jsonValue());
      // 30px since #2079: in 16px a month read as one line on its dashes
      assert.ok(sb.height <= 31 && sb.width > cb.width * 0.7, `low and across the card: ${JSON.stringify(sb)}`);
      assert.equal(await panel.locator(".pq-card", { hasText: "Kimi" }).locator(".pq-spark").count(), 0);
      // a click into the list, then a key to scroll it, rings nothing: the
      // view takes focus only so the keys scroll it (#1601, WebKitGTK)
      // (after the spark is measured: the click focuses the window, whose
      // focus reloads and redraws the cards, so a card measured then can be
      // between two draws)
      await panel.mouse.click(220, 300);
      await panel.keyboard.press("ArrowDown");
      const ring = await panel.evaluate(() => { const a = document.activeElement, cs = getComputedStyle(a); return { view: a.classList.contains("view"), outline: cs.outlineStyle === "none" ? "none" : cs.outlineStyle + " " + cs.outlineWidth }; });
      assert.deepEqual(ring, { view: true, outline: "none" }, "the scrolled view shows no focus ring");
      // turned off in the window, the tray's line goes too, and stays gone
      const win = await panel.context().newPage();
      await win.route("**/*", serve(lang, "dark", false, data));
      // a page of magpie's origin, only for its storage: served as a 404
      // with no body, Chromium refused to open it (ERR_HTTP_RESPONSE_CODE_FAILURE)
      await win.route("http://magpie.test/blank", (r) => r.fulfill({ contentType: "text/html", body: "<!doctype html><title>blank</title>" }));
      await win.goto("http://magpie.test/blank");
      await win.evaluate(() => localStorage.setItem("magpie.quotaRange", "off"));
      await panel.waitForFunction(() => !document.querySelector(".pq-spark"));
      assert.ok(await pcard.locator(".pq-dial").count() > 0, "the card is still there");
      await panel.reload();
      await panel.locator('#ptabs [data-ptab="usage"]').click();
      await pcard.locator(".pq-dial").first().waitFor();
      assert.equal(await panel.locator(".pq-spark").count(), 0, "Off is kept in the tray");
      assert.deepEqual(errors, []);
    });
  }
}

const cbWords = {
  en: { label: "Colorblind mode", off: "Off", on: "On", tip: "Each window's line has the dash", solid: "Solid: what was left." },
  zh: { label: "色盲模式", off: "关闭", on: "开启", tip: "每个窗口的线型", solid: "实线：剩余额度。" },
  "zh-TW": { label: "色盲模式", off: "關閉", on: "開啟", tip: "每個時段的線型", solid: "實線：剩餘額度。" },
  ja: { label: "色覚サポート", off: "オフ", on: "オン", tip: "各ウィンドウの線種", solid: "実線：残量。" },
  de: { label: "Farbenblind-Modus", off: "Aus", on: "An", tip: "Jedes Zeitfenster hat das Strichmuster", solid: "Durchgezogen: der verbleibende Rest." },
};

// colorblindPage: the Usage and Settings pages over fixtures, with the
// settings (colorblind included) kept as a backend would and every save
// recorded
async function colorblindPage(browser, lang, data, colorblind) {
  const posts = [];
  const st = { theme: "light", colorblind, lang, tray: "panel", quotaLeft: false, currency: "usd" };
  const context = await browser.newContext({ viewport: { width: 900, height: 640 }, reducedMotion: "reduce" });
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const base = serve(lang, "light", false, data);
  await page.route("**/*", async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (body) => route.fulfill({ json: body });
    if (url.pathname === "/boot.js") {
      const boot = { lang, theme: st.theme, web: true, ...(st.colorblind ? { colorblind: true } : {}) };
      return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify(boot)};` });
    }
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: st });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") {
        const b = req.postDataJSON();
        posts.push(b);
        Object.assign(st, b);
      }
      return json(st);
    }
    return base(route);
  });
  return { page, st, posts, errors };
}

// readCurve: what a quota curve shows of each window, as drawn
function readCurve(page, sel) {
  return page.evaluate((sel) => {
    const parse = (s) => s.match(/\d+(\.\d+)?/g).slice(0, 3).map(Number);
    const lum = ([r, g, b]) => {
      const [R, G, B] = [r, g, b].map((v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; });
      return 0.2126 * R + 0.7152 * G + 0.0722 * B;
    };
    const hue = ([r, g, b]) => {
      r /= 255; g /= 255; b /= 255;
      const max = Math.max(r, g, b), min = Math.min(r, g, b), d = max - min;
      if (!d) return 0;
      const h = max === r ? ((g - b) / d + (g < b ? 6 : 0)) : max === g ? (b - r) / d + 2 : (r - g) / d + 4;
      return h * 60;
    };
    const curve = document.querySelector(sel);
    const lines = [...curve.querySelectorAll("path.qc-line")].map((p) => {
      const cs = getComputedStyle(p);
      const rgb = parse(cs.stroke);
      return { series: p.dataset.series, stroke: cs.stroke, dash: cs.strokeDasharray, opacity: +cs.opacity, lum: lum(rgb), hue: hue(rgb) };
    });
    const evens = [...curve.querySelectorAll("line.qc-even")].map((e) => ({ series: e.dataset.series, stroke: getComputedStyle(e).stroke, dash: getComputedStyle(e).strokeDasharray }));
    const keys = [...curve.querySelectorAll(".qc-key")].map((k) => {
      const dot = k.querySelector("i"), sample = k.querySelector(".qc-sample"), line = sample?.querySelector("line");
      const kept = [...k.querySelectorAll("*")].filter((x) => parseFloat(getComputedStyle(x).borderLeftWidth) > 0).length;
      return {
        series: k.dataset.series, dot: getComputedStyle(dot).display !== "none", dotColor: getComputedStyle(dot).backgroundColor,
        sample: !!sample && getComputedStyle(sample).display !== "none" && sample.getBoundingClientRect().width > 10,
        sampleStroke: line && getComputedStyle(line).stroke, sampleDash: line && getComputedStyle(line).strokeDasharray, borders: kept,
      };
    });
    return { cb: document.documentElement.dataset.colorblind || "", title: curve.title, lines, evens, keys };
  }, sel);
}

const codexCurve = '.subscription-card[data-key="codex"] .quota-curve';

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, w] of Object.entries(cbWords)) {
    test(`${engine} ${lang}: colorblind mode separates quota windows by colour and dash, in the legend too (#2073)`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const { page, st, posts, errors } = await colorblindPage(browser, lang, fixtures(Date.now()), false);

      // off by default: the dots and solid lines of before
      await page.goto("http://magpie.test/?view=usage");
      await page.locator(codexCurve).first().waitFor();
      let got = await readCurve(page, codexCurve);
      assert.equal(got.cb, "");
      assert.ok(got.lines.every((l) => l.dash === "none"), JSON.stringify(got.lines));
      assert.ok(got.keys.every((k) => k.dot && !k.sample), JSON.stringify(got.keys));
      assert.ok(got.title.startsWith(w.solid), got.title);
      const plainEven = got.evens[0].dash;

      await page.goto("http://magpie.test/?view=settings&tab=general");
      const row = page.locator("#colorblindRow");
      await row.waitFor();
      assert.equal(await row.locator(".name").innerText(), w.label);
      assert.deepEqual(await row.locator("#colorblindSegs .opt").allInnerTexts(), [w.off, w.on]);
      assert.equal(await row.locator("#colorblindSegs .opt.on").innerText(), w.off, "off by default");
      const scroll0 = await page.locator("#view-settings").evaluate((v) => v.scrollTop);
      await row.locator("#colorblindSegs .opt", { hasText: w.on }).click();
      for (let i = 0; i < 50 && !posts.length; i++) await page.waitForTimeout(40);
      assert.equal(posts.at(-1)?.colorblind, true);
      assert.equal(await page.locator("#view-settings").evaluate((v) => v.scrollTop), scroll0, "toggling colorblind mode never scrolls");
      await page.waitForFunction(() => document.documentElement.dataset.colorblind === "1");

      // a reload takes it from boot.js
      await page.goto("http://magpie.test/?view=usage");
      await page.locator(codexCurve).first().waitFor();
      for (const theme of ["light", "dark"]) {
        await page.evaluate((th) => { document.documentElement.dataset.theme = th; }, theme);
        got = await readCurve(page, codexCurve);
        const say = `${theme}: ${JSON.stringify(got)}`;
        assert.equal(got.cb, "1");
        assert.deepEqual(got.lines.map((l) => l.series), ["0", "1"]);
        // 5h and 7d are apart in hue (>100 deg, not two blues) and in luminance
        const hueDiff = Math.abs(got.lines[0].hue - got.lines[1].hue);
        assert.ok(Math.min(hueDiff, 360 - hueDiff) > 100, `hues too close: ${say}`);
        assert.ok(Math.abs(got.lines[0].lum - got.lines[1].lum) > 0.12, `luminance too close: ${say}`);
        // 5h solid, 7d dashed, and the short 5h window isn't washed out
        assert.equal(got.lines[0].dash, "none", say);
        assert.notEqual(got.lines[1].dash, "none", say);
        assert.ok(got.lines[0].opacity >= 0.8, `short window stays legible: ${say}`);
        // each legend key is a sample of its own line: same dash, same colour
        assert.deepEqual(got.keys.map((k) => k.series), ["0", "1"]);
        for (const [i, k] of got.keys.entries()) {
          assert.ok(!k.dot && k.sample, `key ${i} shows its line, not a dot: ${say}`);
          assert.equal(k.sampleDash, got.lines[i].dash, `key ${i}'s dash: ${say}`);
          assert.equal(k.sampleStroke, got.lines[i].stroke, `key ${i}'s colour: ${say}`);
          assert.equal(k.borders, 0, "no left border");
        }
        // the even pace is dotted, unlike any window's line or what it was
        assert.equal(got.evens.length, 1, say);
        assert.equal(got.evens[0].series, "1");
        assert.notEqual(got.evens[0].dash, plainEven, say);
        assert.ok(got.lines.every((l) => l.dash !== got.evens[0].dash), say);
        assert.ok(got.title.startsWith(w.tip), `the tooltip names the dashes and dots: ${got.title}`);
        // the Context tab's "good" grade, a colour and not a series, stays
        // apart in hue from "fair" (--c2 turns amber in this palette)
        const grades = await page.evaluate(() => Object.fromEntries(["good", "fair"].map((g) => {
          const e = document.createElement("span");
          e.className = "ctx-grade-word " + g;
          document.body.append(e);
          const [r, gr, b] = getComputedStyle(e).color.match(/\d+(\.\d+)?/g).slice(0, 3).map((v) => v / 255);
          e.remove();
          const max = Math.max(r, gr, b), d = max - Math.min(r, gr, b);
          const h = !d ? 0 : max === r ? ((gr - b) / d + (gr < b ? 6 : 0)) : max === gr ? (b - r) / d + 2 : (r - gr) / d + 4;
          return [g, h * 60];
        })));
        const gradeDiff = Math.abs(grades.good - grades.fair);
        assert.ok(Math.min(gradeDiff, 360 - gradeDiff) > 60, `good and fair too close: ${theme}: ${JSON.stringify(grades)}`);
      }

      // turned off elsewhere: the window coming back reads it, and the
      // curve drawn already goes back to dots, solid lines and its tooltip
      await page.evaluate(() => { document.documentElement.dataset.theme = "light"; });
      // no state read from before the change may land after it
      await page.waitForLoadState("networkidle");
      st.colorblind = false;
      await page.evaluate(() => window.dispatchEvent(new Event("focus")));
      await page.waitForFunction(() => !document.documentElement.dataset.colorblind);
      // the tooltip follows the mode at once, before any redraw
      const now0 = await page.evaluate((sel) => ({ cb: document.documentElement.dataset.colorblind || "", title: document.querySelector(sel).closest(".quota-curve").title }), codexCurve);
      assert.ok(now0.title.startsWith(now0.cb ? w.tip : w.solid), `tooltip of the mode it is in: ${JSON.stringify(now0)}`);
      // a state poll begun before the change may still answer with it on;
      // the next one turns it off again, so wait for the plain curve
      for (let i = 0; i < 100; i++) {
        got = await readCurve(page, codexCurve);
        if (!got.cb && got.evens[0]?.dash === plainEven && got.title.startsWith(w.solid)) break;
        await page.waitForTimeout(50);
      }
      assert.equal(got.cb, "");
      assert.ok(got.lines.every((l) => l.dash === "none"), JSON.stringify(got.lines));
      assert.ok(got.keys.every((k) => k.dot && !k.sample), JSON.stringify(got.keys));
      assert.equal(got.evens[0].dash, plainEven);
      assert.ok(got.title.startsWith(w.solid), `tooltip back to solid and dashed: ${got.title}`);

      // and Settings turns it off for good
      st.colorblind = true;
      await page.goto("http://magpie.test/?view=settings&tab=general");
      await row.waitFor();
      assert.equal(await page.evaluate(() => document.documentElement.dataset.colorblind), "1", "kept across reload via boot.js");
      assert.equal(await row.locator("#colorblindSegs .opt.on").innerText(), w.on);
      await row.locator("#colorblindSegs .opt", { hasText: w.off }).click();
      for (let i = 0; i < 50 && posts.at(-1)?.colorblind !== false; i++) await page.waitForTimeout(40);
      assert.equal(posts.at(-1)?.colorblind, false);
      await page.waitForFunction(() => !document.documentElement.dataset.colorblind);
      assert.deepEqual(errors, []);
    });
  }

  test(`${engine}: in colorblind mode seven windows have seven dashes, each in its legend key (#2073)`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const now = Date.now(), rw = now + 3 * 24 * H;
    const data = fixtures(now);
    const names = ["Weekly", "Weekly Opus", "Weekly Sonnet", "Monthly", "Weekly A", "Weekly B", "Weekly C"];
    data.quotas[0].windows = names.map((name, i) => ({ name, used: 10 + i * 5, resetsAt: iso(rw) }));
    data.history[0].lines = names.map((name, i) => ({ name, points: [90, 60, 30, 0].map((h) => ({ at: iso(now - h * H), left: 95 - i * 6 - (90 - h) / 9, start: iso(rw - 168 * H), resetsAt: iso(rw) })) }));
    const { page, errors } = await colorblindPage(browser, "en", data, true);
    await page.goto("http://magpie.test/?view=usage");
    await page.locator(codexCurve).first().waitFor();
    const got = await readCurve(page, codexCurve);
    const say = JSON.stringify(got);
    assert.deepEqual(got.lines.map((l) => l.series), ["0", "1", "2", "3", "4", "5", "6"]);
    assert.equal(new Set(got.lines.map((l) => l.dash)).size, 7, `every window its own dash: ${say}`);
    assert.equal(new Set(got.lines.map((l) => l.stroke)).size, 7, `and its own colour: ${say}`);
    for (const [i, k] of got.keys.entries()) {
      assert.ok(!k.dot && k.sample, say);
      assert.equal(k.sampleDash, got.lines[i].dash, say);
      assert.equal(k.sampleStroke, got.lines[i].stroke, say);
    }
    assert.equal(got.evens.length, 7);
    assert.equal(new Set(got.evens.map((e) => e.dash)).size, 1, "the paces share one dotted pattern");
    assert.ok(got.lines.every((l) => l.dash !== got.evens[0].dash), say);
    assert.deepEqual(errors, []);
  });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: historical estimates ignore late answers and say why one is unknown`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 440, height: 760 }, reducedMotion: "reduce" })).newPage();
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    const now = Date.now(), D = 24 * H;
    const data = fixtures(now);
    data.quotas = [data.quotas[0]];
    data.quotas[0].windows = [data.quotas[0].windows[1]];
    data.quotas[0].windows[0].holds = { tokens: 100000, priced: false, used: 30, routed: { calls: 1, tokens: 30000 } };
    const point = (day, left, end) => ({ at: iso(now + day * D), left, start: iso(now + (end - 7) * D), resetsAt: iso(now + end * D) });
    data.history = [{ provider: "codex", user: "a@x.com", lines: [{ name: "Weekly", points: [point(-17,90,-11),point(-12,40,-11),point(-10,90,-4),point(-5,30,-4),point(-3,90,3),point(-1,70,3)] }] }];
    const answer = (tokens) => [{ provider: "codex", user: "a@x.com", windows: [{ name: "Weekly", holds: { tokens, priced: false, used: 60, since: iso(now - 18 * D), routed: { calls: 2, tokens: tokens * .6 } } }] }];
    let release, requests = 0;
    data.historyHolds = (url) => {
      requests++;
      if (Math.abs(Date.parse(url.searchParams.get("to")) - (now - 4 * D)) < 1000) return new Promise((resolve) => { release = () => resolve(answer(111000)); });
      return answer(777000);
    };
    await page.route("**/*", serve("en", "light", false, data));
    await page.goto("http://magpie.test/?view=usage");
    const card = page.locator('.subscription-card[data-key="codex"]');
    const pick = card.getByRole("button", { name: "Choose a recorded cycle" });
    await pick.click();
    await page.locator(".sess-menu .pm-item").nth(1).click();
    await page.waitForFunction(() => document.querySelector(".quota-holds-why")?.textContent === "Loading…");
    await pick.click();
    await page.locator(".sess-menu .pm-item").nth(2).click();
    await page.waitForFunction(() => document.querySelector(".quota-holds")?.textContent.includes("777K"));
    assert.equal(typeof release, "function");
    const delayed = page.waitForResponse((r) => r.url().includes("/history/holds") && new URL(r.url()).searchParams.get("to") === iso(now - 4 * D));
    release();
    await delayed;
    assert.match(await card.locator(".quota-holds").textContent(), /777K/);
    await pick.click();
    await page.locator(".sess-menu .pm-item").first().click();
    assert.match(await card.locator(".quota-holds").textContent(), /100K/, "current estimate is restored");
    await pick.click();
    await page.locator(".sess-menu .pm-item").nth(1).click();
    await page.waitForFunction(() => document.querySelector(".quota-holds")?.textContent.includes("111K"));
    assert.equal(requests, 2, "completed selections reuse their own estimate");
    // a past cycle the server can't reckon says why, as a live one does
    data.historyHolds = () => [{ provider: "codex", user: "a@x.com", windows: [{ name: "Weekly", holdsWhy: "log" }] }];
    await card.locator(".quota-refresh").click();
    await page.waitForFunction(() => document.querySelector(".quota-holds-why")?.textContent === "Whole: not enough data");
    assert.match(await card.locator(".quota-holds-why").getAttribute("title"), /before magpie's request log/);
    assert.equal(await card.locator(".quota-holds").count(), 0);
    // and one with no estimate and no reason shows nothing, like a live card
    data.historyHolds = () => [];
    await card.locator(".quota-refresh").click();
    await page.waitForFunction(() => !document.querySelector(".subscription-card[data-key=codex] .quota-holds-why"));
    assert.equal(await card.locator(".quota-holds").count(), 0);
    assert.deepEqual(await page.evaluate(() => ({ page: document.documentElement.scrollWidth > innerWidth, cards: [...document.querySelectorAll(".subscription-card")].some((c) => c.scrollWidth > c.clientWidth + 1) })), { page: false, cards: false });
  });
}
