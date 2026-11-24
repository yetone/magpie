// Run with Node's test runner and Playwright on the module path; see README.md.
// LotusDecoder on X, on Usage › Overview's table: Codex's and Devin's weeks
// were in the 7 days column, but Volcengine Ark's "Weekly", Zhipu's "MCP ·
// Month", Cursor's windows and Antigravity's pools' 5 hours and 7 days
// were all stacked in Other. A column is now how long its windows run: the
// period magpie sends (else the window's name) puts each in its length's
// column whatever the vendor calls it, a month is one column (Mistral's
// "API · Month" goes in it, no column of its own), and windows of one
// length on one account share a cell, each named by what isn't its length.
// A window of no known length (Cursor's monthly pools, which the plugin
// sends no period for) stays in Other, named. Both engines, every
// language, wide and narrow; the API is faked here, the windows shaped as
// /api/usage/quotas sends them.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (h) => new Date(Date.now() + h * 36e5).toISOString();
const H5 = 5 * 3600, D7 = 7 * 86400, M30 = 30 * 86400;
const w = (name, used, h, period, more = {}) => ({ name, used, resetsAt: at(h), ...(period ? { period } : {}), ...more });
const model = (name, family, pool, used, h) => ({ name, family, pool, used, resetsAt: at(h), period: H5 });
const pool = (p, name, used, h, period) => ({ name, pool: p, used, resetsAt: at(h), aside: true, period });
const quotas = [
  { provider: "codex", name: "Codex", kind: "subscription", icon: "codex-color", plan: "Plus", user: "c@example.com", windows: [
    w("5 hours", 12, 2, H5), w("7 days", 64, 90, D7),
    { name: "Image generation", lane: "images", text: true, aside: true, display: "Unknown", used: 0 }] },
  { provider: "devin", name: "Devin", icon: "generic", user: "d@example.com", windows: [w("7 days", 100, 50, D7)] },
  // Volcengine's levels: session, weekly, monthly (volcWindow)
  { provider: "volcengine-plan", name: "Volcengine Ark Coding Plan", icon: "volcengine", plan: "Pro", user: "v@example.com", windows: [
    w("5 hours", 3, 4, H5), w("Weekly", 100, 140, D7), w("Month", 41, 500, M30)] },
  // Zhipu's: TIME_LIMIT is the month's MCP calls, sent with no span
  { provider: "zhipu-plan", name: "Zhipu Coding Plan", icon: "zhipu", plan: "Max", user: "z@example.com", windows: [
    w("5 hours", 8, 3, H5), w("7 days", 20, 100, D7), w("MCP · Month", 99, 400, 0, { aside: true })] },
  // the Cursor plugin's: the billing cycle's pools with no span, Grok Bot's week
  { provider: "cursor", name: "Cursor", icon: "cursor", plan: "Pro", user: "u@example.com", windows: [
    w("Cursor Models", 30, 100), w("Other Models", 12, 100),
    { name: "Free usage", used: 0, display: "+$20.00 on us", resetsAt: at(100), aside: true, text: true },
    w("Total", 25, 100, 0, { aside: true }), w("Grok Bot", 7, 100, D7, { aside: true })] },
  // an Antigravity account whose pools were read
  { provider: "antigravity", name: "Antigravity", icon: "antigravity-color", plan: "Google AI Pro", user: "a@example.com", windows: [
    model("Claude Sonnet 4.6", "Claude", "Claude & GPT", 30, 3), model("Gemini 3 Flash", "Gemini", "Gemini", 11, 2),
    pool("Gemini", "7 days", 23, 100, D7), pool("Gemini", "5 hours", 11, 2, H5),
    pool("Claude & GPT", "7 days", 64, 90, D7), pool("Claude & GPT", "5 hours", 30, 3, H5)] },
  // Mistral's Studio month: a calendar month, 31 days
  { provider: "mistral-plan", name: "Mistral", icon: "mistral", user: "m@example.com", windows: [w("API · Month", 20, 300, 31 * 86400)] },
];

function serve(lang) {
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

const shots = process.env.SHOTS;

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: a table column is how long its windows run`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.addInitScript(() => localStorage.setItem("magpie.quotaView", '"table"'));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=usage");
        await page.waitForSelector("#subscriptionUsage .qt tbody tr");
        const table = await page.evaluate(() => {
          const heads = [...document.querySelectorAll(".qt thead th")].map((x) => x.textContent.replace(/[↑↓]/g, "").trim());
          const rows = Object.fromEntries([...document.querySelectorAll(".qt tbody tr")].map((tr) => [
            tr.querySelector(".qt-acct").textContent.trim(),
            [...tr.children].slice(2).map((td) => [...td.querySelectorAll(".qt-win")].map((x) => x.querySelector(".qt-fig").textContent.replace(/\s+/g, " ").trim())),
          ]));
          return { heads, rows };
        });
        const tr = (s) => page.evaluate((x) => t(x), s);
        const [five, week, month, other] = [await tr("5 hours"), await tr("7 days"), await tr("Month"), await tr("Other")];
        // the lengths first, shortest first; no column for Mistral's month
        assert.deepEqual(table.heads.slice(2), [five, week, month, await tr("Image generation"), other], `${table.heads}`);
        const col = (name) => table.heads.indexOf(name) - 2;
        const cell = (u, c) => table.rows[u][col(c)];
        const all = (u) => table.rows[u].flat();

        // nothing whose length is known is left in Other
        for (const u of ["c@example.com", "d@example.com", "v@example.com", "z@example.com", "a@example.com", "m@example.com"])
          assert.deepEqual(cell(u, other), [], `${u}'s Other: ${cell(u, other)}`);
        assert.deepEqual(cell("c@example.com", week), ["64%"]);
        assert.deepEqual(cell("d@example.com", week), ["100%"]);
        // Volcengine's "Weekly" and "Month" in theirs, the column saying what they are
        assert.deepEqual(cell("v@example.com", week), ["100%"]);
        assert.deepEqual(cell("v@example.com", month), ["41%"]);
        // Zhipu's month of MCP calls in Month, named by what it counts
        assert.deepEqual(cell("z@example.com", month), ["MCP 99%"]);
        // Antigravity's pools by length, each named by its pool
        assert.deepEqual(cell("a@example.com", five), ["Gemini 11%", "Claude & GPT 30%"]);
        assert.deepEqual(cell("a@example.com", week), ["Gemini 23%", "Claude & GPT 64%"]);
        // Mistral's month in Month
        assert.deepEqual(cell("m@example.com", month), ["API 20%"]);
        // Cursor's Grok Bot by its week; its pools, of no length sent, in Other
        assert.deepEqual(cell("u@example.com", week), ["Grok Bot 7%"]);
        assert.deepEqual(cell("u@example.com", other).length, 4);
        assert.equal(cell("u@example.com", other)[0], `${await tr("Cursor Models")} 30%`);
        // every window is somewhere, once
        assert.equal(all("v@example.com").length, 3);
        assert.equal(all("a@example.com").length, 4);
        assert.equal(all("u@example.com").length, 5);

        // sorting by 5 hours reads a shared cell by its fuller window
        await page.locator(".qt thead th .qt-sort", { hasText: five }).click();
        assert.match(await page.$eval(".qt tbody tr:nth-child(1)", (x) => x.textContent), /a@example\.com/, "Antigravity's 30% first");
        if (shots) await page.screenshot({ path: path.join(shots, `lengths-${engine}-${lang}-${width}.png`), fullPage: true });

        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert.ok(over <= 0, `the page scrolls sideways by ${over}px`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
