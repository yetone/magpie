// Run with Node's test runner and Playwright on the module path; see README.md.
// chen2652 on Discord: a fold of one provider's accounts on one plan, folded,
// says what they have left together, as the Usage page's strip of all of a
// provider's accounts does, and its bar shows it: the tightest window, a
// share the mean of the accounts (5 hours 40% used of 20, 40 and 60). Open,
// each account's row says its own and the fold says nothing of it.
// Accounts on different plans are not put together: their fold is as it
// was. In every language, at a window's width and at 420px, in both
// engines. ARTIFACT_DIR keeps a screenshot of each folded stage.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));
const now = Date.now();
const iso = (ms) => new Date(ms).toISOString();
const acct = (provider, who, plan) => ({ id: `${provider}@${who}`, provider, name: provider === "same" ? "Same Plan" : "Mixed Plans", who, kind: "account", model: "m-1", known: true, used: 10, plan });
const accts = [
  acct("same", "a@example.com", "Pro"), acct("same", "b@example.com", "Pro"), acct("same", "c@example.com", "Pro"),
  acct("mixed", "d@example.com", "Pro"), acct("mixed", "e@example.com", "Max"), acct("mixed", "f@example.com", "Pro"),
];
const route = {
  id: 7, seq: 7, time: iso(now - 2000), agent: "claude", model: "m-1", provider: "same", done: true, status: 200, ms: 900, tokens: 1500,
  order: accts, tries: [{ id: accts[0].id, model: "m-1", start: iso(now - 2000), done: true, ms: 800, status: 200 }],
};
const card = (provider, user, plan, five, seven, reset) => ({ provider, name: provider, kind: "subscription", user, plan, windows: [
  { name: "5 hours", used: five, resetsAt: iso(now + reset * 60e3) },
  { name: "7 days", used: seven, resetsAt: iso(now + 73 * 3600e3) },
] });
const cards = [
  card("same", "a@example.com", "Pro", 20, 10, 130), card("same", "b@example.com", "Pro", 40, 30, 200), card("same", "c@example.com", "Pro", 60, 20, 250),
  card("mixed", "d@example.com", "Pro", 20, 10, 130), card("mixed", "e@example.com", "Max", 40, 30, 200), card("mixed", "f@example.com", "Pro", 60, 20, 250),
];

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
      return json({ mine: true, now: new Date().toISOString(), seq: 7, totals: { requests: 1, rerouted: 0, errors: 0 }, routes: [route] });
    }
    if (url.pathname === "/api/usage/quotas") return json(cards);
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

// the fold's figure: the 5 hours window, the mean of 20, 40 and 60
const words = {
  en: ["5 hours 40% used", "The mean of 3 accounts on one plan"],
  zh: ["5 小时 已用 40%", "同一套餐 3 个账号的平均值"],
  "zh-TW": ["5 小時 已用 40%", "同一方案 3 個帳號的平均值"],
  ja: ["5 時間 使用済み 40%", "同じプランの 3 アカウントの平均"],
  de: ["5 Stunden 40% verbraucht", "Mittelwert von 3 Konten mit demselben Tarif"],
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a folded fold of accounts on one plan says what they have left together`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(words)) {
      for (const width of [1100, 420]) {
        await t.test(`${lang} at ${width}px`, async () => {
          const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
          try {
            const page = await ctx.newPage();
            page.setDefaultTimeout(5000);
            page.setDefaultNavigationTimeout(60000);
            const errors = [];
            page.on("pageerror", (e) => errors.push(e.message));
            await page.route("**/*", serve(lang));
            await page.goto("http://magpie.test/?view=routing");
            const fold = (name) => page.locator(".rt-accts > li.rt-fold", { has: page.locator(".who", { hasText: new RegExp(`^${name}$`) }) });
            const same = fold("Same Plan"), mixed = fold("Mixed Plans");
            await same.waitFor();
            await mixed.waitFor();
            await page.waitForFunction("typeof quotas !== \"undefined\" && Array.isArray(quotas) && quotas.length > 0");
            const look = (li) => li.evaluate((e) => ({
              open: e.classList.contains("open"), bar: !e.classList.contains("nobar") && getComputedStyle(e.querySelector(".bar")).display !== "none",
              fill: e.querySelector(".bar i").style.width, text: e.querySelector("em").textContent, tip: e.querySelector("em").title,
            }));
            // three accounts fold open: each says its own, the fold nothing of theirs
            let s = await look(same);
            assert.ok(s.open, "three accounts open by default");
            assert.ok(!s.bar, "an open fold has no bar of its own");
            assert.ok(!s.text.includes(words[lang][0]), `open: ${s.text}`);
            // folded: the figure first, and the bar at it
            await same.click();
            await page.waitForFunction(([w]) => [...document.querySelectorAll(".rt-accts > li.rt-fold:not(.open) em")].some((e) => e.textContent.startsWith(w)), [words[lang][0]]);
            s = await look(same);
            assert.ok(!s.open && s.bar, JSON.stringify(s));
            assert.equal(s.fill, "40%");
            assert.ok(s.text.startsWith(words[lang][0] + " · "), s.text);
            if (words[lang][1]) assert.ok(s.tip.includes(words[lang][1]), s.tip);
            assert.ok(s.tip.split("\n").length >= 3, "each window and how it was put together, on hover: " + s.tip);
            // no account is named in it
            assert.ok(!/@example\.com/.test(s.text.split(" · ")[0]), s.text);
            // different plans: folded, as it was
            await mixed.click();
            await page.locator(".rt-accts > li.rt-fold:not(.open)").nth(1).waitFor();
            const m = await look(mixed);
            assert.ok(!m.open && !m.bar, JSON.stringify(m));
            assert.ok(!m.text.includes(words[lang][0]), m.text);
            // nothing runs past the list
            const over = await same.evaluate((e) => Math.max(e.scrollWidth - e.clientWidth, document.documentElement.scrollWidth - innerWidth));
            assert.ok(over <= 1, `${lang} at ${width}: ${over}px past`);
            // open again: the figure and the bar go
            await same.click();
            await page.waitForFunction(() => document.querySelectorAll(".rt-accts > li.rt-fold.open").length === 1);
            s = await look(same);
            assert.ok(s.open && !s.bar && !s.text.includes(words[lang][0]), JSON.stringify(s));
            if (process.env.ARTIFACT_DIR) {
              await same.click();
              await page.waitForFunction(() => document.querySelectorAll(".rt-accts > li.rt-fold:not(.open)").length === 2);
              await page.locator(".rt-accts").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `fold-together-${engine}-${lang}-${width}.png`) });
            }
            assert.deepEqual(errors, []);
          } finally {
            await ctx.close();
          }
        });
      }
    }
  });
}
