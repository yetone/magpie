// Run with Node's test runner and Playwright on the module path; see README.md.
// ysicing_01858 on Discord: under Use before expiry an account at 90% of
// its week, renewing today, took few requests. 90% of any window held an
// account back, the week it would lose at its reset too. A week at 90%
// alone (weekLow from the trace) now keeps its place under Smart and Use
// before expiry, and its row doesn't say "kept for last"; one at 90% of its
// five hours still does, now under Use before expiry too, which said
// nothing before. In every language, at a window's width and at 420px, in
// both engines.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const iso = (ms) => new Date(ms).toISOString();
const H = 3600e3;
const M = "gpt-5.6-sol";

// the first has room and renews in 48h, the second is at 91% of a week
// renewing in 3h (weekLow), the third at 93% of its five hours
const acct = (i, used, h, routing, weekLow) => ({ id: `cx#a${i}`, provider: "codex", name: "Codex", who: `cx-${i}@example.com`, kind: "account", model: M,
  routing, known: true, used, weekLow, renews: [iso(now + h * H + 5 * 60e3)], usedRenews: iso(now + h * H + 5 * 60e3) });
let route;
const routeFor = (routing) => ({
  id: 3, seq: 3, time: iso(now - 2000), agent: "claude", model: M, provider: "codex", done: true, status: 200, ms: 900, tokens: 10,
  order: [acct(1, 91, 3, routing, true), acct(2, 30, 48, routing, false), acct(3, 93, 2, routing, false)],
  tries: [{ id: "cx#a1", model: M, start: iso(now - 1500), done: true, status: 200 }],
});

function serve(lang) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light", quotaLeft: false } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 3, totals: { requests: 1, rerouted: 0, errors: 0 }, routes: [route] });
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const kept = { en: "kept for last", zh: "留到最后", "zh-TW": "留到最後", ja: "最後に回す", de: "für zuletzt aufgespart" };
const rowText = async (page, who) => (await page.locator(".rt-accts > li", { hasText: who }).first().textContent()).trim();

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a week at 90% renewing soon isn't called kept for last`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const routing of ["pace", ""]) {
      for (const lang of Object.keys(kept)) {
        for (const width of [1100, 420]) {
          await t.test(`${routing || "smart"} ${lang} at ${width}px`, async () => {
            route = routeFor(routing);
            const ctx = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
            try {
              const page = await ctx.newPage();
              page.setDefaultTimeout(5000);
              page.setDefaultNavigationTimeout(60000);
              const errors = [];
              page.on("pageerror", (e) => errors.push(e.message));
              await page.route("**/*", serve(lang));
              await page.goto("http://magpie.test/?view=routing");
              await page.locator(".rt-accts > li", { hasText: "cx-3@example.com" }).first().waitFor();
              const want = kept[lang];
              await page.waitForFunction(([w]) => [...document.querySelectorAll(".rt-accts > li")].some((l) => l.textContent.includes("cx-3@example.com") && l.textContent.includes(w)), [want], { timeout: 4000 }).catch(() => {});
              const five = await rowText(page, "cx-3@example.com");
              assert.ok(five.includes(want), `${routing} ${lang} ${width}: five hours at 93% says "${five}", not "${want}"`);
              const week = await rowText(page, "cx-1@example.com");
              assert.ok(!week.includes(want), `${routing} ${lang} ${width}: a week at 91% renewing in 3h says "${week}"`);
              const lows = await page.evaluate(() => [...document.querySelectorAll(".rt-accts > li.low")].map((l) => l.textContent.includes("cx-1@") ? 1 : l.textContent.includes("cx-3@") ? 3 : 0));
              assert.deepEqual(lows, [3], `${routing} ${lang} ${width}: rows dimmed as low`);
              const over = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
              assert.ok(over <= 1, `${lang} at ${width}: ${over}px past`);
              assert.deepEqual(errors, []);
            } finally {
              await ctx.close();
            }
          });
        }
      }
    }
  });
}
