// Run with Node's test runner and Playwright on the module path; see README.md.
// @andy_chen1997 on X: a Claude Enterprise seat's card said "Allowance
// unavailable — hover for why", "Claude Code's /usage is temporarily
// unavailable". A seat whose plan has no 5-hour or weekly limits (Claude
// Code's usage report of no meters, or a usage-based seat) is now a text
// row that says so in the reader's language, not a failed read, beside its
// usage credits when it has them; a seat whose rows hold its 5 hours shows
// them. Every language, at 360 and 1000px, both engines.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const soon = (h) => new Date(Date.now() + h * 3600e3).toISOString();
const none = { name: "5-hour and weekly limits", used: 0, text: true, aside: true, display: "None on this plan" };
const card = (user, windows) => ({ provider: "claude", name: "Claude Code", plan: "enterprise", user, windows });
const quotas = [
  card("a@corp.example", [none]),
  card("b@corp.example", [none, { name: "Usage credits", used: 24, aside: true, extra: true, amount: 120, limit: 500, unit: "USD" }]),
  card("c@corp.example", [{ name: "5 hours", used: 37.5, resetsAt: soon(2) }]),
];
function serve(lang) {
  const settings = { lang, theme: "light", currency: "usd", chineseUnits: false };
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
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { name: "5-hour and weekly limits", none: "None on this plan", bad: "Allowance unavailable" },
  zh: { name: "5 小时和每周额度", none: "此套餐没有", bad: "暂时无法获取额度" },
  "zh-TW": { name: "5 小時與每週額度", none: "此方案沒有", bad: "暫時無法取得額度" },
  ja: { name: "5 時間と週間の利用枠", none: "このプランにはなし", bad: "利用枠を取得できません" },
  de: { name: "5-Stunden- und Wochenlimits", none: "Keine in diesem Tarif", bad: "Kontingent nicht verfügbar" },
};

for (const [name, engine] of [["webkit", webkit], ["chromium", chromium]]) {
  test(`${name}: a Claude plan with no 5-hour or weekly limits says so, not unavailable, in every language, at 360 and 1000px`, async () => {
    const browser = await engine.launch();
    try {
      for (const [lang, w] of Object.entries(words)) {
        for (const width of [360, 1000]) {
          const page = await (await browser.newContext({ viewport: { width, height: 1200 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang));
          await page.goto("http://magpie.test/?view=usage");
          await page.locator(".subscription-card .quota").first().waitFor();
          await page.waitForFunction(() => document.body.textContent.includes("c@corp.example"));
          const got = await page.evaluate(() => ({
            body: document.body.textContent.replace(/\s+/g, " "),
            rows: [...document.querySelectorAll(".subscription-card .quota")].map((q) => q.textContent.replace(/\s+/g, " ").trim()),
            pageScroll: document.documentElement.scrollWidth > document.documentElement.clientWidth + 1,
          }));
          const at = `${lang} ${width}px`;
          const said = got.rows.filter((r) => r.includes(w.name) && r.includes(w.none));
          assert.equal(said.length, 2, `${at}: rows ${JSON.stringify(got.rows)}`);
          assert.ok(!got.body.includes(w.bad), `${at}: a plan with no limits read as unavailable`);
          assert.ok(!said.some((r) => /\d%/.test(r)), `${at}: no limits read as a share ${JSON.stringify(said)}`);
          assert.ok(got.rows.some((r) => /24\s?%/.test(r)), `${at}: usage credits beside it ${JSON.stringify(got.rows)}`);
          assert.ok(got.rows.some((r) => /37[.,]5\s?%/.test(r)), `${at}: the rows' 5 hours ${JSON.stringify(got.rows)}`);
          assert.equal(got.pageScroll, false, `${at}: the page scrolls sideways`);
          assert.deepEqual(errors, [], at);
          await page.close();
        }
      }
    } finally { await browser.close(); }
  });
}
