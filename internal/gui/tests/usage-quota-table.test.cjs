// Run with Node's test runner and Playwright on the module path; see README.md.
// The Usage page's allowances as a table beside the cards (zysicyj on X: "a
// dozen-plus accounts, the waterfall is hard to scan, could it be a
// table"). Cards stay the default and the table is the reader's own pick,
// remembered on this computer: a row an account, its subscription and
// account first, then its windows' share and when they start again; a click
// on a head sorts by it, again the other way, a third time back to the
// cards' order. A narrow window scrolls the table in its own box, never the
// page. Both engines, every language, wide and narrow; no backend, the API
// is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const H = 3600e3;
const at = (h) => new Date(Date.now() + h * H).toISOString();
const w = (name, used, h) => ({ name, used, resetsAt: at(h) });
// as a user with a dozen-plus accounts has them: several of one agent,
// plans with other windows, a balance, a failed read
const quotas = [
  ...[["a", 20, 40], ["b", 90, 10], ["c", 55, 75], ["d", 5, 99]].map(([u, five, week]) => (
    { provider: "codex", name: "Codex", kind: "subscription", icon: "codex-color", plan: "Plus", user: u + "@example.com", windows: [w("5 hours", five, 2), w("7 days", week, 90)] })),
  ...[["e", 33, 12], ["f", 71, 61], ["g", 9, 3]].map(([u, five, week]) => (
    { provider: "claude", name: "Claude Code", kind: "subscription", icon: "claude-color", plan: "Max 5x", user: u + "@example.com", windows: [w("5 hours", five, 3), w("7 days", week, 80), w("7 days Opus", 14, 80)] })),
  { provider: "antigravity", name: "Antigravity", icon: "antigravity-color", plan: "Google AI Pro", user: "h@example.com", windows: [
    { name: "Gemini 3 Flash", family: "Gemini", used: 100, resetsAt: at(13) }, { name: "Claude Sonnet 4.6", family: "Claude", used: 0, resetsAt: at(5) }] },
  { provider: "kimi", name: "Kimi", icon: "kimi", user: "i@example.com", windows: [w("Weekly", 42, 70)] },
  { provider: "zai", name: "Z.ai", icon: "zai", user: "j@example.com", windows: [w("5 hours", 64, 1), w("Weekly", 12, 70)] },
  { provider: "deepseek", name: "DeepSeek", icon: "deepseek", windows: [], balance: "¥17.41" },
  { provider: "minimax", name: "MiniMax", icon: "minimax", user: "k@example.com", windows: [], error: "unauthorized" },
  { provider: "codex", name: "Codex", kind: "subscription", icon: "codex-color", plan: "Pro", user: "l@example.com", windows: [w("5 hours", 0, 5), w("7 days", 0, 160)] },
  { provider: "claude", name: "Claude Code", kind: "subscription", icon: "claude-color", plan: "Pro", user: "m@example.com", windows: [w("5 hours", 12, 4), w("7 days", 25, 100)] },
  { provider: "kimi", name: "Kimi", icon: "kimi", user: "n@example.com", windows: [w("Weekly", 77, 70)] },
];

function serve(lang, left) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: left, currency: "usd" };
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

const CARDS = { en: "Cards", zh: "卡片", "zh-TW": "卡片", ja: "カード", de: "Karten" };
const TABLE = { en: "Table", zh: "表格", "zh-TW": "表格", ja: "表", de: "Tabelle" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: the allowances as cards or a table`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, false));
        await page.goto("http://magpie.test/?view=usage");
        await page.waitForSelector("#subscriptionUsage .subscription-card");

        // cards until the reader picks the table
        assert.equal(await page.locator(".qt").count(), 0);
        assert.equal(await page.locator("#quotaView .opt.on").innerText(), CARDS[lang]);
        assert.equal(await page.evaluate(() => localStorage.getItem("magpie.quotaView")), null, "nothing is saved until the reader picks");

        await page.locator("#quotaView .opt", { hasText: TABLE[lang] }).click();
        await page.waitForSelector("#subscriptionUsage .qt tbody tr");
        assert.equal(await page.locator("#subscriptionUsage .subscription-card").count(), 0);
        // a row an account (a card's several accounts are rows too)
        const accounts = quotas.length;
        assert.equal(await page.locator(".qt tbody tr").count(), accounts);
        const heads = await page.$$eval(".qt thead th", (ths) => ths.map((x) => x.textContent.trim()));
        assert.equal(heads.length, 2 + 4 + 1, `${heads}`);
        // the cards' figures, the same readings
        const row = await page.$eval('.qt tbody tr:nth-child(1)', (tr) => tr.innerText.replace(/\s+/g, " "));
        assert.match(row, /Codex/);
        assert.match(row, /a@example\.com/);
        assert.match(row, /20%/);
        assert.match(row, /40%/);
        // when each starts again
        assert.ok((await page.$$eval(".qt .qt-reset", (x) => x.length)) >= accounts, "resets are shown");
        // a failed read and a balance say so across the windows
        assert.equal(await page.locator('.qt tbody tr:has-text("¥17.41")').count(), 1);
        assert.equal(await page.locator(".qt tbody tr .qt-msg").count(), 2);

        // the pick is remembered
        assert.equal(await page.evaluate(() => localStorage.getItem("magpie.quotaView")), '"table"');
        await page.reload();
        await page.waitForSelector("#subscriptionUsage .qt tbody tr");
        assert.equal(await page.locator("#quotaView .opt.on").innerText(), TABLE[lang]);

        // sorted by a window: the most used first, again the least, then the cards' order
        const first = () => page.$eval(".qt tbody tr:nth-child(1)", (tr) => tr.innerText.replace(/\s+/g, " "));
        const fiveHours = page.locator(".qt thead th.qt-w .qt-sort").first();
        await fiveHours.click();
        assert.match(await first(), /b@example\.com/);
        assert.equal(await page.locator('.qt thead th[aria-sort="descending"]').count(), 1);
        await page.locator(".qt thead th.qt-w .qt-sort").first().click();
        assert.match(await first(), /l@example\.com/, "ascending: unused first");
        assert.equal(await page.locator('.qt thead th[aria-sort="ascending"]').count(), 1);
        await page.locator(".qt thead th.qt-w .qt-sort").first().click();
        assert.match(await first(), /a@example\.com/, "back to the cards' order");
        assert.equal(await page.locator('.qt thead th[aria-sort]:not([aria-sort="none"])').count(), 0);
        // by account, A first
        await page.locator(".qt thead th.qt-acct .qt-sort").click();
        assert.match(await first(), /a@example\.com/);

        // the page itself never scrolls sideways; the table scrolls in its box
        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert.ok(over <= 0, `the page scrolls sideways by ${over}px`);
        const box = await page.$eval(".qt-wrap", (b) => ({ w: b.clientWidth, s: b.scrollWidth, page: document.documentElement.clientWidth, right: b.getBoundingClientRect().right }));
        assert.ok(box.right <= box.page + 1, "the table's box stays inside the page");
        if (width === 420) assert.ok(box.s > box.w, "a narrow table scrolls in its box");

        // back to the cards
        await page.locator("#quotaView .opt", { hasText: CARDS[lang] }).click();
        await page.waitForSelector("#subscriptionUsage .subscription-card");
        assert.equal(await page.locator(".qt").count(), 0);
        assert.equal(await page.evaluate(() => document.querySelector("#subscriptionUsage").classList.contains("tabled")), false);
        assert.deepEqual(errors, []);
      });
    }
  }
  test(`${engine}: Left reads in the table too`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 1100, height: 800 }, reducedMotion: "reduce" })).newPage();
    page.setDefaultTimeout(5000);
    await page.addInitScript(() => localStorage.setItem("magpie.quotaView", '"table"'));
    await page.route("**/*", serve("en", true));
    await page.goto("http://magpie.test/?view=usage");
    await page.waitForSelector("#subscriptionUsage .qt tbody tr");
    const row = await page.$eval(".qt tbody tr:nth-child(1)", (tr) => tr.innerText.replace(/\s+/g, " "));
    assert.match(row, /80%/);
    assert.match(row, /60%/);
  });
}
