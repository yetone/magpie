// Run with Node's test runner and Playwright on the module path; see README.md.
// JonahFu on X: In came to include the cache (01e13f0e0 draws the whole
// prompt in Requests' In where the cache columns are left out), and they
// wanted it as before. Settings › Usage › Advanced › In without cache: Off
// at first, the window as main has it; On saves inUncached, another save
// keeps it, and the Requests list's In is then the vendor's in in both
// layouts, the header saying so. The row is in a page served to a browser
// too. Every language, wide and narrow, Chromium and WebKit; the API is
// faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, reach } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const row = (i, extra) => ({
  t: new Date(now - (i + 1) * 60e3).toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color",
  provider: "claude", providerName: "Claude", providerAccount: "a@example.com", req: "claude-opus-5", model: "claude-opus-5", served: "claude-opus-5",
  ms: 3000, status: 200, rid: "r-" + i, ep: "/v1/messages", cost: 0.01, priced: true, source: "log", ...extra,
});
const ROWS = [row(0, { in: 2, out: 900, cache_read: 52000, cache_write: 8400 }), row(1, { in: 1234, out: 50, cache_read: 0, cache_write: 0 })];

function server(lang, web, posts, start) {
  const fixed = {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", dock: false, dockWindow: false, proxy: "",
    redact: false, redactWords: [], codexWarmup: "", claudeWarmup: "", trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.900", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425", proxyNow: "none", proxySource: "none",
    visionModels: [], imageGenModels: [], lanURLs: [], fx: { rate: 7.2, at: new Date().toISOString(), stale: false }, ...start,
  };
  let cur = fixed;
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: cur, fx: fixed.fx });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") {
        const body = req.postDataJSON();
        posts.push(body);
        cur = { ...fixed, ...body };
      }
      return json(cur);
    }
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: new Date(now).toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/requests") return json({ period: url.searchParams.get("period"), rows: ROWS, offset: 0, total: ROWS.length, calls: ROWS.length, errors: 0, input: 1236, output: 950, cache_read: 52000, cache_write: 8400, reasoning: 0, cost: 0.02, unpriced: 0, agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }] });
    if (url.pathname === "/api/usage/requests/content") return json({ found: false, why: "read" });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 2, errors: 0, input: 1236, output: 950, cache_read: 52000, cache_write: 8400, reasoning: 0, unpriced: 0, cost: 0.02, bucket: "day", series: [], agents: [], models: [], path: "~/.config/magpie/usage.jsonl" });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { name: "In without cache", off: "Off", on: "On" },
  zh: { name: "输入不含缓存", off: "关闭", on: "开启" },
  "zh-TW": { name: "輸入不含快取", off: "關閉", on: "開啟" },
  ja: { name: "キャッシュを除いた入力", off: "オフ", on: "オン" },
  de: { name: "Eingabe ohne Cache", off: "Aus", on: "An" },
};

async function open(browser, width, lang, web, posts, start, where) {
  const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", server(lang, web, posts, start));
  await watchSettingsReads(page);
  await page.goto("http://magpie.test/" + where);
  return { context, page, errors };
}

// what Requests' In shows, in each layout the window can have: ledFit sets
// .tight for the width, toggled here to read the other too
async function ledgerIn(page) {
  await page.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
  await page.locator(".led tbody tr.led-row").nth(ROWS.length - 1).waitFor({ timeout: 15000 });
  return page.evaluate(() => {
    const table = document.querySelector(".led"), heads = [...table.tHead.rows[0].cells];
    const i = heads.findIndex((h) => h.textContent === t("In"));
    const cell = (r) => table.tBodies[0].rows[r].cells[i];
    const says = (c) => (c.innerText + (getComputedStyle(c, "::after").content.match(/^"(.*)"$/)?.[1] || "")).trim();
    const tight = table.classList.contains("tight");
    const read = (on) => { table.classList.toggle("tight", on); return { cached: says(cell(0)), plain: says(cell(1)) }; };
    const wide = read(false), narrow = read(true);
    table.classList.toggle("tight", tight);
    return { wide, narrow, head: heads[i].title, own: fmtN(2), whole: fmtN(60402), plain: fmtN(1234),
      shortTip: t("Input not read from or written to the cache"), longTip: t("Input not read from or written to the cache. Where the cache columns are left out, the whole prompt") };
  });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 390]) {
    test(`${engine} ${width}: In without cache is set in Settings › Usage › Advanced and keeps Requests' In the vendor's`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const lang of Object.keys(words)) {
        await t.test(lang, async () => {
          const w = words[lang];
          // the row, in the app and in a page served to a browser
          for (const web of [false, true]) {
            const posts = [];
            const { context, page, errors } = await open(browser, width, lang, web, posts, {}, "?view=settings&tab=usage");
            try {
              await settingsDrawn(page);
              const posted = async (n) => { for (let i = 0; i < 100 && posts.length < n; i++) await page.waitForTimeout(20); assert.equal(posts.length, n, "posts"); return posts[n - 1]; };
              assert.equal(await page.locator("#usageMore").evaluate((d) => d.open), false, "folded under Advanced while off");
              assert.equal(await page.locator("#usageMore > .list > #inUncachedRow").count(), 1, "under Advanced");
              await page.locator("#usageMore > summary").click();
              const opts = page.locator("#inUncachedSegs .opt");
              await opts.first().waitFor();
              assert.equal((await page.locator("#inUncachedRow .name").textContent()).trim(), w.name);
              assert.deepEqual(await opts.allTextContents(), [w.off, w.on]);
              assert.equal(await page.locator("#inUncachedSegs .opt.on").textContent(), w.off, "off until turned on");
              assert.equal(await page.locator("#trayCurrencyRow").isVisible(), !web, "the menu bar's row is the app's alone");
              const scrolled = () => page.locator("#view-settings").evaluate((v) => v.scrollTop);
              await reach(page, opts.nth(1));
              const top = await scrolled();
              await opts.nth(1).click();
              assert.equal((await posted(1)).inUncached, true, "On saves it");
              await page.locator("#inUncachedSegs .opt.on").filter({ hasText: w.on }).waitFor();
              await page.waitForTimeout(300);
              assert.equal(await scrolled(), top, "the click moved the page");
              // another setting saved later keeps it
              const day = page.locator("#dayStartSegs .opt").nth(2);
              await reach(page, day);
              await day.click();
              const second = await posted(2);
              assert.equal(second.dayStart, 2, "the other setting saved");
              assert.equal(second.inUncached, true, "kept by another save");
              const off = page.locator("#inUncachedSegs .opt").nth(0);
              await reach(page, off);
              await off.click();
              assert.equal((await posted(3)).inUncached, false, "Off saves false");
              const over = await page.evaluate(() => {
                const r = document.querySelector("#inUncachedRow").getBoundingClientRect();
                return [...document.querySelectorAll("#inUncachedRow *")].filter((e) => e.getBoundingClientRect().right > Math.max(r.right, innerWidth) + 1).map((e) => e.className || e.id);
              });
              assert.deepEqual(over, [], "nothing past the edge");
              assert.deepEqual(errors, []);
            } finally {
              await context.close();
            }
          }

          // Off, as main has it: the whole prompt where the cache columns are left out
          {
            const { context, page, errors } = await open(browser, width, lang, true, [], {}, "?view=usage");
            try {
              const got = await ledgerIn(page);
              assert.equal(got.wide.cached, got.own);
              assert.equal(got.narrow.cached, got.whole, "off: the whole prompt with the cache columns left out");
              assert.equal(got.head, got.longTip);
              assert.deepEqual(errors, []);
            } finally {
              await context.close();
            }
          }
          // On: the vendor's in in both layouts
          {
            const { context, page, errors } = await open(browser, width, lang, true, [], { inUncached: true }, "?view=usage");
            try {
              const got = await ledgerIn(page);
              assert.equal(got.wide.cached, got.own, "beside the cache columns, the vendor's in");
              assert.equal(got.narrow.cached, got.own, "with them left out, still the vendor's in");
              for (const v of [got.wide, got.narrow]) assert.equal(v.plain, got.plain);
              assert.equal(got.head, got.shortTip, "the header says In leaves the cache out");
              if (lang !== "en") assert.notEqual(got.shortTip, "Input not read from or written to the cache", "translated");
              assert.deepEqual(errors, []);
            } finally {
              await context.close();
            }
          }
        });
      }
    });
  }
}
