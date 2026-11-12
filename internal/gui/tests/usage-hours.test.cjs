// Usage's own number of hours (eachg0 on X): the range menu's top row takes N,
// the page asks the backend for the rolling window "last:Nh", the chip names it,
// and N is kept on this device. English and Chinese, both engines; the API is faked.
// Usage's "24 hours" (mahiro233 on Discord): the day up to now, asked of the
// backend as the rolling window "last:24h", not since midnight as Today is.
// The reader's pick of a period is kept: the page opens on it again, until a
// way in (the menu bar's total) names another. Picked days are not kept.
// English and Chinese, both engines; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets"));
const dayKey = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
const back = (n) => { const d = new Date(); d.setHours(12, 0, 0, 0); d.setDate(d.getDate() - n); return d; };

function serve(lang, asked) {
  const settings = { theme: "light", lang, quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage") {
      const p = url.searchParams.get("period");
      asked.push(p);
      return json({ period: p, calls: 0, errors: 0, input: 0, output: 0, reasoning: 0, unpriced: 0, cost: 0, bucket: "day", series: [], agents: [], models: [], path: "" });
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}


const words = {
  en: { h30: "Last 30 hours", none: "No calls in the last 30 hours.", h1: "Last hour" },
  zh: { h30: "最近 30 小时", none: "最近 30 小时没有调用。", h1: "最近 1 小时" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: Usage over a number of hours the reader sets, kept`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const asked = [];
      await page.route("**/*", serve(lang, asked));
      const on = () => page.locator("#period .opt.on").textContent();
      const settle = () => page.waitForFunction(() => document.querySelector("#period .opt") && !document.querySelector("#period .opt:disabled"));
      const menu = async () => { await page.locator("#period .range-opt").click(); return page.locator(".range-menu .rm-hours input"); };

      await page.goto("http://magpie.test/?view=usage");
      await settle();
      let input = await menu();
      assert.equal(await input.inputValue(), "", "nothing is guessed for a first time");
      await input.fill("0");
      await input.press("Enter");
      assert.equal(await input.getAttribute("aria-invalid"), "true", "0 hours is no window");
      await input.fill("30");
      await input.press("Enter");
      await page.waitForFunction(() => document.querySelector("#view-usage .none"));
      assert.equal(asked.at(-1), "last:30h");
      assert.equal((await on()).trim(), w.h30);
      assert.ok((await page.locator("#view-usage .none").textContent()).startsWith(w.none));
      assert.equal(await page.locator(".range-menu").count(), 0, "the menu closes on the pick");

      // opened again: on the pick, and the menu has its number
      asked.length = 0;
      await page.reload();
      await settle();
      assert.equal(asked[0], "last:30h");
      assert.equal((await on()).trim(), w.h30);
      input = await menu();
      assert.equal(await input.inputValue(), "30");

      // 24 is the preset's own chip; 1 reads in the singular
      await input.fill("24");
      await input.press("Enter");
      await settle();
      assert.equal(asked.at(-1), "last:24h");
      assert.equal(await page.locator("#period .range-opt.on").count(), 0);
      input = await menu();
      await input.fill("1");
      await input.press("Enter");
      await settle();
      assert.equal((await on()).trim(), w.h1);

      // the menu bar's total names its own period, which isn't kept
      asked.length = 0;
      await page.goto("http://magpie.test/?view=usage&period=7d");
      await settle();
      assert.equal(asked[0], "7d");
      assert.deepEqual(errors, []);
    });
  }
}
