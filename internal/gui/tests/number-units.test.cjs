// Run with Node's test runner and Playwright on the module path; see README.md.
// Number units (John on Discord): in Chinese a large count is said in 万 and
// 亿 ("15.4 亿", an axis's "8000 万") unless Settings' Number units picks K /
// M / B, which shortens it as English does ("1.54B", "80M") while the rest
// stays Chinese — every count on the Usage page (#476: the Overview, the
// Requests tab's totals, chart axis and summary line, the Sessions tab) and
// in the tray panel (its Usage tab, Routing's "today"), the axes' labels
// still ending before the plot starts. The row is
// Chinese's alone: in English it is hidden and counts are K / M / B anyway.
// Picking it saves westernUnits, never scrolls the settings page, and holds
// after a reload. Chromium and WebKit; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const midnight = new Date();
midnight.setHours(0, 0, 0, 0);

// a day by the hour of one provider: 7000 万 (70M) tokens an hour, from 2am
function ledger() {
  const series = Array.from({ length: 24 }, (_, h) => {
    const on = h >= 2 ? 1 : 0;
    const part = { calls: 900 * on, tokens: 7e7 * on, cost: 1.2 * on };
    const by = { provider: { zcode: part }, agent: { claude: part }, model: { "glm-5": part } };
    return { label: String(h), time: new Date(midnight.getTime() + h * 3600e3).toISOString(), calls: part.calls, errors: 0,
      input: part.tokens * 0.01, output: part.tokens * 0.005, cache_write: part.tokens * 0.05, cache_read: part.tokens * 0.935, cost: part.cost, by };
  });
  const sum = (k) => series.reduce((a, p) => a + p[k], 0);
  const tot = { calls: sum("calls"), errors: 0, input: sum("input"), output: sum("output"), cache_write: sum("cache_write"), cache_read: sum("cache_read"), reasoning: 0, cost: sum("cost"), unpriced: 0 };
  const share = (id, name, icon) => ({ id, name, icon, ...tot });
  return {
    period: "today", rows: [], offset: 0, total: 1, ...tot, bucket: "hour", series,
    by: { provider: [share("zcode", "ZCode", "generic")], agent: [share("claude", "Claude Code", "claudecode-color")], model: [share("glm-5", "glm-5", "")] },
    agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }], providers: [{ id: "zcode", name: "ZCode", icon: "generic" }],
  };
}

// a day of sessions: 15.4 亿 (1.54B) tokens in and out, today
function sessDay() {
  const d = new Date(), date = [d.getFullYear(), d.getMonth() + 1, d.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
  return { date, usage: [{ agent: "claude", cwd: "/work/alpha", model: "glm-5", input: 1e9, output: 5.4e8, cache_read: 2e9, cache_write: 0, cost: 3, priced: true }], active: [] };
}
// the period's usage: 15.4 亿 (1.54B) tokens, 8000 万 (80M) at the busiest hour
const overview = { calls: 900, errors: 0, input: 1e9, output: 5.4e8, cache_read: 2e9, cache_write: 3e7, reasoning: 2e8, unpriced: 0, cost: 1, bucket: "hour",
  series: [{ label: "9", calls: 600, input: 6e7, output: 2e7, cost: 0.5 }, { label: "10", calls: 300, input: 9.4e8, output: 5.2e8, cost: 0.5 }], agents: [], models: [] };

// the settings live here, across reloads of one context
function serve(lang, store, web) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: store.cur });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") { store.posts.push(req.postDataJSON()); store.cur = { ...store.cur, ...req.postDataJSON() }; }
      return json(store.cur);
    }
    if (url.pathname === "/api/usage/requests") return json(ledger());
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json(overview);
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") { const d = sessDay(); return json({ from: d.date, to: d.date, days: [d], agents: { claude: "Claude Code" } }); }
    if (url.pathname === "/api/sessions/overview") return json({ count: 3, median: 5e8, p90: 9e8, days: [3], messages: [30], output: [0] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    // the gateway's trace waits for a request, as it does: the panel isn't redrawn by one
    if (url.pathname === "/api/gateway/trace" && url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
    if (url.pathname === "/api/gateway/trace") return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the chart's side labels, and whether each ends before the plot starts
async function axis(p, card) {
  return p.locator(card).first().evaluate((c) => {
    const svg = c.querySelector(".led-chart svg");
    const grid = Math.min(...[...svg.querySelectorAll(".grid")].map((g) => g.getBoundingClientRect().left));
    return [...svg.querySelectorAll("text.axis[text-anchor=end]")].map((x) => {
      const b = x.getBoundingClientRect();
      return { text: x.textContent.trim(), fits: b.width > 0 && b.right <= grid + 0.5 };
    });
  });
}
const kpi = async (p, card) => (await p.locator(card + " .blk .v").first().textContent()).trim();

async function check(p, card, western, what) {
  const labs = await axis(p, card);
  assert.equal(labs.length, 5, what + ": five side labels");
  for (const l of labs) assert(l.fits, `${what}: "${l.text}" runs into the plot`);
  const texts = labs.map((l) => l.text);
  if (western) {
    assert(!texts.some((x) => /[万亿]/.test(x)), `${what}: K/M/B axis, got ${texts}`);
    assert(texts.includes("80M"), `${what}: 80M on the axis, got ${texts}`);
  } else {
    assert(texts.includes("8000 万"), `${what}: 8000 万 on the axis, got ${texts}`);
  }
}

// every other count the Usage page shows, in the one unit or the other
const has = (x, western) => western ? !/[万亿]/.test(x) && /\d[KMB]\b/.test(x) : !/\d[KMB]\b/.test(x) && /[万亿]/.test(x);
async function rest(p, western, what) {
  await p.locator("#usageTab .opt").nth(0).click();
  await p.locator("#stats .kpi b").first().waitFor();
  const tiles = await p.locator("#stats .kpi").allInnerTexts();
  assert.equal(tiles[0].split("\n")[0], western ? "1.54B" : "15.4 亿", what + ": the Overview's tokens");
  for (const x of tiles) if (/\d/.test(x)) assert(has(x, western) || !/[万亿KMB]/.test(x), `${what}: Overview tile "${x}"`);
  assert.equal((await p.locator("#chart .peak").textContent()).trim(), western ? "1.46B" : "14.6 亿", what + ": the Overview chart's peak");

  await p.locator("#usageTab .opt").nth(1).click();
  await p.locator("#ledChart svg").waitFor();
  const sum = (await p.locator("#ledSum").textContent()).trim();
  assert(has(sum, western), `${what}: the Requests summary "${sum}"`);
  assert(sum.includes(western ? "15M" : "1540 万"), `${what}: the Requests summary "${sum}"`);

  await p.locator("#usageTab .opt").nth(2).click();
  await p.waitForFunction(() => !document.querySelector("#sessStats .kpi.stale") && /\d/.test(document.querySelectorAll("#sessStats .kpi b")[1]?.textContent || ""));
  const st = await p.locator("#sessStats .kpi").allInnerTexts();
  assert.equal(st[1].split("\n")[0], western ? "1.54B" : "15.4 亿", what + ": the Sessions tokens");
  assert(st[0].includes(western ? "500M" : "5 亿"), `${what}: the Sessions median "${st[0]}"`);
  assert(has(st[3], western), `${what}: the Sessions cache "${st[3]}"`);
  await p.locator("#usageTab .opt").nth(1).click();
}

const settingsView = (p) => p.locator("#view-settings").evaluate((v) => v.scrollTop);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Chinese counts in 万/亿 or, picked in Settings, K/M/B", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());

    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const store = { cur: { lang, theme: "light", currency: "usd", tray: "panel" }, posts: [] };
        const ctx = await browser.newContext({ viewport: { width: 1000, height: 480 }, reducedMotion: "reduce" });
        const p = await ctx.newPage();
        const errors = [];
        p.setDefaultTimeout(5000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.route("**/*", serve(lang, store, false));
        const requests = async () => {
          await p.locator('[data-view="usage"]').first().click();
          await p.locator("#usageTab .opt").nth(1).click();
          await p.locator("#ledChart svg").waitFor();
        };

        // by default Chinese says 万 and 亿, English K/M/B
        await p.goto("http://magpie.test/");
        await requests();
        await check(p, ".led-trend", lang === "en", lang + " default");
        assert.equal(await kpi(p, "#ledKpi"), lang === "en" ? "1.54B" : "15.4 亿");
        await rest(p, lang === "en", lang + " default");

        await p.locator("#prefs").click();

        await p.locator("#setTab-usage").click();
        await p.locator("#currencySegs .opt").first().waitFor();
        if (lang === "en") {
          assert.equal(await p.locator("#unitsRow").isHidden(), true, "no Number units row in English");
        } else {
          const segs = p.locator("#unitsSegs .opt");
          assert.equal(await p.locator("#unitsRow .name").textContent(), "数字单位");
          assert.deepEqual((await segs.allTextContents()).map((x) => x.trim()), ["中文（万 / 亿）", "英文（K / M / B）"]);
          assert.equal(await segs.nth(0).evaluate((b) => b.classList.contains("on")), true, "万/亿 starts picked");
          // scrolled down by a real wheel, a pick moves nothing
          await p.locator("#currencySegs").hover();
          for (let i = 0; i < 20 && !(await settingsView(p)); i++) { await p.mouse.wheel(0, 200); await p.waitForTimeout(20); }
          const before = await settingsView(p);
          assert(before > 0, "the settings list must be long enough to scroll");
          await segs.nth(1).click();
          await p.locator("#unitsSegs .opt.on", { hasText: "K / M / B" }).waitFor();
          await p.waitForTimeout(300);
          assert.equal(await settingsView(p), before, "picking units must not scroll the settings page");
          assert.equal(store.posts.at(-1).westernUnits, true, "saved as westernUnits");
          assert.equal(store.posts.at(-1).currency, "usd", "the rest of the settings sent as they were");

          // the Requests tab now says K/M/B, still in Chinese
          await requests();
          await check(p, ".led-trend", true, "zh western");
          assert.equal(await kpi(p, "#ledKpi"), "1.54B");
          assert.equal((await p.locator("#ledKpi .blk .k").nth(1).textContent()).trim(), "请求", "the page stays Chinese");
          await rest(p, true, "zh western");

          // and after a reload, from the settings magpie keeps
          await p.reload();
          await requests();
          await check(p, ".led-trend", true, "zh western, reloaded");
        }
        assert.deepEqual(errors, []);
        await ctx.close();

        // the tray panel's Usage tab, with what was saved
        const pctx = await browser.newContext({ viewport: { width: 380, height: 560 }, reducedMotion: "reduce" });
        const pp = await pctx.newPage();
        pp.setDefaultTimeout(5000);
        pp.on("pageerror", (e) => errors.push(e.message));
        await pp.route("**/*", serve(lang, store, true));
        await pp.goto("http://magpie.test/?mode=panel");
        await pp.locator('[data-ptab="stats"]').click();
        await pp.locator("#panelUsage .pu-card .led-chart svg").waitFor();
        await check(pp, "#panelUsage .pu-card", true, lang + " panel");
        assert.equal((await pp.locator("#panelUsage .pu-tot .blk .v").first().textContent()).trim(), "1.54B");
        // Routing's "today" over its list, then in 万/亿 once Chinese's units are picked again
        await pp.locator('[data-ptab="routing"]').click();
        const today = () => pp.locator(".pr-today b").nth(1).textContent();
        await pp.waitForFunction(() => /\d/.test(document.querySelectorAll(".pr-today b")[1]?.textContent || ""));
        assert.equal(await today(), "1.54B", lang + " panel: Routing's today");
        if (lang === "zh") {
          // redrawn as the setting turns, not when a request next comes
          const now = await pp.evaluate((s) => { applyPrefs({ ...s, westernUnits: false }); return document.querySelectorAll(".pr-today b")[1]?.textContent; }, store.cur);
          assert.equal(now, "15.4 亿", "zh panel: Routing's today in 万/亿 again");
          await pp.locator('[data-ptab="stats"]').click();
          assert.equal((await pp.locator("#panelUsage .pu-tot .blk .v").first().textContent()).trim(), "15.4 亿");
          await check(pp, "#panelUsage .pu-card", false, "zh panel, 万/亿");
        }
        assert.deepEqual(errors, []);
        await pctx.close();
      });
    }
  });
}
