// Run with Node's test runner and Playwright on the module path; see README.md.
// The forecast on the allowance cards (#977): each window whose readings the
// backend forecast (quota_forecast.go) says whether it lasts — "Lasts to
// reset" with the multiple it leaves, "Runs out in …" with the time (one
// sentence whichever layer answered; the history marker beside it says
// which), or "Used up" — on a line of its own under its reset, after the
// Pace number; the tray card says the same, one compact line a window,
// wrapping rather than cut off at 440px. The backend's numbers are the only
// input: it sends no display strings. A window with no forecast, state
// "none", or no readings has no verdict anywhere, and its readings stay in
// the curve's legend (#802). A brief account and a past cycle picked from
// the curve's menu show none. A Sub2API key's own windows (#1004) and a
// remote magpie's gateway-key card get it too. "Every model" and back
// keeps each pool's verdict. Every language, light and
// dark, Chromium and WebKit, at 1180 and 560px and the 440px tray; no
// backend, the API is faked here.
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
const engines = process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"];
const LANGS = ["en", "zh", "ja", "de"];
const launch = (engine) => (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));

function fixtures(now) {
  const r5 = now + 2 * H, rw = now + 3 * 24 * H, rm = now + 20 * 24 * H;
  const quotas = [
    { provider: "claude", name: "Claude Code", icon: "claude-color", plan: "Max 5x", user: "ada@example.com", windows: [
      { name: "5 hours", used: 30, startsAt: iso(r5 - 5 * H), resetsAt: iso(r5) },
      { name: "7 days", used: 60, startsAt: iso(rw - 7 * 24 * H), resetsAt: iso(rw) },
      { name: "30 days", used: 40, startsAt: iso(rm - 30 * 24 * H), resetsAt: iso(rm) },
      { name: "1 day", used: 10, resetsAt: iso(r5) },
    ] },
    { provider: "gemini", name: "Gemini CLI", icon: "gemini-color", user: "bob@example.com", windows: [
      { name: "7 days", used: 100, resetsAt: iso(rw) },
      { name: "5 hours", used: 20, resetsAt: iso(r5) },
      // no readings of this one at all: it keeps its meter alone
      { name: "30 days", used: 5, resetsAt: iso(rm) },
    ] },
  ];
  const history = [
    { provider: "claude", user: "ada@example.com", lines: [
      { name: "5 hours", points: cycle(r5 - 5 * H, now, 100, 70), forecast: {
        state: "ok", source: "even", left: 70, evenLeft: 58, ahead: 12, ratePerHour: 6,
        lastsToReset: true, etaSeconds: 0, leftAtReset: 20, headroom: 1.3, cycles: 0,
        cycleStart: iso(r5 - 5 * H), resetsAt: iso(r5) } },
      { name: "7 days", points: cycle(rw - 7 * 24 * H, now, 100, 40), forecast: {
        state: "ok", source: "even", left: 40, evenLeft: 48, ahead: -8, ratePerHour: 2,
        lastsToReset: false, etaSeconds: 2.5 * H / 1000, leftAtReset: -10, headroom: 0.9, cycles: 0,
        cycleStart: iso(rw - 7 * 24 * H), resetsAt: iso(rw) } },
      { name: "30 days", points: cycle(rm - 30 * 24 * H, now, 100, 60), forecast: {
        state: "ok", source: "history", left: 60, evenLeft: 59.8, ahead: 0.2, ratePerHour: 1,
        lastsToReset: false, etaSeconds: 34 * H / 1000, leftAtReset: -5, headroom: 0.95, cycles: 3,
        cycleStart: iso(rm - 30 * 24 * H), resetsAt: iso(rm) } },
      // magpie has read this window but the backend says nothing yet
      { name: "1 day", points: cycle(r5 - 5 * H, now, 100, 90) },
    ] },
    { provider: "gemini", user: "bob@example.com", lines: [
      { name: "7 days", points: cycle(rw - 7 * 24 * H, now, 100, 0), forecast: {
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
    if (url.pathname === "/api/gateway/trace") return url.searchParams.get("wait") ? new Promise(() => {}) : json({ mine: true, now: new Date().toISOString(), seq: 0, totals: {}, routes: [] });
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json(data.quotas);
    if (url.pathname === "/api/usage/quotas/history") return json(data.history);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [], pools: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { lasts: "Lasts to reset · 1.3×", lastsHist: "Lasts to reset · 1.4×", runout: "Runs out in 2h 30m", histRunout: "Runs out in 1d 10h", spent: "Used up", hist: "history", zero: "Runs out", capped: "Lasts to reset · 10×+" },
  zh: { lasts: "撑得到重置 · 1.3× 余量", lastsHist: "撑得到重置 · 1.4× 余量", runout: "2 小时 30 分后用完", histRunout: "1 天 10 小时后用完", spent: "已用完", hist: "历史", zero: "预计用完", capped: "撑得到重置 · 10×+ 余量" },
  ja: { lasts: "リセットまで持つ · 1.3× 余裕", lastsHist: "リセットまで持つ · 1.4× 余裕", runout: "2 時間 30 分後に使い切り", histRunout: "1 日 10 時間後に使い切り", spent: "使い切り", hist: "履歴", zero: "使い切り予測", capped: "リセットまで持つ · 10×+ 余裕" },
  de: { lasts: "Reicht bis zum Reset · 1.3× Reserve", lastsHist: "Reicht bis zum Reset · 1.4× Reserve", runout: "Leer in 2 Std. 30 Min.", histRunout: "Leer in 1 T. 10 Std.", spent: "Aufgebraucht", hist: "Verlauf", zero: "Voraussichtlich leer", capped: "Reicht bis zum Reset · 10×+ Reserve" },
};

// each window of a card: whether it has a verdict, and if so its text, its
// class, whether it sits on the reset line after the Pace number, and
// whether it is cut or past its card
const windowsOf = (card) => card.locator(".quota").evaluateAll((qs) => qs.map((q) => {
  const v = q.querySelector(".quota-verdict");
  if (!v) return null;
  const c = v.closest(".subscription-card").getBoundingClientRect(), b = v.getBoundingClientRect();
  const pace = q.querySelector(".quota-reset .quota-pace");
  return {
    text: v.textContent, cls: v.className, title: v.title,
    onReset: v.parentElement.classList.contains("quota-reset"),
    afterPace: !pace || !!(pace.compareDocumentPosition(v) & Node.DOCUMENT_POSITION_FOLLOWING),
    whole: v.scrollWidth <= v.clientWidth + 1 && b.left >= c.left - 1 && b.right <= c.right + 1,
  };
}));

const noSideScroll = (page) => page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1);

for (const engine of engines) {
  for (const lang of LANGS) {
    const w = words[lang];
    test(`${engine} ${lang}: the allowance cards and the tray say what the forecast expects`, async (t) => {
      const browser = await launch(engine);
      t.after(() => browser.close());
      const errors = [];
      const data = fixtures(Date.now());
      const open = async (url, theme, viewport) => {
        const ctx = await browser.newContext({ viewport, reducedMotion: "reduce" });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(`${theme} ${viewport.width}: ${e.message}`));
        await page.route("**/*", serve(lang, theme, url.includes("mode=panel"), data));
        await page.goto(url);
        return page;
      };

      for (const [theme, width] of [["light", 1180], ["dark", 1180], ["light", 560], ["dark", 560]]) {
        const where = `${theme} ${width}px`;
        const page = await open("http://magpie.test/?view=usage", theme, { width, height: 900 });
        const card = page.locator(".subscription-card", { hasText: "Claude Code" });
        await card.locator(".quota-verdict").first().waitFor();
        const gem = page.locator(".subscription-card", { hasText: "Gemini CLI" });
        await gem.locator(".quota-verdict").first().waitFor();

        const mine = await windowsOf(card);
        assert.deepEqual(mine.map((v) => v && v.text), [w.lasts, w.runout, w.histRunout + " " + w.hist, null], where);
        assert.deepEqual(mine.map((v) => v && v.cls), ["quota-verdict k-safe", "quota-verdict k-warn", "quota-verdict k-warn", null], where);
        for (const v of mine.filter(Boolean)) {
          assert.ok(v.onReset, `${where}: "${v.text}" is on its window's reset line`);
          assert.ok(v.afterPace, `${where}: "${v.text}" comes after the Pace number`);
          assert.ok(v.whole, `${where}: "${v.text}" is cut or past its card`);
        }
        const his = await windowsOf(gem);
        assert.deepEqual(his.map((v) => v && v.text), [w.spent, w.lastsHist + " " + w.hist, null], `${where}: no verdict for a window with no readings`);
        assert.deepEqual(his.map((v) => v && v.cls), ["quota-verdict k-risk", "quota-verdict k-safe", null], where);
        // the history layer is marked, with what it means in its tooltip
        const src = card.locator(".quota-verdict .qv-src");
        assert.equal(await src.count(), 1, where);
        assert.ok((await src.getAttribute("title")).length > 10, where);
        // the colours are the theme's
        const colors = await card.locator(".quota-verdict").evaluateAll((es) => es.map((e) => getComputedStyle(e).color));
        const tok = await page.evaluate(() => ["--tk-s", "--tk-n"].map((v) => { const s = document.createElement("i"); s.style.color = `var(${v})`; document.body.append(s); const c = getComputedStyle(s).color; s.remove(); return c; }));
        assert.deepEqual(colors, [tok[0], tok[1], tok[1]], `${where}: verdict colours`);
        // the curve and its readings (#802) are main's, untouched beside it
        assert.equal(await card.locator(".quota-curve .qc-key").count(), 4, `${where}: every read window keeps its key`);
        assert.equal(await noSideScroll(page), true, `${where}: the page scrolls sideways`);

        if (theme === "light" && width === 1180) {
          // the curves off, the verdicts stay: they are the window's, not the curve's
          await page.locator("#quotaHead #quotaTrend").click();
          await page.locator(".sess-menu .pm-item").first().click();
          await page.waitForFunction(() => !document.querySelector(".quota-curve"));
          assert.equal(await card.locator(".quota-verdict").count(), 3, "the verdicts stay with the curves off");
          await page.locator("#quotaHead #quotaTrend").click();
          await page.locator(".sess-menu .pm-item").last().click();
          await card.locator(".quota-curve").waitFor();
          assert.equal(await card.locator(".quota-verdict").count(), 3);
        }
        await page.context().close();
      }

      // the tray panel: the same verdict, one compact line a window. The
      // tray is narrow and the German line long, so it wraps rather than
      // being cut off; measured in both themes at the tray's 440px and, so
      // the wrap itself is tried, at 260px, without scrolling a card into view.
      for (const [theme, width] of [["dark", 440], ["light", 440], ["light", 260]]) {
        const panel = await open("http://magpie.test/?mode=panel", theme, { width, height: 800 });
        await panel.locator('#ptabs [data-ptab="usage"]').click();
        const pcard = panel.locator(".pq-card", { hasText: "ada@example.com" });
        await pcard.locator(".pq-verdict").first().waitFor();
        assert.deepEqual(await pcard.locator(".pq-verdict > b").allTextContents(), ["5h", "7d", "30d"], theme);
        assert.deepEqual(await pcard.locator(".pq-verdict > span").allTextContents(), [w.lasts, w.runout, w.histRunout + " " + w.hist], theme);
        assert.deepEqual(await pcard.locator(".pq-verdict").evaluateAll((es) => es.map((e) => e.className)),
          ["pq-verdict k-safe", "pq-verdict k-warn", "pq-verdict k-warn"], theme);
        const bcard = panel.locator(".pq-card", { hasText: "bob@example.com" });
        assert.deepEqual(await bcard.locator(".pq-verdict > span").allTextContents(), [w.spent, w.lastsHist + " " + w.hist], `${theme}: no line for the window it has no readings of`);
        assert.deepEqual(await bcard.locator(".pq-verdict > b").allTextContents(), ["7d", "5h"], theme);
        const overflow = await panel.locator(".pq-card").evaluateAll((cards) => {
          const out = [];
          for (const card of cards) {
            const cb = card.getBoundingClientRect();
            for (const v of card.querySelectorAll(".pq-verdict")) {
              const vb = v.getBoundingClientRect(), label = v.querySelector(":scope > b"), text = v.querySelector(":scope > span");
              if (text.scrollWidth > text.clientWidth + 1 || text.scrollHeight > text.clientHeight + 1) out.push("clipped: " + text.textContent);
              for (const [what, b] of [["verdict", vb], ["label", label.getBoundingClientRect()], ["text", text.getBoundingClientRect()]]) {
                if (b.left < cb.left - 1 || b.right > cb.right + 1) out.push(`${what} past card edge: ${v.textContent}`);
              }
            }
          }
          return out;
        });
        assert.deepEqual(overflow, [], `${theme} ${width}px: no tray verdict is clipped or leaves its card`);
        // (narrower than the tray ever is, the page itself scrolls already)
        if (width === 440) assert.equal(await noSideScroll(panel), true, `${theme} ${width}px: the tray scrolls sideways`);
        await panel.context().close();
      }
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a run-out with no time to say, a headroom past ten, and none where nothing is projected`, async (t) => {
      const browser = await launch(engine);
      t.after(() => browser.close());
      const now = Date.now(), r5 = now + 2 * H, reset = now + 2 * H;
      const names = ["Untouched", "Unknown cycle", "One reading", "Young cycle"];
      const data = {
        quotas: [
          { provider: "claude", name: "Claude Code", icon: "claude-color", user: "ada@example.com", windows: [
            { name: "5 hours", used: 96, resetsAt: iso(r5) },
            { name: "1 day", used: 1, resetsAt: iso(now + 5 * 60e3) },
          ] },
          { provider: "codex", name: "Codex", user: "idle@example.com", windows: names.map((name) => ({ name, used: 0, resetsAt: iso(reset) })) },
        ],
        history: [
          { provider: "claude", user: "ada@example.com", lines: [
            // a crossing already passed: it still says it runs out, never "Used up"
            { name: "5 hours", points: cycle(r5 - 5 * H, now - 90 * 60e3, 100, 40), forecast: {
              state: "ok", source: "even", left: 40, evenLeft: 30, ahead: -26, ratePerHour: 20, lastsToReset: false,
              etaSeconds: 0, asOf: iso(now - 90 * 60e3), runsOutAt: iso(now - 10 * 60e3), leftAtReset: -30, headroom: 0.1, cycles: 0, cycleStart: iso(r5 - 5 * H), resetsAt: iso(r5) } },
            { name: "1 day", points: cycle(now - 24 * H, now, 100, 99.9), forecast: {
              state: "ok", source: "history", left: 99.9, evenLeft: 0.4, ahead: 99, ratePerHour: 0.1, lastsToReset: true,
              etaSeconds: 0, leftAtReset: 99, headroom: 240.5, cycles: 0, cycleStart: iso(now - 24 * H), resetsAt: iso(now + 5 * 60e3) } },
          ] },
          // many readings all at 100%, readings with no cycle to place them,
          // one reading, a cycle too young: observations, no verdict
          { provider: "codex", user: "idle@example.com", lines: [
            { name: names[0], points: Array.from({ length: 37 }, (_, i) => ({ at: iso(now - (36 - i) * 60000), left: 100, start: iso(now - 3 * H), resetsAt: iso(reset) })), forecast: { state: "none", left: 100, cycleStart: iso(now - 3 * H), resetsAt: iso(reset) } },
            { name: names[1], points: Array.from({ length: 15 }, (_, i) => ({ at: iso(now - (14 - i) * 60000), left: 85 - i })) },
            { name: names[2], points: [{ at: iso(now), left: 80 }] },
            { name: names[3], points: cycle(now - 12 * 60000, now, 100, 99), forecast: { state: "none", left: 99, cycleStart: iso(now - 12 * 60000), resetsAt: iso(reset) } },
          ] },
        ],
      };
      for (const panel of [false, true]) {
        const ctx = await browser.newContext({ viewport: { width: panel ? 440 : 1000, height: 800 }, reducedMotion: "reduce" });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, "dark", panel, data));
        await page.goto("http://magpie.test/" + (panel ? "?mode=panel" : "?view=usage"));
        if (panel) await page.locator('#ptabs [data-ptab="usage"]').click();
        const sel = panel ? ".pq-verdict > span" : ".quota-verdict";
        const card = page.locator(panel ? ".pq-card" : ".subscription-card", { hasText: panel ? "ada@example.com" : "Claude Code" });
        await card.locator(sel).first().waitFor();
        assert.deepEqual(await card.locator(sel).allTextContents(), [words[lang].zero, words[lang].capped + " " + words[lang].hist], `panel ${panel}`);
        const idle = page.locator(panel ? ".pq-card" : ".subscription-card", { hasText: panel ? "idle@example.com" : "Codex" });
        await idle.waitFor();
        assert.equal(await idle.locator(".quota-verdict, .pq-verdict").count(), 0, `panel ${panel}: no verdict when there is no projection, whatever the reason`);
        if (!panel) assert.equal(await idle.locator(".quota-curve .qc-key").count(), 4, "every observed window keeps its readings in the legend");
        assert.deepEqual(errors, [], `panel ${panel}`);
        await ctx.close();
      }
    });
  }

  // brief accounts keep their bars alone, and a past cycle picked from the
  // curve's menu says nothing of the current one
  test(`${engine}: no verdict in brief or on a past cycle`, async (t) => {
    const browser = await launch(engine);
    t.after(() => browser.close());
    const now = Date.now();
    const two = fixtures(now);
    // a second Claude account, in brief beside the first
    two.quotas.splice(1, 0, { ...two.quotas[0], user: "zed@example.com", lastServedAt: undefined });
    two.quotas[0].lastServedAt = iso(now);
    two.history.push({ ...two.history[0], user: "zed@example.com" });
    // a completed cycle before the current month's, for the curve's menu
    // (the longest window owns it)
    const month = two.history[0].lines[2], start = Date.parse(month.points[0].start);
    month.points = [...cycle(start - 30 * 24 * H, start, 100, 10), ...month.points];
    const ctx = await browser.newContext({ viewport: { width: 1180, height: 900 }, reducedMotion: "reduce" });
    const page = await ctx.newPage();
    page.setDefaultTimeout(5000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", serve("en", "light", false, two));
    await page.goto("http://magpie.test/?view=usage");
    const card = page.locator(".subscription-card", { hasText: "Claude Code" });
    await card.locator(".quota-verdict").first().waitFor();
    assert.equal(await card.locator(".quota-windows.brief").count(), 1, "the second account is in brief");
    assert.equal(await card.locator(".quota-windows.brief .quota-verdict:visible").count(), 0, "no verdict shows in brief");
    assert.equal(await card.locator(".quota-windows:not(.brief) .quota-verdict").count(), 3);
    const range = card.locator(".quota-curve .qc-range").first();
    assert.equal(await range.isDisabled(), false, "a recorded cycle to pick");
    await range.click();
    await page.locator(".sess-menu .pm-item").nth(1).click();
    await page.waitForFunction(() => document.querySelector(".quota-curve .qc-range")?.textContent.startsWith("Past cycle"));
    assert.equal(await card.locator(".quota-windows:not(.brief) .quota-verdict").count(), 0, "a past cycle has no forecast");
    assert.deepEqual(errors, []);
    await ctx.close();
  });

  // "Every model" and back rebuilds the card's windows: a pool's window says
  // its verdict from its own line before and after, and the models' own
  // windows, which magpie keeps no line of, say none
  for (const lang of ["en", "de"]) {
    test(`${engine} ${lang}: the verdict follows the Every model toggle`, async (t) => {
      const browser = await launch(engine);
      t.after(() => browser.close());
      const now = Date.now(), r5 = now + 2 * H, rw = now + 5 * 24 * H;
      const model = (name, family, pool, used, end) => ({ name, family, pool, used, resetsAt: iso(end) });
      const pool = (pool, name, used, end) => ({ name, pool, used, resetsAt: iso(end) });
      const line = (name, start, end, to, f) => ({ name, points: cycle(start, now, 100, to), forecast: { state: "ok", source: "even", cycles: 0, cycleStart: iso(start), resetsAt: iso(end), ...f } });
      const data = {
        quotas: [{ provider: "antigravity", name: "Antigravity", icon: "antigravity-color", user: "ada@example.com", plan: "Google AI Pro", windows: [
          model("Claude Sonnet 4.6", "Claude", "Claude & GPT", 30, r5), model("GPT-OSS 120B (Medium)", "GPT-OSS", "Claude & GPT", 30, r5),
          model("Gemini 3 Flash", "Gemini", "Gemini", 20, r5), model("Gemini 3.1 Pro (High)", "Gemini", "Gemini", 20, r5),
          pool("Gemini", "7 days", 25, rw), pool("Gemini", "5 hours", 20, r5),
          pool("Claude & GPT", "7 days", 60, rw), pool("Claude & GPT", "5 hours", 30, r5),
        ] }],
        history: [{ provider: "antigravity", user: "ada@example.com", lines: [
          line("Gemini · 5 hours", r5 - 5 * H, r5, 80, { lastsToReset: true, headroom: 1.3 }),
          line("Gemini · 7 days", rw - 7 * 24 * H, rw, 75, { lastsToReset: true, headroom: 2 }),
          line("Claude & GPT · 5 hours", r5 - 5 * H, r5, 70, { lastsToReset: true, headroom: 1.1 }),
          line("Claude & GPT · 7 days", rw - 7 * 24 * H, rw, 40, { lastsToReset: false, etaSeconds: 2.5 * H / 1000 }),
        ] }],
      };
      const ctx = await browser.newContext({ viewport: { width: 560, height: 900 }, reducedMotion: "reduce" });
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, "light", false, data));
      await page.goto("http://magpie.test/?view=usage");
      const card = page.locator(".subscription-card", { hasText: "Antigravity" });
      await card.locator(".quota-verdict").first().waitFor();
      const pools = async () => (await windowsOf(card)).map((v) => v && v.cls);
      const grouped = ["quota-verdict k-safe", "quota-verdict k-safe", "quota-verdict k-safe", "quota-verdict k-warn"];
      assert.deepEqual(await pools(), grouped, "each pool's window, 5 hours then 7 days");
      await card.locator(".quota-every").click();
      await page.waitForFunction(() => document.querySelector(".quota-every")?.getAttribute("aria-expanded") === "true");
      assert.equal(await card.locator(".quota-verdict").count(), 0, "the models' own windows have no line, so no verdict");
      await card.locator(".quota-every").click();
      await page.waitForFunction(() => document.querySelector(".quota-every")?.getAttribute("aria-expanded") === "false");
      assert.deepEqual(await pools(), grouped, "back by group, each pool's verdict again");
      assert.ok((await windowsOf(card)).every((v) => v.onReset && v.whole), "on the reset line, not cut, at 560px");
      assert.ok(await noSideScroll(page));
      assert.deepEqual(errors, []);
      await ctx.close();
    });
  }

  // #1004: a sub2api key's own 5-hour, day and 7-day windows, and a remote
  // magpie's gateway-key card (#1547), get the verdict from their own lines
  for (const lang of ["en", "de"]) {
    test(`${engine} ${lang}: a key's own windows carry the forecast`, async (t) => {
      const browser = await launch(engine);
      t.after(() => browser.close());
      const now = Date.now();
      const r5 = now + 2 * H, rd = now + 20 * H, rw = now + 5 * 24 * H;
      const usd = (amount, limit) => ({ amount, limit, unit: "USD" });
      const ok = (o) => ({ state: "ok", cycles: 0, ...o });
      const data = {
        quotas: [
          { provider: "sub2api", name: "Sub2API", windows: [
            { name: "5 hours", used: 30, resetsAt: iso(r5), ...usd(90, 300) },
            { name: "1 day", used: 55, resetsAt: iso(rd), ...usd(165, 300) },
            { name: "7 days", used: 60, resetsAt: iso(rw), ...usd(480, 800) },
          ] },
          { provider: "office/~keys", name: "Office", icon: "magpie", from: "Office", kind: "plan", user: "laptop",
            gatewayKey: { limited: true, period: "day", calls: 3, costLeft: 1.54 },
            windows: [{ name: "Spend today", used: 70, amount: 1.4, limit: 2, unit: "USD", resetsAt: iso(rd) }] },
        ],
        history: [
          { provider: "sub2api", user: "", lines: [
            { name: "5 hours", points: cycle(r5 - 5 * H, now, 100, 70), forecast: ok({ source: "even", lastsToReset: true, headroom: 1.3, cycleStart: iso(r5 - 5 * H), resetsAt: iso(r5) }) },
            { name: "1 day", points: cycle(rd - 24 * H, now, 100, 45), forecast: ok({ source: "even", lastsToReset: false, etaSeconds: 6 * H / 1000, cycleStart: iso(rd - 24 * H), resetsAt: iso(rd) }) },
            // a week blends its completed cycles: the history marker
            { name: "7 days", points: cycle(rw - 7 * 24 * H, now, 100, 40), forecast: ok({ source: "history", lastsToReset: false, etaSeconds: 3 * 24 * H / 1000, cycles: 3, cycleStart: iso(rw - 7 * 24 * H), resetsAt: iso(rw) }) },
          ] },
          { provider: "office/~keys", user: "laptop", lines: [
            { name: "Spend today", points: cycle(rd - 24 * H, now, 100, 30), forecast: ok({ source: "even", lastsToReset: false, etaSeconds: 2 * H / 1000, cycleStart: iso(rd - 24 * H), resetsAt: iso(rd) }) },
          ] },
        ],
      };
      const ctx = await browser.newContext({ viewport: { width: 1180, height: 900 }, reducedMotion: "reduce" });
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, "light", false, data));
      await page.goto("http://magpie.test/?view=usage");
      const card = page.locator(".subscription-card", { hasText: "Sub2API" });
      await card.locator(".quota-verdict").first().waitFor();
      assert.deepEqual((await windowsOf(card)).map((v) => v && v.cls), ["quota-verdict k-safe", "quota-verdict k-warn", "quota-verdict k-warn"], "the forecast reaches a key's windows");
      assert.equal(await card.locator(".quota-verdict").nth(2).locator(".qv-src").textContent(), words[lang].hist, "the week's line carries the history marker");
      const keys = page.locator(".subscription-card", { hasText: "Office" });
      await keys.locator(".quota-verdict").waitFor();
      assert.deepEqual(await keys.locator(".quota-verdict").allTextContents(), [{ en: "Runs out in 2h", de: "Leer in 2 Std." }[lang]], "a gateway key's window");
      assert.equal(await keys.locator(".quota-keyline").count(), 1, "the key's own line stays under its windows");
      assert.deepEqual(errors, []);
      await ctx.close();
    });
  }
}
