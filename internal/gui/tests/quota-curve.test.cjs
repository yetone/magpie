// Run with Node's test runner and Playwright on the module path; see README.md.
// What was left over time (#651): under an account's meters on the Usage
// page, a line a window from magpie's readings — broken where the 5-hour
// window started again — a dashed even burn from each window's start to its
// reset, and an upright line at now. One pick in the allowances' head, the
// app's own menu, turns every card at once: "2 days" / "Cycle", or "Off"
// (ARNO on Discord: the curves made the cards cluttered), which draws no
// curve on any card, the tray's thin line included; the pick is remembered
// across a reload, and no click moves the page. Several accounts on a card
// are set apart by a rule, a curve's legend before the next one too. An
// account with no readings has no curve. The tray's card has a thin line of
// its first window's cycle.
// English and Chinese, light and dark; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
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
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { head: "Left over time", two: "2 days", cycle: "Cycle", off: "Off", trend: (r) => "Trends: " + r, five: "5 hours", week: "Weekly", dashes: /Dashed: an even pace/ },
  zh: { head: "剩余额度走势", two: "2 天", cycle: "本周期", off: "关闭", trend: (r) => "走势：" + r, five: "5 小时", week: "每周", dashes: /虚线：匀速用量/ },
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
      const open = async (name, url, theme, viewport) => {
        const page = await (await browser.newContext({ viewport, reducedMotion: "reduce" })).newPage();
        pages.push([name, page]);
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, theme, url.includes("mode=panel"), data));
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
        assert.equal(await page.locator(".quota-curve .segs, .quota-curve button, .quota-curve select").count(), 0, "no control on a card");
        assert.equal(await curve.locator(".qc-range").textContent(), w.cycle);
        // a line a window, now; the 5-hour window, a comb over the week's
        // cycle, faint and without its upright even burn
        assert.equal(await curve.locator("path.qc-line").count(), 2);
        assert.equal(await curve.locator("line.qc-even").count(), 1);
        assert.match(await curve.getAttribute("title"), w.dashes, "hovering the plot says what the dashes are");
        assert.equal(await curve.locator("path.qc-line.qc-short").getAttribute("data-name"), "5 hours");
        assert.equal(await curve.locator("line.qc-now").count(), 1);
        const five = await curve.locator('path.qc-line[data-name="5 hours"]').getAttribute("d");
        assert.equal((five.match(/M/g) || []).length, 2, "the 5-hour line breaks where its window started again: " + five);
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
          assert.deepEqual(await second.evaluate((e) => [e.previousElementSibling.previousElementSibling.previousElementSibling.className, e.previousElementSibling.previousElementSibling.className, e.previousElementSibling.className]), ["quota-curve", "quota-autoreset", "quota-credits on"]);
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
