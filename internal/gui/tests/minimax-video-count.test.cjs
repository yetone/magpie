// Run with Node's test runner and Playwright on the module path; see README.md.
// #1366: a MiniMax Token Plan's video windows are counted in videos, and
// MiniMax's own CLI (mmx quota show) shows them so, "4 / 5" of the day and
// "34 / 35" of the week left. The Usage page says the count before the
// share, used or left as the reader picks, in the reader's language, and
// the window's name is in it too; the general windows keep the share alone.
// Nothing is cut at a narrow or a wide window.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const later = (h) => new Date(Date.now() + h * 3600e3).toISOString();
// what /api/usage/quotas says of the reporter's plan: the windows
// readMiniMaxPlan reads from their /v1/token_plan/remains
const plan = {
  provider: "minimax-cn", name: "MiniMax (China)", kind: "plan", windows: [
    { name: "5 hours", used: 6, remaining: 94, resetsAt: later(0.03) },
    { name: "7 days", used: 7, remaining: 93, resetsAt: later(72) },
    { name: "Video · 24 hours", used: 20, remaining: 80, resetsAt: later(14), amount: 1, limit: 5, unit: "videos" },
    { name: "Video · 7 days", used: 2.86, remaining: 97.14, resetsAt: later(72), amount: 1, limit: 35, unit: "videos" },
  ],
};
// per language: the video windows' names, and their counts used and left
const want = {
  zh: { names: ["视频 · 24 小时", "视频 · 7 天"], used: ["1 / 5 个视频", "1 / 35 个视频"], left: ["4 / 5 个视频", "34 / 35 个视频"], general: "剩余 94%" },
  en: { names: ["Video · 24 hours", "Video · 7 days"], used: ["1 / 5 videos", "1 / 35 videos"], left: ["4 / 5 videos", "34 / 35 videos"], general: "94% left" },
  "zh-TW": { names: ["影片 · 24 小時", "影片 · 7 天"], used: ["1 / 5 個影片", "1 / 35 個影片"], left: ["4 / 5 個影片", "34 / 35 個影片"], general: "剩餘 94%" },
  ja: { names: ["動画 · 24 時間", "動画 · 7 日"], used: ["1 / 5 本", "1 / 35 本"], left: ["4 / 5 本", "34 / 35 本"], general: "残り 94%" },
  de: { names: ["Video · 24 Stunden", "Video · 7 Tage"], used: ["1 / 5 Videos", "1 / 35 Videos"], left: ["4 / 5 Videos", "34 / 35 Videos"], general: "94% übrig" },
};

function serve(lang, quotaLeft) {
  const settings = { lang, theme: "light", quotaLeft, currency: "usd" };
  const state = { agents: [], profiles: [], settings };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/gateway/trace") return url.searchParams.get("wait") ? new Promise(() => {}) : json({ mine: true, now: new Date().toISOString(), seq: 0, totals: {}, routes: [] });
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/usage/quotas") return json([plan]);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

for (const [name, engine] of [["chromium", chromium], ["webkit", webkit]]) {
  test(`${name}: MiniMax's video windows say their count of videos, in every language`, async () => {
    const browser = await engine.launch();
    try {
      for (const lang of Object.keys(want)) {
        for (const quotaLeft of [false, true]) {
          for (const width of [420, 1100]) {
            const at = `${lang} ${width}px ${quotaLeft ? "left" : "used"}`;
            const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
            page.setDefaultTimeout(5000);
            await page.route("**/*", serve(lang, quotaLeft));
            await page.goto("http://magpie.test/?view=usage");
            await page.locator(".subscription-card .quota-n").nth(3).waitFor();
            await page.waitForTimeout(200);
            const rows = await page.evaluate(() => [...document.querySelectorAll(".subscription-card .quota")].map((q) => {
              const labels = q.querySelector(".quota-labels"), n = q.querySelector(".quota-n");
              return {
                name: labels.firstElementChild.textContent,
                count: n.textContent,
                cut: [n, ...n.children, labels.firstElementChild].some((e) => e.scrollWidth > e.clientWidth + 1),
              };
            }));
            const w = want[lang];
            assert.equal(rows.length, 4, at);
            assert.deepEqual(rows.slice(2).map((r) => r.name), w.names, at);
            for (const [i, r] of rows.slice(2).entries()) {
              assert.ok(r.count.startsWith((quotaLeft ? w.left : w.used)[i] + " ·"), `${at}: "${r.count}"`);
            }
            // the general windows are told in shares only
            assert.ok(!rows[0].count.includes("/"), `${at}: "${rows[0].count}"`);
            if (quotaLeft) assert.equal(rows[0].count, w.general, at);
            for (const r of rows) assert.equal(r.cut, false, `${at}: "${r.name}" "${r.count}" is cut`);
            assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${at}: the page scrolls sideways`);
            await page.close();
          }
        }
      }
    } finally { await browser.close(); }
  });
}
