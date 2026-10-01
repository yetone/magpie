// Run with Node's test runner and Playwright on the module path; see README.md.
// The Currency setting (#212): the Usage page's cost shows in dollars by
// default and in yuan, at magpie's cached exchange rate, once cny is
// chosen on the Settings page — a click on the segmented control never
// scrolls the page, and the rate used sits in the row's tooltip. English
// and Chinese; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const usage = {
  calls: 5, errors: 0, input: 12000, output: 4000, cache_read: 500, cache_write: 100, reasoning: 200,
  unpriced: 0, cost: 12.34, bucket: "day",
  series: [
    { label: "Mon", input: 6000, output: 2000, calls: 3, cost: 7 },
    { label: "Tue", input: 6000, output: 2000, calls: 2, cost: 5.34 },
  ],
  agents: [{ name: "Claude Code", calls: 5, cost: 12.34 }],
  models: [{ name: "claude-opus", calls: 5, cost: 12.34 }],
};

function settingsPayload(over) {
  return {
    theme: "light", lang: "en", tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

function server(lang) {
  let cur = settingsPayload({ lang });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: cur.fx });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") cur = { ...cur, ...req.postDataJSON() };
      return json(cur);
    }
    if (url.pathname === "/api/usage") return json(usage);
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const view = (page) => page.locator("#view-settings").evaluate((v) => v.scrollTop);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": costs show in the chosen currency", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const pages = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-currency-${i}.png`) });
      }
      await browser.close();
    });

    for (const [lang, name, usd, cny, sub] of [
      ["en", "¥ CNY", "$ USD", "¥ CNY", "1 USD = 7.20 CNY"],
      ["zh", "¥ 人民币", "$ 美元", "¥ 人民币", "1 美元 = 7.20 元"],
    ]) {
      await t.test(lang, async () => {
        const errors = [];
        const context = await browser.newContext({ viewport: { width: 900, height: 480 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        pages.push(page);
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));

        // the Usage page shows $ by default
        await page.goto("http://magpie.test/?view=usage");
        await page.locator("#usageCost b").waitFor();
        assert.equal((await page.locator("#usageCost b").textContent()).trim(), "≈$12.34", "usd is the default");

        // Settings: the currency row, its rate in the tooltip
        await page.locator("#prefs").click();
        await page.locator("#setTab-usage").click();
        await page.locator("#currencySegs .opt").first().waitFor();
        const segs = page.locator("#currencySegs .opt");
        assert.equal(await segs.count(), 2);
        assert.equal((await segs.nth(0).textContent()).trim(), usd);
        assert.equal((await segs.nth(1).textContent()).trim(), cny);
        assert.equal(await segs.nth(0).evaluate((b) => b.classList.contains("on")), true, "usd starts picked");
        const tip = await page.locator("#currencySub").getAttribute("title");
        assert(tip.includes(sub), `tooltip should carry the rate: ${tip}`);

        // scrolled well down (a real wheel, so the reader's-scroll guard lets
        // it stick — a script setting scrollTop outright is put back), picking
        // ¥ CNY moves nothing
        await page.locator("#currencySegs").hover();
        for (let i = 0; i < 20 && !(await view(page)); i++) { await page.mouse.wheel(0, 200); await page.waitForTimeout(20); }
        const before = await view(page);
        assert(before > 0, "the settings list must be long enough to scroll");
        await segs.nth(1).click();
        await page.locator('#currencySegs .opt.on', { hasText: cny }).waitFor();
        await page.waitForTimeout(400);
        assert.equal(await view(page), before, "picking a currency must not scroll the settings page");

        // the Usage page now shows ¥, at the fixed rate (12.34 * 7.2)
        await page.locator('[data-view="usage"]').click();
        await page.waitForTimeout(200);
        assert.equal((await page.locator("#usageCost b").textContent()).trim(), "≈¥88.85");

        // and back to Settings, ¥ CNY is still picked after a reload
        await page.reload();
        await page.locator("#prefs").click();
        await page.locator("#setTab-usage").click();
        await page.locator("#currencySegs .opt").first().waitFor();
        assert.equal(await page.locator('#currencySegs .opt', { hasText: cny }).evaluate((b) => b.classList.contains("on")), true);

        assert.deepEqual(errors, []);
      });
    }
  });
}

// After a restart with ¥ CNY already chosen (#212 follow-up): /api/state
// carries the settings (currency: "cny") and, beside them rather than in
// them, the rate (fx), as the Go side sends it. The Usage page, opened
// straight away and before Settings ever is, must show ¥ at that rate —
// it once read the rate only from inside the settings, found none, and
// fell back to $ while the Settings row still showed ¥ CNY picked.
function restartedServer(lang) {
  const cur = settingsPayload({ lang, currency: "cny" });
  const base = server(lang);
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/api/state") {
      return route.fulfill({ json: { agents: [], profiles: [], settings: { theme: "light", lang, currency: "cny" }, fx: cur.fx } });
    }
    if (url.pathname === "/api/settings" && req.method() === "GET") return route.fulfill({ json: cur });
    return base(route);
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a currency chosen before a restart is the one costs show in", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const [lang, cny] of [["en", "¥ CNY"], ["zh", "¥ 人民币"]]) {
      await t.test(lang, async () => {
        const errors = [];
        const context = await browser.newContext({ viewport: { width: 900, height: 420 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", restartedServer(lang));

        // no visit to Settings first: the Usage page at start is in ¥
        await page.goto("http://magpie.test/?view=usage");
        await page.locator("#usageCost b").waitFor();
        assert.equal((await page.locator("#usageCost b").textContent()).trim(), "≈¥88.85", "cny at the rate state gave");

        // Settings agrees
        await page.locator("#prefs").click();
        await page.locator("#setTab-usage").click();
        await page.locator("#currencySegs .opt").first().waitFor();
        assert.equal(await page.locator("#currencySegs .opt", { hasText: cny }).evaluate((b) => b.classList.contains("on")), true);

        // and back on Usage, still ¥
        await page.locator('[data-view="usage"]').click();
        await page.waitForTimeout(200);
        assert.equal((await page.locator("#usageCost b").textContent()).trim(), "≈¥88.85");

        assert.deepEqual(errors, []);
        await context.close();
      });
    }
  });
}
