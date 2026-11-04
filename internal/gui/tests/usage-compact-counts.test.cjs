// Run with Node's test runner and Playwright on the module path; see README.md.
// skyhuang1994 on Discord: "用量的这个数据展示 可以遵循 数字单位的展示逻辑吗
// 看这么长串的数字有点懵逼". The Usage page's request rows said their tokens
// in full (52,345,678), and the Requests total, the line over the list, the
// ranking and the Routing page's counts said requests in full. Each says
// fmtN's short form now (52M, 123K, or 5234.6 万 with Settings' 万 / 亿), and
// its exact value on hover. In every language, both engines, at 1100 and
// 420 wide; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const IN = 1234567, OUT = 98765, READ = 52345678, WRITE = 400, CALLS = 123456;
const ALL = IN + OUT + READ + WRITE;
const ROWS = [{
  t: new Date(now - 60e3).toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color",
  provider: "claude", providerName: "Claude", providerAccount: "a@example.com", req: "claude-opus-5", model: "claude-opus-5", served: "claude-opus-5",
  ms: 3000, status: 200, rid: "r-0", ep: "/v1/messages", cost: 0.01, priced: true, in: IN, out: OUT, cache_read: READ, cache_write: WRITE,
}];
const tot = { calls: CALLS, errors: 0, input: IN, output: OUT, cache_read: READ, cache_write: WRITE, reasoning: 0, cost: 2, unpriced: 0 };
const ledger = {
  period: "today", rows: ROWS, offset: 0, total: CALLS, ...tot, bucket: "hour", series: [],
  by: { provider: [{ id: "claude", name: "Claude", icon: "claude-color", ...tot }], agent: [{ id: "claude", name: "Claude Code", icon: "claudecode-color", ...tot }], model: [{ id: "claude-opus-5", name: "claude-opus-5", ...tot }] },
  agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }], providers: [{ id: "claude", name: "Claude", icon: "claude-color" }],
};

function serve(lang, units) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", chineseUnits: units } });
    if (url.pathname === "/api/settings") return json({ lang, theme: "light", chineseUnits: units });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date(now).toISOString(), seq: 1, totals: { requests: CALLS, rerouted: 1234, errors: 7 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [{ day: "2026-11-03", requests: 23456 }], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/requests") return json(ledger);
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ ...tot, bucket: "day", series: [], agents: [], models: [] });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const LOCALE = { en: "en", zh: "zh-CN", "zh-TW": "zh-TW", ja: "ja-JP", de: "de-DE" };
// the short forms: K / M by default, 万 / 亿 in Chinese with Settings' units
const KMB = { [IN]: "1.2M", [OUT]: "98.8K", [READ]: "52M", [CALLS]: "123K", [ALL]: "54M", 1234: "1.2K", 23456: "23.5K" };
const WAN = { [IN]: "123.5 万", [OUT]: "9.9 万", [READ]: "5234.6 万", [CALLS]: "12.3 万", [ALL]: "5367.9 万", 1234: "1234", 23456: "2.3 万" };
const CASES = [...Object.keys(LOCALE).map((lang) => [lang, false]), ["zh", true], ["zh-TW", true]];
const short = (lang, units, n) => units ? WAN[n].replace("万", lang === "zh-TW" ? "萬" : "万") : KMB[n];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: the Usage and Routing pages say counts short, exact on hover`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const [lang, units] of CASES) {
      for (const width of [1100, 420]) {
        await t.test(`${lang}${units ? " 万" : ""} at ${width}px`, async () => {
          const ctx = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
          try {
            const page = await ctx.newPage();
            page.setDefaultTimeout(5000);
            page.setDefaultNavigationTimeout(60000);
            const errors = [];
            page.on("pageerror", (e) => errors.push(e.message));
            await page.route("**/*", serve(lang, units));
            const s = (n) => short(lang, units, n), x = (n) => n.toLocaleString(LOCALE[lang]);
            const at = `${lang}${units ? " 万" : ""} ${width}`;

            // the Usage page's requests: its row, its totals, the line over the list, the ranking
            await page.goto("http://magpie.test/?view=usage");
            await page.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
            await page.locator(".led tbody tr.led-row").first().waitFor({ timeout: 15000 });
            const cells = await page.locator(".led tbody tr.led-row").first().locator("td.n").evaluateAll((ts) => ts.map((c) => [c.textContent, c.title]));
            for (const n of [IN, OUT, READ]) {
              assert.ok(cells.some(([text, title]) => text === s(n) && title.split("\n")[0].split(" · ")[0] === x(n)), `${at}: no cell says ${s(n)} with ${x(n)} on hover: ${JSON.stringify(cells)}`);
            }
            const blocks = await page.locator("#ledKpi .blk").evaluateAll((bs) => bs.map((b) => [b.querySelector(".v").textContent, b.title]));
            assert.ok(blocks.some(([v, title]) => v === s(CALLS) && title === x(CALLS)), `${at}: no total says ${s(CALLS)} requests: ${JSON.stringify(blocks)}`);
            const sum = page.locator("#ledSum");
            const sumText = await sum.textContent();
            assert.ok(sumText.includes(s(CALLS)) && !sumText.includes(x(CALLS)), `${at}: the line says "${sumText}"`);
            assert.ok((await sum.getAttribute("title")).includes(x(CALLS)), `${at}: the line's tooltip lacks ${x(CALLS)}`);
            const rank = page.locator("#ledRank .rk").first();
            await rank.waitFor();
            const val = rank.locator(".rk-val");
            assert.equal((await val.textContent()).trim(), s(ALL), `${at}: the ranking's tokens`);
            assert.equal(await val.getAttribute("title"), x(ALL), `${at}: the ranking's tooltip`);
            assert.ok(!(await rank.textContent()).includes(x(CALLS)), `${at}: the ranking says requests in full`);
            let over = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
            assert.ok(over <= 1, `${at}: the Usage page is ${over}px past`);

            // the Routing page's counts and a day's requests
            await page.goto("http://magpie.test/?view=routing");
            const stat = page.locator(".rt-stats b").first();
            await page.waitForFunction((want) => document.querySelector(".rt-stats b")?.textContent === want, s(CALLS), { timeout: 5000 }).catch(() => {});
            assert.equal(await stat.textContent(), s(CALLS), `${at}: Routing's requests`);
            assert.equal(await stat.getAttribute("title"), x(CALLS), `${at}: Routing's requests on hover`);
            assert.equal(await page.locator(".rt-stats b").nth(1).textContent(), s(1234), `${at}: Routing's rerouted`);
            const day = page.locator(".rt-day small").first();
            await day.waitFor({ state: "attached" });
            assert.equal(await day.textContent(), s(23456), `${at}: a day's requests`);
            assert.equal(await day.getAttribute("title"), x(23456), `${at}: a day's requests on hover`);
            over = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
            assert.ok(over <= 1, `${at}: the Routing page is ${over}px past`);
            assert.deepEqual(errors, []);
          } finally {
            await ctx.close();
          }
        });
      }
    }
  });
}
