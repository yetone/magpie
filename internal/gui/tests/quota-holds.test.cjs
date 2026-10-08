// Run with Node's test runner and Playwright on the module path; see README.md.
// Chiao on Discord: an account card's window says what the whole window
// holds, reckoned from what magpie routed through the account in it over
// the share used — "≈ N tokens · ≈ $X" under its reset, in every language,
// at a phone's width too, and in its tooltip how it was reckoned and that
// it reads low if the account is used elsewhere. A window the server didn't
// reckon says nothing, and one with no API price says no cost.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const soon = (h) => new Date(Date.now() + h * 3600e3).toISOString();
const holds = (tokens, cost, used, priced = true) => ({ tokens, cost, priced, used, since: new Date().toISOString(), routed: { calls: 41, tokens: Math.round(tokens * used / 100), cacheRead: 52345678, cost: cost * used / 100 } });
const quotas = [
  { provider: "claude", name: "Claude Code", icon: "claude-color", plan: "max", user: "dee@example.com", windows: [
    { name: "5 hours", used: 20, resetsAt: soon(2), holds: holds(12345678, 85.4321, 20) },
    { name: "7 days", used: 40, resetsAt: soon(70), holds: holds(234567890, 1234.5, 40) },
    { name: "7 days · Fable", used: 3, resetsAt: soon(70) },
  ] },
  { provider: "kiro", name: "Kiro", icon: "kiro-color", plan: "Pro", user: "kay@example.com", windows: [
    { name: "Weekly", used: 25, holds: holds(40000, 0, 25, false) },
  ] },
];
function serve(lang) {
  const settings = { lang, theme: "light", currency: "usd", chineseUnits: lang.startsWith("zh") };
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

const want = {
  en: { line: "Whole ≈ 12M tokens", low: "reads low" },
  zh: { line: "整窗 ≈ 1234.6 万 tokens", low: "偏低" },
  "zh-TW": { line: "整窗 ≈ 1234.6 萬 tokens", low: "偏低" },
  ja: { line: "枠全体 ≈ 12M トークン", low: "低め" },
  de: { line: "Gesamt ≈ 12M Tokens", low: "zu niedrig" },
};

for (const [name, engine] of [["webkit", webkit], ["chromium", chromium]]) {
  test(`${name}: a window's whole, under its reset, whole at 360 and 440px in every language`, async () => {
    const browser = await engine.launch();
    try {
      for (const lang of Object.keys(want)) {
        for (const width of [360, 440, 1000]) {
          const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          await page.route("**/*", serve(lang));
          await page.goto("http://magpie.test/?view=usage");
          await page.locator(".subscription-card .quota-holds").nth(2).waitFor();
          const got = await page.evaluate(() => {
            const lines = [...document.querySelectorAll(".quota-holds")];
            return {
              count: document.querySelectorAll(".quota").length,
              lines: lines.map((l) => {
                const card = l.closest(".subscription-card").getBoundingClientRect(), b = l.getBoundingClientRect();
                return {
                  text: l.textContent, title: l.title,
                  window: l.closest(".quota").querySelector(".quota-labels > span").textContent,
                  inReset: !!l.closest(".quota-reset"),
                  cut: [...l.children].some((s) => s.scrollWidth > s.clientWidth + 1 || s.getBoundingClientRect().right > card.right + 1),
                  inCard: b.left >= card.left - 1 && b.right <= card.right + 1,
                };
              }),
              pageScroll: document.documentElement.scrollWidth > document.documentElement.clientWidth + 1,
            };
          });
          const at = `${lang} ${width}px`;
          assert.equal(got.count, 4, at);
          assert.equal(got.lines.length, 3, `${at}: the Fable week, too little used, says nothing: ${JSON.stringify(got.lines)}`);
          const [five, week, kiro] = got.lines;
          assert.equal(five.text, want[lang].line + " · ≈ $85.43", at);
          assert.match(week.text, /≈ \$1235$/, at);
          assert.doesNotMatch(kiro.text, /\$/, `${at}: no API price, no cost`);
          for (const l of got.lines) {
            assert.ok(l.inReset, `${at}: ${l.text} isn't on the reset's line`);
            assert.ok(!l.cut && l.inCard, `${at}: "${l.text}" is cut or past its card`);
            assert.ok(l.title.includes(want[lang].low), `${at}: the tooltip doesn't say it reads low: ${l.title}`);
            assert.ok(l.title.includes("41") && l.title.split("\n").length === 4, `${at}: ${l.title}`);
          }
          assert.ok(five.title.includes("$85.43") && five.title.includes("20%"), at);
          assert.ok(!kiro.title.includes("$"), at);
          assert.equal(got.pageScroll, false, `${at}: the page scrolls sideways`);
          // a click on it moves nothing
          const before = await page.evaluate(() => [scrollY, document.querySelector("#view-usage")?.scrollTop]);
          await page.locator(".quota-holds").first().click();
          await page.waitForTimeout(100);
          assert.deepEqual(await page.evaluate(() => [scrollY, document.querySelector("#view-usage")?.scrollTop]), before, at);
          await page.close();
        }
      }
    } finally { await browser.close(); }
  });
}
