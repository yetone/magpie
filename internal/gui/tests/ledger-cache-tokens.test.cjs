// Run with Node's test runner and Playwright on the module path; see README.md.
// #1839 (kensenzhao): a request's details say its cache read and cache
// creation beside in and out, at every width — not only where a narrow
// window leaves the row's cache columns out — and 0 as 0, so a prompt that
// missed the cache reads as that. A failure that spent nothing says no
// tokens. Back from a request's page is the list where it was, nothing
// spills out of the details, in
// every language, in Chromium and WebKit at 1100 and 390 wide; the API is
// faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const reader = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const row = (i, extra) => ({
  t: new Date(now - (i + 1) * 60e3).toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color",
  provider: "claude", providerName: "Claude", providerAccount: "a@example.com", req: "claude-opus-5", model: "claude-opus-5", served: "claude-opus-5",
  ms: 3000, status: 200, rid: "r-" + i, ep: "/v1/messages", cost: 0.01, priced: true, ...extra,
});
const ROWS = [
  // the prompt read from the cache, a little of it written for an hour
  row(0, { in: 3, out: 900, cache_read: 52000, cache_write: 400, cache_write_1h: 400 }),
  // the same turn with its cache missed: the whole prompt is input
  row(1, { in: 52403, out: 900, cache_read: 0, cache_write: 0 }),
  // refused before anything was spent
  row(2, { in: 0, out: 0, status: 429, err: "slow down", err_type: "rate_limit_error" }),
];

function serve(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: new Date(now).toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/requests") return json({ period: url.searchParams.get("period"), rows: ROWS, offset: 0, total: ROWS.length, calls: ROWS.length, errors: 1, input: 52406, output: 1800, cache_read: 52000, cache_write: 400, reasoning: 0, cost: 0.02, unpriced: 0, agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }] });
    if (url.pathname === "/api/usage/requests/content") return json({ found: false, why: "read" });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 3, errors: 1, input: 52406, output: 1800, cache_read: 52000, cache_write: 400, reasoning: 0, unpriced: 0, cost: 0.02, bucket: "day", series: [], agents: [], models: [], path: "~/.config/magpie/usage.jsonl" });
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// each language's label, and its words for in, out, cache read and cache
// creation, numbers as the language writes them
const W = {
  en: { dt: "Tokens", say: (i, o, r, w) => `${i} in · ${o} out · ${r} cache read · ${w} cache creation` },
  zh: { dt: "Token", say: (i, o, r, w) => `输入 ${i} · 输出 ${o} · 缓存读取 ${r} · 缓存创建 ${w}` },
  "zh-TW": { dt: "Token", say: (i, o, r, w) => `輸入 ${i} · 輸出 ${o} · 快取讀取 ${r} · 快取建立 ${w}` },
  ja: { dt: "トークン", say: (i, o, r, w) => `入力 ${i} · 出力 ${o} · キャッシュ読み取り ${r} · キャッシュ作成 ${w}` },
  de: { dt: "Token", say: (i, o, r, w) => `${i} Eingabe · ${o} Ausgabe · ${r} Cache-Lesen · ${w} Cache-Erstellung` },
};

// the locale the page writes numbers in for a language (intlLang)
const LOCALE = { en: "en", zh: "zh-CN", "zh-TW": "zh-TW", ja: "ja-JP", de: "de-DE" };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 390]) {
    test(`${engine} ${width}: a request's details say its cache read and cache creation`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const lang of Object.keys(W)) {
        await t.test(lang, async () => {
          const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
          const page = await context.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang));
          try {
            await page.goto("http://magpie.test/?view=usage");
            await page.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
            const rows = page.locator(".led tbody tr.led-row");
            await rows.nth(ROWS.length - 1).waitFor({ timeout: 15000 });
            const n = (v) => v.toLocaleString(LOCALE[lang]);
            const top = () => page.locator("#view-usage").evaluate((v) => v.scrollTop);
            const detail = async (i) => {
              await reader.inView(page, rows.nth(i));
              // clear of the sticky sideways scroll bar at the view's foot
              const v = await page.locator("#view-usage").boundingBox(), b = await rows.nth(i).boundingBox();
              if (b.y + b.height > v.y + v.height - 48) {
                await page.mouse.wheel(0, 120);
                await page.waitForTimeout(300);
              }
              const was = await top(), at = await page.evaluate(() => [scrollX, scrollY]);
              const box = await reader.openRequest(page, rows.nth(i));
              assert.deepEqual(await page.evaluate(() => [scrollX, scrollY]), at);
              const got = await box.evaluate((b, dt) => {
                const d = [...b.querySelectorAll("dt")].find((x) => x.textContent === dt);
                if (!d) return null;
                const dd = d.nextElementSibling, r = dd.getBoundingClientRect(), wrap = b.querySelector(".led-box").getBoundingClientRect();
                return { text: dd.textContent, shown: r.width > 0 && r.height > 0 && getComputedStyle(dd).display !== "none", inside: r.left >= wrap.left - 0.5 && r.right <= wrap.right + 0.5, border: getComputedStyle(dd).borderLeftWidth };
              }, W[lang].dt);
              await reader.closeRequest(page);
              assert.equal(await top(), was, "back to the list where it was left");
              return got;
            };
            const cached = await detail(0);
            assert(cached, "the details have a tokens row");
            // and how much of what was written was for an hour, as the row's tooltip says it
            const ttl = await page.evaluate(([m, h]) => t("{m} for 5 minutes, {h} for an hour", { m, h }), [n(0), n(400)]);
            if (lang !== "en") assert.notEqual(ttl, "0 for 5 minutes, 400 for an hour", "translated");
            assert.equal(cached.text, W[lang].say(n(3), n(900), n(52000), n(400)) + " · " + ttl);
            assert(cached.shown && cached.inside, JSON.stringify(cached));
            assert.equal(cached.border, "0px", "no left-border accent");
            const missed = await detail(1);
            assert.equal(missed.text, W[lang].say(n(52403), n(900), n(0), n(0)), "0 is said as 0");
            assert(missed.shown && missed.inside, JSON.stringify(missed));
            assert.equal(await detail(2), null, "a failure that spent nothing has no tokens row");
            // no page wider than the window
            assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "the page scrolls sideways");
            assert.deepEqual(errors, []);
          } finally {
            if (process.env.ARTIFACT_DIR) {
              await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
              await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${width}-${lang}-cache-tokens.png`) });
            }
            await context.close();
          }
        });
      }
    });
  }
}
