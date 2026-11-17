// Run with Node's test runner and Playwright on the module path; see README.md.
// yuushabu_buyinn on X: Baidu Qianfan's Token Plan Personal also holds
// credit packs, Baidu's gifts (赠送积分包) among them, and the Usage card
// counted only the month. The card now has a "Gift credits" row and a
// "Credit packs" row under the month, each as credits used of the packs'
// total and when the soonest left runs out, named in the reader's
// language, at a phone's width too. Packs magpie couldn't read are an
// "Unknown" row, never 0 left; an account with none has the month alone.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const soon = (h) => new Date(Date.now() + h * 3600e3).toISOString();
const month = { name: "Month", used: 25, amount: 11250, limit: 45000, unit: "credits", resetsAt: soon(24 * 14), credits: true };
// one account a card: several of one provider fold into one card
const card = (id, windows) => ({ provider: id, name: "Baidu Qianfan " + id.slice(-1), icon: "baiducloud-color", plan: "Token Plan Pro", user: "u", windows });
const quotas = [
  card("qianfan-a", [month,
    { name: "Gift credits", used: 100 * 1200 / 7000, amount: 1200, limit: 7000, unit: "credits", runsOutAt: soon(30), runsOut: 3800 },
    { name: "Credit packs", used: 25, amount: 2500, limit: 10000, unit: "credits", runsOutAt: soon(24 * 300), runsOut: 7500 }]),
  card("qianfan-b", [{ ...month, credits: false }, { name: "Credit packs", used: 0, text: true, display: "Unknown" }]),
  card("qianfan-c", [{ ...month, credits: false }]),
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
  en: { gift: "Gift credits", packs: "Credit packs", unknown: "Unknown", expire: /^3,?800 credits expire .+ · in (29|30)h$/ },
  zh: { gift: "赠送积分包", packs: "积分包", unknown: "未知", expire: /^3,?800 积分 将于 .+ 过期 · (29|30) 小时后$/ },
  "zh-TW": { gift: "贈送積分包", packs: "積分包", unknown: "未知", expire: /^3,?800 積分 將於 .+ 過期 · (29|30) 小時後$/ },
  ja: { gift: "ギフトクレジット", packs: "クレジットパック", unknown: "不明", expire: /^3,?800 クレジットは .+ に失効 · (29|30) ?時間後$/ },
  de: { gift: "Geschenkte Credits", packs: "Credit-Pakete", unknown: "Unbekannt", expire: /^3[.,]?800 Credits verfallen .+ · in (29|30) Std\.?$/ },
};

for (const [name, engine] of [["webkit", webkit], ["chromium", chromium]]) {
  test(`${name}: Qianfan's gift and bought credit packs are rows of their own beside the month, in every language, at 360, 440 and 1000px`, async () => {
    const browser = await engine.launch();
    try {
      for (const [lang, w] of Object.entries(words)) {
        for (const width of [360, 440, 1000]) {
          const page = await (await browser.newContext({ viewport: { width, height: 1200 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          await page.route("**/*", serve(lang));
          await page.goto("http://magpie.test/?view=usage");
          await page.locator(".subscription-card").nth(2).locator(".quota").first().waitFor();
          const got = await page.evaluate(() => ({
            cards: [...document.querySelectorAll(".subscription-card")].map((c) => {
              const box = c.getBoundingClientRect();
              return [...c.querySelectorAll(".quota")].map((q) => {
                const b = q.getBoundingClientRect();
                return { text: q.textContent.replace(/\s+/g, " ").trim(), reset: q.querySelector(".quota-reset")?.textContent.replace(/\s+/g, " ").trim() ?? "",
                  inCard: b.left >= box.left - 1 && b.right <= box.right + 1 };
              });
            }),
            pageScroll: document.documentElement.scrollWidth > document.documentElement.clientWidth + 1,
          }));
          const at = `${lang} ${width}px`;
          assert.equal(got.cards.length, 3, at);
          const [a, b, c] = got.cards;
          assert.equal(a.length, 3, `${at}: month, gift and bought rows`);
          assert.ok(a[1].text.includes(w.gift), `${at}: gift row "${a[1].text}"`);
          assert.ok(/1,?200|1\.200/.test(a[1].text) && /7,?000|7\.000/.test(a[1].text), `${at}: gift amounts "${a[1].text}"`);
          assert.match(a[1].reset, w.expire, at);
          assert.ok(a[2].text.startsWith(w.packs) || a[2].text.includes(w.packs), `${at}: bought row "${a[2].text}"`);
          assert.ok(/10,?000|10\.000/.test(a[2].text), `${at}: bought total "${a[2].text}"`);
          assert.equal(b.length, 2, `${at}: packs not read are one row`);
          assert.ok(b[1].text.includes(w.packs) && b[1].text.includes(w.unknown), `${at}: unknown row "${b[1].text}"`);
          assert.ok(!/\b0%|100%/.test(b[1].text), `${at}: unknown packs read as a share "${b[1].text}"`);
          assert.equal(c.length, 1, `${at}: no packs, the month alone`);
          for (const q of [...a, ...b, ...c]) assert.ok(q.inCard, `${at}: "${q.text}" is past its card`);
          assert.equal(got.pageScroll, false, `${at}: the page scrolls sideways`);
          await page.close();
        }
      }
    } finally { await browser.close(); }
  });
}
