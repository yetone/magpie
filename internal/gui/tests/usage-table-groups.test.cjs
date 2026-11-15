// Run with Node's test runner and Playwright on the module path; see README.md.
// jay68xu on X (#6740), on the Usage page's table: a provider's several
// accounts were rows of their own, its icon and name again on each, and
// columns no row filled took room. A provider with several accounts is now
// one row, its name, how many and each column's share over them, its
// accounts folded under it until it is clicked; what is open is kept across
// a reload, by provider. A provider with one account is its one row, as
// before. A column with nothing in any row (Account, when no account is
// named) is left out. The clicks don't move the page, and Enter on the
// row's button keeps the focus on it. Both engines, every language, wide
// and narrow; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (h) => new Date(Date.now() + h * 36e5).toISOString();
const w = (name, used, h) => ({ name, used, resetsAt: at(h) });
const ZC = { provider: "zcode", name: "ZCode", icon: "generic", plan: "Pro" };
const accounts = [
  ...[["a", 10, 20], ["b", 30, 40], ["c", 80, 60]].map(([u, five, week]) => ({ ...ZC, user: u + "@example.com", windows: [w("5 hours", five, 2), w("7 days", week, 90)] })),
  { provider: "kimi", name: "Kimi", icon: "kimi", user: "k@example.com", windows: [w("5 hours", 50, 1), w("7 days", 70, 70)] },
  { provider: "qoder", name: "Qoder", icon: "generic", user: "q1@example.com", windows: [w("5 hours", 0, 3)] },
  { provider: "qoder", name: "Qoder", icon: "generic", user: "q2@example.com", windows: [], error: "unauthorized" },
];
// no account named anywhere: keys and balances
const unnamed = [
  { provider: "deepseek", name: "DeepSeek", icon: "deepseek", windows: [], balance: "¥17.41" },
  { provider: "zai", name: "Z.ai", icon: "zai", windows: [w("5 hours", 64, 1), w("7 days", 12, 70)] },
];

function serve(lang, quotas) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const COUNT = { en: "3 accounts", zh: "3 个账号", "zh-TW": "3 個帳號", ja: "3 個のアカウント", de: "3 Konten" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: a provider's accounts fold under its row`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.addInitScript(() => localStorage.setItem("magpie.quotaView", '"table"'));
        await page.route("**/*", serve(lang, accounts));
        await page.goto("http://magpie.test/?view=usage");
        await page.waitForSelector("#subscriptionUsage .qt tbody tr");
        const rows = () => page.$$eval(".qt tbody tr", (trs) => trs.map((tr) => ({ cls: tr.className, p: tr.dataset.provider, text: tr.innerText.replace(/\s+/g, " ").trim() })));

        // folded: a row a provider, one account's provider its one row
        let r = await rows();
        assert.deepEqual(r.map((x) => [x.cls, x.p]), [["qt-group", "zcode"], ["", "kimi"], ["qt-group", "qoder"]]);
        assert.ok(r[0].text.includes("ZCode") && r[0].text.includes(COUNT[lang]), r[0].text);
        // each column's share over the accounts: (10+30+80)/3, (20+40+60)/3
        assert.match(r[0].text, /40%.*40%/);
        assert.doesNotMatch(r[0].text, /@example/);
        assert.match(r[1].text, /k@example\.com/);
        // a failed read counts for nothing in the mean
        assert.match(r[2].text, /\b0%/);
        assert.equal(await page.locator('.qt-group[data-provider="zcode"] .qt-fold').getAttribute("aria-expanded"), "false");
        assert.equal(await page.evaluate(() => localStorage.getItem("magpie.quotaTableOpen")), null);

        // a click opens it in place, the page not moving
        const at0 = await page.evaluate(() => [scrollY, document.querySelector("#view-usage")?.scrollTop ?? 0]);
        await page.locator('.qt-group[data-provider="zcode"]').click();
        r = await rows();
        assert.deepEqual(r.slice(0, 5).map((x) => [x.cls, x.p]), [["qt-group", "zcode"], ["qt-kid", "zcode"], ["qt-kid", "zcode"], ["qt-kid", "zcode"], ["", "kimi"]]);
        assert.match(r[1].text, /a@example\.com.*10%.*20%/);
        assert.doesNotMatch(r[1].text, /ZCode/, "the provider's name isn't said again");
        assert.equal(await page.locator('.qt-group[data-provider="zcode"] .qt-fold').getAttribute("aria-expanded"), "true");
        assert.deepEqual(await page.evaluate(() => [scrollY, document.querySelector("#view-usage")?.scrollTop ?? 0]), at0, "the page didn't move");

        // kept across a reload
        await page.reload();
        await page.waitForSelector("#subscriptionUsage .qt tbody tr.qt-kid");
        assert.equal(await page.locator(".qt tbody tr.qt-kid").count(), 3);
        assert.equal(await page.evaluate(() => localStorage.getItem("magpie.quotaTableOpen")), '["zcode"]');

        // the keyboard: Enter on the row's button folds it, the focus stays
        await page.locator('.qt-group[data-provider="zcode"] .qt-fold').focus();
        await page.keyboard.press("Enter");
        assert.equal(await page.locator(".qt tbody tr.qt-kid").count(), 0);
        assert.equal(await page.evaluate(() => document.activeElement?.closest(".qt-group")?.dataset.provider), "zcode");
        await page.keyboard.press("Space");
        assert.equal(await page.locator(".qt tbody tr.qt-kid").count(), 3);

        // the narrow table scrolls in its own box, never the page
        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert.ok(over <= 0, `the page scrolls sideways by ${over}px`);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}px: a column no row fills is left out`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.addInitScript(() => localStorage.setItem("magpie.quotaView", '"table"'));
        await page.route("**/*", serve(lang, unnamed));
        await page.goto("http://magpie.test/?view=usage");
        await page.waitForSelector("#subscriptionUsage .qt tbody tr");
        const account = await page.evaluate(() => t("Account"));
        const heads = await page.$$eval(".qt thead th", (ths) => ths.map((x) => x.textContent.replace(/[↑↓]/g, "").trim()));
        assert.ok(!heads.includes(account), `${heads}`);
        assert.equal(await page.locator(".qt .qt-acct").count(), 0);
        // every row has a cell under each head
        const cells = await page.$$eval(".qt tbody tr", (trs) => trs.map((tr) => [...tr.children].reduce((n, td) => n + td.colSpan, 0)));
        assert.deepEqual(cells, cells.map(() => heads.length));
        assert.deepEqual(errors, []);
      });
    }
  }
}
