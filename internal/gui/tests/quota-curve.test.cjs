// Run with Node's test runner and Playwright on the module path; see README.md.
// What was left over time (#651), as the forecast redesign draws it: each
// window of an account's card carries its own burn-down — what magpie's
// readings say of its current cycle, with the soft fill under the line, the
// dashed even burn and the upright line at now — under a header and above its
// meter, rather than one combined curve under the meters. The range (or Off)
// is still picked once, in the allowances' head, and turns every card's plots
// at once; the legend moved there too, so it stands once rather than on every
// card. "2 days" draws the last two days of the window, "Cycle" its current
// cycle; Off draws no plot on any card, the tray's thin line included; the
// pick is remembered across a reload, and no click moves the page. An account
// with no readings has no plot, and no header either; an account in brief
// builds neither (its CSS used to hide them). Nothing is drawn past a plot's
// viewBox, and the plots' observer watches the one container the cards are
// rebuilt in rather than each plot. Several accounts on a card are set apart
// by a rule. The tray's card has a thin line of its first window's cycle.
// The fixtures serve a complete /api/usage body, as the handler returns it.
// English and Chinese, light and dark; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const H = 3600e3;
const iso = (ms) => new Date(ms).toISOString();

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
  const r5 = now + 2 * H, rw = now + 3 * 24 * H;
  const quotas = [
    { provider: "codex", name: "Codex", icon: "openai", plan: "Plus", user: "a@x.com", windows: [
      { name: "5 hours", used: 40, resetsAt: iso(r5) },
      { name: "Weekly", used: 30, resetsAt: iso(rw) },
    ] },
    { provider: "codex", name: "Codex", icon: "openai", plan: "Pro", user: "b@x.com", windows: [
      { name: "5 hours", used: 10, resetsAt: iso(r5) },
    ] },
    { provider: "kimi", name: "Kimi", icon: "kimi", windows: [{ name: "Weekly", used: 10, resetsAt: iso(rw) }] },
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
    // a page of this origin, only to set what the tray's page then reads
    if (url.pathname === "/blank") return route.fulfill({ contentType: "text/html", body: "<!doctype html><title>blank</title>" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { two: "2 days", cycle: "Cycle", off: "Off", trend: (r) => "Trends: " + r, five: "5 hours", week: "Weekly", legend: ["Actual left", "Even burn", "Projected at this rate"] },
  zh: { two: "2 天", cycle: "本周期", off: "关闭", trend: (r) => "走势：" + r, five: "5 小时", week: "每周", legend: ["实际剩余", "匀速消耗", "按当前速度预测"] },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: every window draws what was left over its own cycle`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
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
      const open = async (name, url, theme, viewport) => {
        const page = await (await browser.newContext({ viewport, reducedMotion: "reduce" })).newPage();
        pages.push([name, page]);
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        page.on("console", (m) => { if (m.type() === "error") errors.push("console: " + m.text()); });
        // what app.js observes, so a refresh that rebuilt a plot cannot
        // quietly leave the old one (and its tree) observed
        await page.addInitScript(() => {
          const seen = new Set();
          const RO = window.ResizeObserver;
          window.ResizeObserver = class extends RO {
            observe(target, opts) { seen.add(target); return super.observe(target, opts); }
            unobserve(target) { seen.delete(target); return super.unobserve(target); }
          };
          window.__observed = () => ({
            n: seen.size,
            detached: [...seen].filter((e) => !e.isConnected).length,
            plots: [...seen].filter((e) => e.classList?.contains("quota-plot")).length,
            windows: [...seen].filter((e) => e.classList?.contains("quota-windows")).length,
            who: [...seen].filter((e) => !e.isConnected).map((e) => e.tagName + "." + e.className),
          });
        });
        await page.route("**/*", serve(lang, theme, url.includes("mode=panel"), data));
        await page.goto(url);
        return page;
      };

      const strokes = {};
      for (const theme of ["light", "dark"]) {
        const page = await open(theme, "http://magpie.test/?view=usage", theme, { width: 900, height: 620 });
        const card = page.locator(".subscription-card", { hasText: "Codex" });
        // the account in full: a burn-down under its header, above its meter
        const box = card.locator(".quota-windows").first();
        const plots = box.locator(".quota-plot");
        await plots.first().waitFor();
        assert.equal(await box.locator(".qv-row").count(), 2, "a header a window");
        assert.deepEqual(await box.locator(".qv-badge").allTextContents(), [w.five, w.week]);
        assert.deepEqual(await box.locator(".qv-text").allTextContents(), [], "this fixture has no forecasts and no verdicts");
        assert.equal(await plots.nth(0).locator("path.qc-line").getAttribute("data-name"), "5 hours");
        assert.equal(await card.locator(".quota-curve").count(), 0, "the card's one combined curve is gone");
        assert.equal(await page.locator(".quota-windows .qc-legend").count(), 0, "no legend on a card");
        // the range is picked once, in the allowances' head, not on every card
        const pick = page.locator("#quotaHead #quotaTrend");
        assert.equal(await pick.textContent(), w.trend(w.cycle), "Cycle first");
        assert.equal(await page.locator(".quota-plot .segs, .quota-plot button, .quota-plot select").count(), 0, "no control on a card");
        // one legend there, beside it, with the plot's three marks
        const legend = page.locator("#quotaHead #quotaLegend");
        assert.equal(await legend.count(), 1);
        assert.equal(await legend.locator(".qc-key").count(), 3);
        assert.deepEqual(await legend.locator(".qc-key").allTextContents(), w.legend);
        // the 5-hour window's own cycle, not the week's: one line, its even
        // burn, the upright now, the dot for now, no forecast to project
        const five = await plots.nth(0).locator('path.qc-line[data-name="5 hours"]').getAttribute("d");
        assert.equal((five.match(/M/g) || []).length, 1, "this window's current cycle only: " + five);
        assert.equal(await plots.nth(0).locator("line.qc-even").count(), 1);
        assert.equal(await plots.nth(0).locator("line.qc-grid").count(), 3, "the 0/50/100 grid");
        assert.equal(await plots.nth(0).locator("line.qc-now").count(), 1);
        assert.equal(await plots.nth(0).locator("circle.qc-dot").count(), 1);
        assert.equal(await plots.nth(0).locator("path.qc-area").count(), 1, "the soft fill under the line");
        assert.equal(await box.locator("path.qc-proj").count(), 0, "no forecast, no projection");
        assert.equal(await page.locator(".subscription-card", { hasText: "Kimi" }).locator(".quota-plot").count(), 0, "no readings, no plot");
        assert.equal(await page.locator(".subscription-card", { hasText: "Kimi" }).locator(".qv-row").count(), 0, "and no header");
        assert.equal(await page.locator(".qc-line.qc-short").count(), 0, "a window is drawn over its own cycle now, never faint");
        // the other account, in brief, keeps its bars alone: neither a header
        // nor a plot is built for it (the CSS only hid them before)
        const brief = card.locator(".quota-windows.brief");
        assert.equal(await brief.count(), 1);
        assert.equal(await brief.locator(".quota-plot").count(), 0, "a brief account builds no curve");
        assert.equal(await brief.locator(".qv-row").count(), 0, "nor a header");
        assert.equal(await brief.locator(".quota-labels").first().isVisible(), true);
        // The current upstream's click/keyboard enlargement and reading
        // tooltip survive the move from one account plot to each window.
        const svg = plots.first().locator("svg");
        const small = (await svg.boundingBox()).height;
        assert.match(await plots.first().getAttribute("title"), /[0-9]+%/);
        const scroll = await page.evaluate(() => document.scrollingElement.scrollTop);
        await svg.click();
        assert.equal(await svg.getAttribute("aria-expanded"), "true");
        assert.ok((await svg.boundingBox()).height > small);
        await svg.press("Enter");
        assert.equal(await svg.getAttribute("aria-expanded"), "false");
        assert.equal((await svg.boundingBox()).height, small);
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), scroll);
        await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
        // the colours are the theme's chart colours, nothing outside the card
        strokes[theme] = await plots.first().locator("path.qc-line").evaluate((e) => getComputedStyle(e).stroke);
        const c1 = await page.evaluate(() => { const s = document.createElement("i"); s.style.color = "var(--c1)"; document.body.append(s); const c = getComputedStyle(s).color; s.remove(); return c; });
        assert.equal(strokes[theme], c1);
        const cb = await card.boundingBox(), pb = await plots.first().boundingBox();
        assert.ok(pb.x >= cb.x && pb.x + pb.width <= cb.x + cb.width + 0.5, "the plot fits its card");
        assert.ok(pb.width > cb.width * 0.6, `the plot is the card's width: ${JSON.stringify(pb)} of ${JSON.stringify(cb)}`);
        const left = await box.evaluate((e) => [...e.querySelectorAll("*")].filter((x) => parseFloat(getComputedStyle(x).borderLeftWidth) > 0).length);
        assert.equal(left, 0, "no left borders");

        if (theme === "light") {
          // several accounts: the next one is set apart from the one before
          await card.locator(".quota-more").click();
          const second = card.locator(".subscription-account", { hasText: "b@x.com" });
          // Auto-use and Use credits close the first Codex account's section.
          assert.deepEqual(await second.evaluate((e) => [e.previousElementSibling.previousElementSibling.previousElementSibling.className, e.previousElementSibling.previousElementSibling.className, e.previousElementSibling.className]), ["quota-windows", "quota-autoreset", "quota-credits on"]);
          assert.notEqual(await second.evaluate((e) => getComputedStyle(e).borderTopStyle), "none", "a rule between the accounts");
          const choose = async (name) => {
            await pick.click();
            const items = page.locator(".sess-menu .pm-item");
            assert.deepEqual(await items.locator(".pm-name").allTextContents(), [w.off, w.two, w.cycle]);
            await items.filter({ hasText: name }).click();
          };
          // 2 days: the range turns, the page doesn't move, the pick is kept
          const scroller = await page.evaluate(() => { const s = document.scrollingElement; s.scrollTop = 40; return s.scrollTop; });
          const before = await plots.first().locator(".qc-axis").textContent();
          await choose(w.two);
          assert.equal(await pick.textContent(), w.trend(w.two));
          assert.notEqual(await plots.first().locator(".qc-axis").textContent(), before);
          assert.notEqual(await plots.first().locator('path.qc-line[data-name="5 hours"]').getAttribute("d"), five);
          assert.equal(await box.locator("line.qc-even").count(), 2, "over two days each window has its even burn");
          assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), scroller, "the click moved the page");
          assert.equal(await page.evaluate(() => localStorage.getItem("magpie.quotaRange")), "2d");
          // Off: no card draws a plot or a legend, the pick stays where it
          // was, kept across a reload; back on, the plots come back
          const at = (await pick.boundingBox()).y;
          await choose(w.off);
          assert.equal(await page.locator(".quota-plot").count(), 0);
          assert.equal(await legend.isVisible(), false, "no curves, no legend");
          assert.equal(await pick.textContent(), w.trend(w.off));
          assert.equal((await pick.boundingBox()).y, at, "the pick moved");
          assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), scroller, "the click moved the page");
          assert.equal(await page.evaluate(() => localStorage.getItem("magpie.quotaRange")), "off");
          await page.reload();
          await page.locator(".subscription-card", { hasText: "Codex" }).locator(".quota-windows").first().waitFor();
          assert.equal(await page.locator(".quota-plot").count(), 0, "Off is kept across a restart");
          assert.equal(await pick.textContent(), w.trend(w.off));
          await choose(w.cycle);
          await page.locator(".quota-plot").first().waitFor();
          assert.equal(await page.evaluate(() => localStorage.getItem("magpie.quotaRange")), "cycle");
          // every refresh makes the cards anew: the observers keep the one
          // container, so nothing they hold is detached, and three refreshes
          // leave the count where it was (per-plot observers grew it, a plot's
          // whole tree each, for as long as the window lived)
          const observed = await page.evaluate(() => window.__observed());
          assert.equal(observed.plots, 0, "a plot observed one by one");
          assert.equal(observed.windows, 0, "a windows row observed one by one");
          for (const name of [w.off, w.cycle, w.off, w.cycle, w.off, w.cycle]) await choose(name);
          const after = await page.evaluate(() => window.__observed());
          const held = ({ n, plots, windows }) => ({ n, plots, windows });
          assert.deepEqual(held(after), held(observed),
            "a refresh left an observer target behind: before " + JSON.stringify(observed.who) + ", after " + JSON.stringify(after.who));
          // and the page read its usage without the thrown toast
          assert.equal((await page.locator(".status.err").allTextContents()).join(""), "");
        }
      }
      assert.notEqual(strokes.light, strokes.dark, "dark has its own line colour");
      if (lang === "en") {
        const wallPage = pages.find(([name]) => name === "light")[1];
        const original = data.quotas;
        const base = original[0];
        await wallPage.setViewportSize({ width: 1180, height: 620 });
        for (const n of [1, 2, 3, 5, 6]) {
          data.quotas = Array.from({ length: n }, (_, i) => ({ ...base, provider: "wall-" + i, name: "Card " + i }));
          await wallPage.reload();
          await wallPage.locator(".subscription-card").first().waitFor();
          const columns = () => wallPage.locator("#subscriptionUsage").evaluate((e) => getComputedStyle(e).gridTemplateColumns.split(" ").filter((v) => parseFloat(v) > 0).length);
          assert.equal(await wallPage.locator(".subscription-card").count(), n);
          assert.equal(await columns(), Math.min(n, n === 6 ? 3 : 4), `card count ${n}`);
          // Zoom and a narrow resize keep every card inside its own wall.
          for (const zoom of [0.8, 1.5]) {
            await wallPage.evaluate((v) => document.body.style.zoom = v, zoom);
            await wallPage.setViewportSize({ width: 900, height: 620 });
            const inside = await wallPage.locator("#subscriptionUsage").evaluate((e) => {
              const r = e.getBoundingClientRect();
              return [...e.children].every((c) => { const b = c.getBoundingClientRect(); return b.x >= r.x - 1 && b.right <= r.right + 1; });
            });
            assert.ok(inside, `cards fit at ${n} cards, zoom ${zoom}`);
          }
          await wallPage.evaluate(() => document.body.style.zoom = "");
          await wallPage.setViewportSize({ width: 1180, height: 620 });
        }
        data.quotas = original;
      }

      // the tray: a thin line of the 5-hour window's cycle, across the card
      const panel = await open("panel", "http://magpie.test/?mode=panel", "dark", { width: 440, height: 640 });
      await panel.locator('#ptabs [data-ptab="usage"]').click();
      const pcard = panel.locator(".pq-card", { hasText: "a@x.com" });
      const spark = pcard.locator(".pq-spark");
      await spark.waitFor();
      assert.equal(await spark.locator("path.qc-line").count(), 1);
      assert.equal((((await spark.locator("path.qc-line").getAttribute("d")).match(/M/g)) || []).length, 1, "the current cycle only");
      assert.equal(await spark.locator("line.qc-even").count(), 1);
      const sb = await spark.boundingBox(), cb = await pcard.boundingBox();
      assert.ok(sb.height <= 17 && sb.width > cb.width * 0.7, `thin and across the card: ${JSON.stringify(sb)}`);
      assert.equal(await panel.locator(".pq-card", { hasText: "Kimi" }).locator(".pq-spark").count(), 0);
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
