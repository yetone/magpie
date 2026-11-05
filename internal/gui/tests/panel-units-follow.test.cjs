// Run with Node's test runner and Playwright on the module path; see README.md.
// The tray panel follows Settings' units (blrbrzkj on Discord, two
// screenshots: Settings said ¥ 人民币 and 中文（万 / 亿）, the panel's Usage
// tab still ≈$7.18 and 16M). The panel is open on its Usage tab while
// Settings, in the window, picks another currency and number units; when
// the panel is shown again it reads the settings again, and its cost, its
// counts and its chart's axis say what was picked, not what was picked
// when it was first drawn: ¥ at the rate magpie has, 万 / 亿. Then back to
// $ and K / M / B. Each language: 万 / 亿 is Chinese's alone, so the
// others check the currency only. Chromium and WebKit; no backend, the API
// is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_PANEL_UNITS_ASSETS || path.join(__dirname, "../assets"));
const midnight = new Date();
midnight.setHours(0, 0, 0, 0);

// a day of one provider: 7000 万 (70M) tokens and $1.2 an hour, from 2am
function ledger() {
  const series = Array.from({ length: 24 }, (_, h) => {
    const on = h >= 2 ? 1 : 0;
    const part = { calls: 9 * on, tokens: 7e7 * on, cost: 1.2 * on };
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

// /api/state as magpie answers it: the settings, and beside them the rate
// a cny cost is shown at (none looked up for usd)
function serve(lang, store) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light"};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") { store.stateReads++; return json({ agents: [], profiles: [], settings: store.cur, fx: store.cur.currency === "cny" ? { rate: 7, at: new Date().toISOString(), stale: false } : { rate: 0 } }); }
    if (url.pathname === "/api/settings") return json(store.cur);
    if (url.pathname === "/api/usage/requests") return json(ledger());
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
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

// the panel's Usage tab: its tokens, its cost, and its chart's side labels
async function shown(p) {
  return p.evaluate(() => {
    const v = [...document.querySelectorAll("#panelUsage .pu-tot .blk .v")].map((x) => x.textContent.trim());
    const axis = [...document.querySelectorAll("#panelUsage .pu-card .led-chart svg text.axis[text-anchor=end]")].map((x) => x.textContent.trim());
    return { tokens: v[0], cost: v[2], axis };
  });
}

// the panel hidden and shown again, as the Mac's page is told it is: by
// hideWebview and showWebview, and when Wails hides it itself on a click
// elsewhere or Escape, by sayWailsHides (windowvisibility.go; its AppKit
// half is TestPanelHiddenByWailsMarksPageOutOfSight)
async function hideAndShow(p, store) {
  const before = store.stateReads;
  await p.evaluate(() => window.magpieWindowVisibility(false));
  await p.evaluate(() => window.magpieWindowVisibility(true));
  for (let i = 0; i < 100 && store.stateReads === before; i++) await p.waitForTimeout(20);
  assert(store.stateReads > before, "shown again, the panel reads the state again");
}

const LANGS = (process.env.MAGPIE_LANGS || "zh,en,zh-TW,ja,de").split(",");
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the tray panel's Usage tab follows Settings' currency and number units", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of LANGS) {
      await t.test(lang, async () => {
        const chinese = lang === "zh" || lang === "zh-TW";
        const store = { cur: { lang, theme: "light", currency: "usd", chineseUnits: false, tray: "panel" }, stateReads: 0 };
        const ctx = await browser.newContext({ viewport: { width: 380, height: 560 }, reducedMotion: "reduce" });
        const p = await ctx.newPage();
        const errors = [];
        p.setDefaultTimeout(5000);
        p.setDefaultNavigationTimeout(60000);
        p.on("pageerror", (e) => errors.push(e.message));
        await p.route("**/*", serve(lang, store));
        await p.goto("http://magpie.test/?mode=panel");
        await p.locator('[data-ptab="stats"]').click();
        await p.locator("#panelUsage .pu-card .led-chart svg").waitFor();
        let s = await shown(p);
        assert.equal(s.tokens, "1.54B", lang + ": K/M/B at first");
        assert.equal(s.cost, "≈$26.40", lang + ": $ at first");

        // Settings, in the window, picks ¥ and 万 / 亿
        store.cur = { ...store.cur, currency: "cny", chineseUnits: true };
        await hideAndShow(p, store);
        await p.waitForFunction(() => /¥/.test(document.querySelectorAll("#panelUsage .pu-tot .blk .v")[2]?.textContent || ""), null, { timeout: 3000 }).catch(() => {});
        s = await shown(p);
        assert.equal(s.cost, "≈¥185", lang + ": the cost in yuan once Settings picks ¥, got " + s.cost);
        if (chinese) {
          assert.equal(s.tokens, "15.4 " + (lang === "zh" ? "亿" : "億"), lang + ": the tokens in 万/亿, got " + s.tokens);
          assert(s.axis.some((x) => /万|萬/.test(x)), lang + ": the chart's axis in 万, got " + s.axis);
        } else {
          assert.equal(s.tokens, "1.54B", lang + ": K/M/B, 万/亿 being Chinese's alone");
        }

        // and back
        store.cur = { ...store.cur, currency: "usd", chineseUnits: false };
        await hideAndShow(p, store);
        await p.waitForFunction(() => /\$/.test(document.querySelectorAll("#panelUsage .pu-tot .blk .v")[2]?.textContent || ""), null, { timeout: 3000 }).catch(() => {});
        s = await shown(p);
        assert.equal(s.cost, "≈$26.40", lang + ": back in dollars, got " + s.cost);
        assert.equal(s.tokens, "1.54B", lang + ": back in K/M/B, got " + s.tokens);
        assert(!s.axis.some((x) => /万|萬|亿|億/.test(x)), lang + ": the axis back in K/M/B, got " + s.axis);
        assert.deepEqual(errors, []);
        await ctx.close();
      });
    }
  });
}

// The currency alone (dLdgKyRd on Discord, v0.1.3025: the panel's Usage tab
// ≈$12.36 while the Usage page said ≈¥82.61). The test above picks ¥ and
// 万 / 亿 together, and the units' change redrew the panel while the
// currency's alone didn't; nor was a panel drawn before the state came
// ever redrawn. Each way the panel gets its currency: its first open, with
// /api/state slower than its usage read; a reopen; a focus, as when
// Settings changed while it was open; and its own tab picked afterwards.
// At the panel's width and a narrower one.
async function openPanel(browser, lang, store, width, tab) {
  const ctx = await browser.newContext({ viewport: { width, height: 560 }, reducedMotion: "reduce" });
  const p = await ctx.newPage();
  const errors = [];
  p.setDefaultTimeout(5000);
  p.setDefaultNavigationTimeout(60000);
  p.on("pageerror", (e) => errors.push(e.message));
  const plain = serve(lang, store);
  await p.route("**/*", async (route) => {
    // the state answers after the panel's usage read
    if (store.slowState && new URL(route.request().url()).pathname === "/api/state") await new Promise((r) => setTimeout(r, 400));
    return plain(route);
  });
  await p.addInitScript((tab) => { try { localStorage.setItem("magpie.panelTab", tab); } catch {} }, tab);
  await p.goto("http://magpie.test/?mode=panel");
  return { ctx, p, errors };
}
const costShown = (p) => p.evaluate(() => document.querySelectorAll("#panelUsage .pu-tot .blk .v")[2]?.textContent.trim() || "");
async function costBecomes(p, want, why) {
  for (let i = 0; i < 100 && (await costShown(p)) !== want; i++) await p.waitForTimeout(20);
  assert.equal(await costShown(p), want, why);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the tray panel's Usage tab follows Settings' currency picked alone", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of LANGS) for (const width of [380, 300]) {
      await t.test(lang + " " + width + "px", async () => {
        const units = { lang, theme: "light", chineseUnits: false, tray: "panel" };

        // first open: ¥ picked already, the state slower than the usage read
        let store = { cur: { ...units, currency: "cny" }, stateReads: 0, slowState: true };
        let o = await openPanel(browser, lang, store, width, "stats");
        await o.p.locator("#panelUsage .pu-card .led-chart svg").waitFor();
        for (let i = 0; i < 100 && store.stateReads === 0; i++) await o.p.waitForTimeout(20);
        await o.p.waitForTimeout(500); // the state is in
        await costBecomes(o.p, "≈¥185", lang + ": first open, the state after the usage");
        assert.deepEqual(o.errors, []);
        await o.ctx.close();

        // reopened, then focused, after Settings picked the currency alone
        store = { cur: { ...units, currency: "usd" }, stateReads: 0 };
        o = await openPanel(browser, lang, store, width, "stats");
        await o.p.locator("#panelUsage .pu-card .led-chart svg").waitFor();
        await costBecomes(o.p, "≈$26.40", lang + ": $ at first");
        store.cur = { ...store.cur, currency: "cny" };
        await hideAndShow(o.p, store);
        await costBecomes(o.p, "≈¥185", lang + ": reopened after ¥ was picked alone");
        store.cur = { ...store.cur, currency: "usd" };
        const before = store.stateReads;
        await o.p.evaluate(() => window.dispatchEvent(new Event("focus")));
        for (let i = 0; i < 100 && store.stateReads === before; i++) await o.p.waitForTimeout(20);
        await costBecomes(o.p, "≈$26.40", lang + ": focused after $ was picked again");
        assert.deepEqual(o.errors, []);
        await o.ctx.close();

        // open on another tab; ¥ picked; the Usage tab picked after
        store = { cur: { ...units, currency: "usd" }, stateReads: 0 };
        o = await openPanel(browser, lang, store, width, "agents");
        for (let i = 0; i < 100 && store.stateReads === 0; i++) await o.p.waitForTimeout(20);
        store.cur = { ...store.cur, currency: "cny" };
        await hideAndShow(o.p, store);
        await o.p.waitForTimeout(100);
        await o.p.locator('[data-ptab="stats"]').click();
        await o.p.locator("#panelUsage .pu-card .led-chart svg").waitFor();
        await costBecomes(o.p, "≈¥185", lang + ": its Usage tab picked after ¥");
        assert.deepEqual(o.errors, []);
        await o.ctx.close();
      });
    }
  });
}
