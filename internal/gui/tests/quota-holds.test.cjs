// Run with Node's test runner and Playwright on the module path; see README.md.
// Chiao on Discord: an account card's window says what the whole window
// holds, reckoned from what magpie routed through the account in it over
// the share used — "≈ N tokens · ≈ $X" under its reset, in every language,
// at a phone's width too, and in its tooltip how it was reckoned and that
// it reads low if the account is used elsewhere. A window the server didn't
// reckon says "not enough data", muted, with why in its tooltip (#2045),
// one it gives no reason for says nothing, and one with no API price says
// no cost.
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
    { name: "7 days · Fable", used: 3, resetsAt: soon(70), holdsWhy: "low" },
    { name: "7 days · Sonnet", used: 30, resetsAt: soon(70), holdsWhy: "credits" },
    { name: "7 days · Haiku", used: 30, resetsAt: soon(70), holdsWhy: "some reason this GUI doesn't know" },
  ] },
  { provider: "kiro", name: "Kiro", icon: "kiro-color", plan: "Pro", user: "kay@example.com", windows: [
    { name: "Weekly", used: 25, holds: holds(40000, 0, 25, false) },
    { name: "Credits", used: 30, holdsWhy: "clock" },
  ] },
];
function serve(lang, cards = quotas, summary) {
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
    if (url.pathname === "/api/usage/quotas") return json(cards);
    if (url.pathname === "/api/usage" && summary) return json(summary);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

const want = {
  en: { line: "Whole ≈ 12M tokens", low: "reads low", none: "Whole: not enough data", under: "Under 5%", credits: "credits", clock: "reset time" },
  zh: { line: "整窗 ≈ 1234.6 万 tokens", low: "偏低", none: "整窗：数据不足", under: "不到 5%", credits: "积分", clock: "重置时间" },
  "zh-TW": { line: "整窗 ≈ 1234.6 萬 tokens", low: "偏低", none: "整窗：資料不足", under: "不到 5%", credits: "積分", clock: "重置時間" },
  ja: { line: "枠全体 ≈ 12M トークン", low: "低め", none: "枠全体：データ不足", under: "5% 未満", credits: "クレジット", clock: "リセット時刻" },
  de: { line: "Gesamt ≈ 12M Tokens", low: "zu niedrig", none: "Gesamt: zu wenig Daten", under: "Weniger als 5 %", credits: "Guthaben", clock: "Rücksetzzeit" },
};

for (const [name, engine] of [["webkit", webkit], ["chromium", chromium]]) {
  test(`${name}: a window's whole, or not enough data and why, under its reset, whole at 360 to 1100px in every language`, async () => {
    const browser = await engine.launch();
    try {
      for (const lang of Object.keys(want)) {
        for (const width of [360, 420, 440, 1000, 1100]) {
          const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
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
              unknown: [...document.querySelectorAll(".quota-holds-why")].map((l) => {
                const card = l.closest(".subscription-card").getBoundingClientRect(), b = l.getBoundingClientRect(), cs = getComputedStyle(l);
                return {
                  text: l.textContent, title: l.title, window: l.closest(".quota").querySelector(".quota-labels > span").textContent,
                  inReset: !!l.closest(".quota-reset"), cut: l.scrollWidth > l.clientWidth + 1, inCard: b.left >= card.left - 1 && b.right <= card.right + 1,
                  // muted as the reset line is: its caption colour, not faded below it
                  // (a card's captions read at 4.5:1, Jiabubu)
                  muted: cs.color === getComputedStyle(l.closest(".quota-reset")).color && cs.color !== getComputedStyle(l.closest(".quota")).color,
                };
              }),
              pageScroll: document.documentElement.scrollWidth > document.documentElement.clientWidth + 1,
            };
          });
          const at = `${lang} ${width}px`;
          assert.equal(got.count, 7, at);
          assert.equal(got.lines.length, 3, `${at}: only the windows reckoned tell a whole: ${JSON.stringify(got.lines)}`);
          // the ones not reckoned say so, and why; one with a reason this
          // GUI doesn't know says nothing
          assert.deepEqual(got.unknown.map((l) => l.text), [want[lang].none, want[lang].none, want[lang].none], `${at}: ${JSON.stringify(got.unknown)}`);
          const [fable, sonnet, credits] = got.unknown;
          assert.ok(fable.title.includes(want[lang].under), `${at}: ${fable.title}`);
          assert.ok(sonnet.title.includes(want[lang].credits), `${at}: ${sonnet.title}`);
          assert.ok(credits.title.includes(want[lang].clock), `${at}: ${credits.title}`);
          for (const l of got.unknown) {
            assert.ok(l.inReset && !l.cut && l.inCard && l.muted, `${at}: ${JSON.stringify(l)}`);
            assert.equal(l.title.split("\n").length, 2, at);
          }
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

// NorethSea (#5861): where a call paid the API's long-context premium, the
// server also sends the whole at base rates (baseCost). The figure is then a
// button, which says it is the API's: a click switches every card's figure
// to the base one and back,
// moves nothing, and is kept for the next load; the tooltip names both. A
// window with no baseCost keeps one plain figure.
const premium = (tokens, cost, base, used) => ({ ...holds(tokens, cost, used), baseCost: base });
const baseCards = [
  { provider: "codex", name: "Codex", icon: "openai", plan: "pro", user: "sam@example.com", windows: [
    { name: "5 hours", used: 20, resetsAt: soon(2), holds: holds(12345678, 85.4321, 20) },
    { name: "7 days", used: 40, resetsAt: soon(70), holds: premium(234567890, 1234.5, 777.25, 40) },
  ] },
  { provider: "claude", name: "Claude Code", icon: "claude-color", plan: "max", user: "kit@example.com", windows: [
    { name: "7 days", used: 50, resetsAt: soon(30), holds: premium(34567890, 312.5, 210.75, 50) },
  ] },
];
const base = { en: " base", zh: "（基础价）", "zh-TW": "（基礎價）", ja: "（基本料金）", de: " (Basispreis)" };
// and the API figure says it is one, beside the other
const api = { en: " API", zh: "（API 价）", "zh-TW": "（API 價）", ja: "（API 料金）", de: " (API-Preis)" };

for (const [name, engine] of [["webkit", webkit], ["chromium", chromium]]) {
  test(`${name}: a whole that paid long-context premiums switches to its base-rate figure on every card, in every language`, async () => {
    const browser = await engine.launch();
    try {
      for (const lang of Object.keys(base)) {
        for (const width of [360, 1000]) {
          const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
          const page = await context.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          await page.route("**/*", serve(lang, baseCards));
          await page.goto("http://magpie.test/?view=usage");
          await page.locator(".quota-holds-cost").nth(1).waitFor();
          const read = () => page.evaluate(() => [...document.querySelectorAll(".quota-holds")].map((l) => {
            const card = l.closest(".subscription-card").getBoundingClientRect(), b = l.getBoundingClientRect();
            return { text: l.textContent, title: l.title, button: l.querySelector("button")?.textContent ?? null,
              inCard: b.left >= card.left - 1 && b.right <= card.right + 1 };
          }));
          const at = `${lang} ${width}px`;
          let got = await read();
          assert.equal(got.length, 3, at);
          const [plain, week, kit] = got;
          // no premium paid: one plain figure, the tooltip as it was
          assert.equal(plain.button, null, at);
          assert.equal(plain.title.split("\n").length, 4, at);
          assert.equal(week.button, "≈ $1235" + api[lang], at);
          assert.equal(kit.button, "≈ $313" + api[lang], at);
          for (const l of [week, kit]) {
            assert.equal(l.title.split("\n").length, 6, `${at}: ${l.title}`);
            assert.ok(l.inCard, `${at}: ${l.text}`);
          }
          assert.ok(week.title.includes("$1235") && week.title.includes("$777"), `${at}: ${week.title}`);
          const before = await page.evaluate(() => [scrollY, document.querySelector("#view-usage")?.scrollTop]);
          await page.locator(".quota-holds-cost").first().click();
          got = await read();
          assert.ok(got[1].button.startsWith("≈ $777") && got[1].button.endsWith(base[lang]), `${at}: ${got[1].button}`);
          assert.ok(got[2].button.startsWith("≈ $211") && got[2].button.endsWith(base[lang]), `${at}: the other card didn't switch: ${got[2].button}`);
          assert.equal(got[0].text, plain.text, at);
          for (const l of got) assert.ok(l.inCard, `${at}: ${l.text}`);
          assert.deepEqual(await page.evaluate(() => [scrollY, document.querySelector("#view-usage")?.scrollTop]), before, at);
          // kept for the next load, and switched back
          await page.reload();
          await page.locator(".quota-holds-cost").nth(1).waitFor();
          assert.ok((await read())[2].button.endsWith(base[lang]), `${at}: not kept`);
          await page.locator(".quota-holds-cost").nth(1).click();
          got = await read();
          assert.deepEqual([got[1].button, got[2].button], [week.button, kit.button], at);
          await context.close();
        }
      }
    } finally { await browser.close(); }
  });
}

// Musikrv77852 on X: after a window resets with nothing used yet, the last
// window's whole stays (the server's holds.prevAt), its tooltip saying
// which window it was reckoned in, as a past cycle's does; a window that
// tells its own says nothing of the kind.
for (const [name, engine] of [["webkit", webkit], ["chromium", chromium]]) {
  test(`${name}: a whole kept from the last window says which window, in every language`, async () => {
    const browser = await engine.launch();
    try {
      for (const lang of Object.keys(want)) {
        for (const width of [360, 1000]) {
          const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const since = new Date(Date.now() - 8 * 86400e3).toISOString(), prevAt = new Date(Date.now() - 86400e3).toISOString();
          const cards = [{ provider: "kimi-code", name: "Kimi Code", icon: "kimi", plan: "pro", user: "mu@example.com", windows: [
            { name: "7 days", used: 0, resetsAt: soon(160), holds: { ...holds(12345678, 0, 40, false), since, prevAt } },
            { name: "5 hours", used: 20, resetsAt: soon(2), holds: holds(2345678, 0, 20, false) },
          ] }];
          await page.route("**/*", serve(lang, cards));
          await page.goto("http://magpie.test/?view=usage");
          await page.locator(".subscription-card .quota-holds").nth(1).waitFor();
          const got = await page.evaluate(() => [...document.querySelectorAll(".quota-holds")].map((l) => ({ text: l.textContent, title: l.title })));
          const at = `${lang} ${width}px`;
          assert.equal(got.length, 2, at);
          assert.ok(got[0].text.length > 0 && got[0].title.split("\n").length === 5, `${at}: the kept whole's tooltip says which window: ${got[0].title}`);
          assert.ok(got[0].title.split("\n")[0].length > 0 && got[1].title.split("\n").length === 4, `${at}: a window with its own says nothing: ${got[1].title}`);
          await page.close();
        }
      }
    } finally { await browser.close(); }
  });
}

// Anotiaww on X: the whole-window estimate follows the Overview's tokens
// pick. With cache, it adds the cache read and written magpie routed in the
// window, scaled by the same share used, and says so on the line and in the
// tooltip; picked on the tokens tile, the cards follow at once, the pick is
// kept for the next load, and picked back the line is as it was.
const cacheCard = [{ provider: "claude", name: "Claude Code", icon: "claude-color", plan: "max", user: "dee@example.com", windows: [
  { name: "5 hours", used: 20, resetsAt: soon(2), holds: (() => { const h = holds(12345678, 85.4321, 20); h.routed.cacheWrite = 1000000; return h; })() },
] }];
// 12345678 in and out, and (52345678 + 1000000) cache over 20% used
const withCacheTokens = 12345678 + (52345678 + 1000000) * 5;
const summary = { calls: 30, errors: 0, input: 5000000, output: 2000000, reasoning: 0, cache_read: 90000000, cache_write: 0, unpriced: 0, cost: 0, bucket: "day",
  series: [{ label: "Oct 15", time: new Date().toISOString(), calls: 30, input: 5000000, output: 2000000, cache_read: 90000000, cache_write: 0, cost: 0 }],
  path: "~/.config/magpie/usage.jsonl", agents: [], models: [], accounts: [], callerKeys: [] };
const withCache = {
  en: { line: (n) => `Whole ≈ ${n} tokens with cache`, tip: "cache read and written included" },
  zh: { line: (n) => `整窗 ≈ ${n} tokens（含缓存）`, tip: "含缓存读取与写入" },
  "zh-TW": { line: (n) => `整窗 ≈ ${n} tokens（含快取）`, tip: "含快取讀取與寫入" },
  ja: { line: (n) => `枠全体 ≈ ${n} トークン（キャッシュ込み）`, tip: "キャッシュの読み取りと書き込みを含む" },
  de: { line: (n) => `Gesamt ≈ ${n} Tokens mit Cache`, tip: "Cache-Lesen und -Schreiben eingerechnet" },
};

for (const [name, engine] of [["webkit", webkit], ["chromium", chromium]]) {
  test(`${name}: the whole follows the tokens tile's with-cache pick, in every language`, async () => {
    const browser = await engine.launch();
    try {
      for (const lang of Object.keys(withCache)) {
        for (const width of [360, 1000]) {
          const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
          const page = await context.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          await page.route("**/*", serve(lang, cacheCard, summary));
          await page.goto("http://magpie.test/?view=usage");
          await page.locator(".subscription-card .quota-holds").first().waitFor();
          await page.locator("#stats .kpi .kpi-pick").first().waitFor();
          const at = `${lang} ${width}px`;
          const read = () => page.locator(".quota-holds").first().evaluate((l) => {
            const card = l.closest(".subscription-card").getBoundingClientRect(), b = l.getBoundingClientRect();
            return { text: l.textContent, title: l.title, inCard: b.left >= card.left - 1 && b.right <= card.right + 1,
              cut: [...l.children].some((s) => s.scrollWidth > s.clientWidth + 1 || s.getBoundingClientRect().right > card.right + 1) };
          });
          const plain = await read();
          assert.equal(plain.text, want[lang].line + " · ≈ $85.43", at);
          assert.ok(!plain.title.includes(withCache[lang].tip), at);
          const n = await page.evaluate((v) => fmtN(v), withCacheTokens);
          // picked With cache on the tokens tile: the card follows at once
          await page.locator("#stats .kpi .kpi-pick").first().click();
          const menu = page.locator(".proto-menu:visible");
          await menu.waitFor();
          await menu.locator(".pm-item").nth(1).click();
          await page.waitForFunction((t) => document.querySelector(".quota-holds")?.textContent.startsWith(t), withCache[lang].line(n));
          let got = await read();
          assert.equal(got.text, withCache[lang].line(n) + " · ≈ $85.43", at);
          assert.ok(got.title.split("\n")[0].includes(withCache[lang].tip) && got.title.split("\n")[0].includes(n), `${at}: ${got.title}`);
          assert.equal(got.title.split("\n").length, 4, at);
          assert.ok(got.inCard && !got.cut, `${at}: ${JSON.stringify(got)}`);
          // kept for the next load
          await page.reload();
          await page.locator(".subscription-card .quota-holds").first().waitFor();
          assert.equal((await read()).text, withCache[lang].line(n) + " · ≈ $85.43", `${at}: not kept`);
          // and back without: as it was
          await page.locator("#stats .kpi .kpi-pick").first().click();
          await page.locator(".proto-menu:visible").waitFor();
          await page.locator(".proto-menu:visible .pm-item").nth(0).click();
          await page.waitForFunction((t) => document.querySelector(".quota-holds")?.textContent === t, plain.text);
          got = await read();
          assert.deepEqual([got.text, got.title], [plain.text, plain.title], at);
          await context.close();
        }
      }
    } finally { await browser.close(); }
  });
}
