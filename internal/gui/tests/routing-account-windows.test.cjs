// Run with Node's test runner and Playwright on the module path; see README.md.
// #1833: the Routing page's Accounts and keys list shows a subscription
// account's windows each (Claude's 5 hours and 7 days), with when each
// resets, as the Usage page read them: its card matched by the agent the
// account is signed in to and its user, whatever their case. zwm_vincent
// on X: a row showed only the window routing went by, 5 hours on one and
// 7 days on the next, the other on hover. Every row now shows each window
// on a line of its own, its short name, a bar, what is used and when it
// resets, each starting at the same place on every row; the whole of each
// on hover. Nothing new is read for it: opening Routing asks
// /api/usage/quotas in the ordinary way, never ?asked=1. An account with
// no card of its own (another computer's card for the same user is not
// its) shows none. In every language, at a window's width and at 420px,
// in both engines.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const iso = (ms) => new Date(ms).toISOString();
const accts = [
  { id: "claude", provider: "claude", name: "Claude", who: "work@example.com", kind: "account", agent: "claude", model: "claude-opus-5", known: true, used: 40, plan: "Max" },
  { id: "claude@spare", provider: "claude", name: "Claude", who: "spare@example.com", kind: "account", agent: "claude", model: "claude-opus-5", known: false, plan: "Pro" },
  // routing went by its week: the row says both windows all the same
  { id: "claude@team", provider: "claude", name: "Claude", who: "team@example.com", kind: "account", agent: "claude", model: "claude-opus-5", known: true, used: 70, usedWindow: "7 days", plan: "Max" },
];
const route = {
  id: 7, seq: 7, time: iso(now - 2000), agent: "claude", model: "claude-opus-5", provider: "claude", done: true, status: 200, ms: 900, tokens: 1500,
  order: accts, tries: [{ id: "claude", model: "claude-opus-5", start: iso(now - 2000), done: true, ms: 800, status: 200 }],
};
const cards = [
  { provider: "claude", name: "Claude", kind: "subscription", user: "Work@Example.com", plan: "Max", windows: [
    { name: "5 hours", used: 40, resetsAt: iso(now + 130 * 60e3) },
    { name: "7 days", used: 12, resetsAt: iso(now + 73 * 3600e3) },
  ] },
  { provider: "claude", name: "Claude", kind: "subscription", user: "team@example.com", plan: "Max", windows: [
    { name: "5 hours", used: 5, resetsAt: iso(now + 250 * 60e3) },
    { name: "7 days", used: 70, resetsAt: iso(now + 30 * 3600e3) },
  ] },
  // another computer's reading of the spare account: not this one's
  { provider: "claude", name: "Claude · Home", kind: "subscription", user: "spare@example.com", from: "Home", windows: [{ name: "5 hours", used: 90 }] },
];

function serve(lang, asks) {
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
    if (url.pathname === "/api/usage/quotas") { asks.push(url.search); return json(cards); }
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

const words = {
  en: ["5 hours 40% used · resets in 2h", "7 days 12% used · resets in 3d"],
  zh: ["5 小时 已用 40% · 2 小时后重置", "7 天 已用 12% · 3 天后重置"],
  "zh-TW": ["5 小時 已用 40% · 2 小時後重置", "7 天 已用 12% · 3 天后重置"],
  ja: ["5 時間 使用済み 40% · 2 時間後にリセット", "7 日 使用済み 12% · 3 日後にリセット"],
  de: ["5 Stunden 40% verbraucht · Reset in 2 Std.", "7 Tage 12% verbraucht · Reset in 3 T."],
};
// each window's line, its short name, what is used and when it resets
const lines = {
  en: { work: [["5h", "40% used", "resets in 2h"], ["7d", "12% used", "resets in 3d"]], team: [["5h", "5% used", "resets in 4h"], ["7d", "70% used", "resets in 30h"]] },
  zh: { work: [["5h", "已用 40%", "2 小时后重置"], ["7d", "已用 12%", "3 天后重置"]], team: [["5h", "已用 5%", "4 小时后重置"], ["7d", "已用 70%", "30 小时后重置"]] },
  "zh-TW": { work: [["5h", "已用 40%", "2 小時後重置"], ["7d", "已用 12%", "3 天后重置"]], team: [["5h", "已用 5%", "4 小時後重置"], ["7d", "已用 70%", "30 小時後重置"]] },
  ja: { work: [["5h", "使用済み 40%", "2 時間後にリセット"], ["7d", "使用済み 12%", "3 日後にリセット"]], team: [["5h", "使用済み 5%", "4 時間後にリセット"], ["7d", "使用済み 70%", "30 時間後にリセット"]] },
  de: { work: [["5h", "40% verbraucht", "Reset in 2 Std."], ["7d", "12% verbraucht", "Reset in 3 T."]], team: [["5h", "5% verbraucht", "Reset in 4 Std."], ["7d", "70% verbraucht", "Reset in 30 Std."]] },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: an account in Accounts and keys shows its windows, as Usage read them`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(words)) {
      for (const width of [1100, 420]) {
        await t.test(`${lang} at ${width}px`, async () => {
          const ctx = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
          try {
            const page = await ctx.newPage();
            page.setDefaultTimeout(5000);
            page.setDefaultNavigationTimeout(60000);
            const errors = [], asks = [];
            page.on("pageerror", (e) => errors.push(e.message));
            await page.route("**/*", serve(lang, asks));
            await page.goto("http://magpie.test/?view=routing");
            const act = page.locator(".rt-act", { hasText: "work@example.com" }).first();
            const team = page.locator(".rt-act", { hasText: "team@example.com" }).first();
            // each window a line of its own on every row, whichever one
            // routing went by
            const said = (row) => row.locator(".wq > div").evaluateAll((ls) => ls.map((l) => [...l.querySelectorAll("span.k, span.v, span.r")].map((e) => e.textContent)));
            await page.waitForFunction(() => [...document.querySelectorAll(".rt-act")].filter((e) => e.querySelectorAll(".wq > div").length === 2).length === 2);
            assert.deepEqual(await said(act), lines[lang].work);
            assert.deepEqual(await said(team), lines[lang].team);
            for (const row of [act, team]) {
              assert.equal(await row.locator(".st").isVisible(), false, "no one figure besides the windows");
              assert.equal(await row.locator(".wins").count(), 0);
              assert.equal(await row.locator(":scope > .bar").count(), 0, "a bar each window, not one for the row");
              assert.equal(await row.locator(".wq .quota-track").count(), 2);
            }
            // the bars say what the windows say
            assert.deepEqual(await act.locator(".wq .quota-track i").evaluateAll((is) => is.map((i) => i.style.width)), ["40%", "12%"]);
            // the whole of each window on hover, the day it resets told
            const tip = await act.locator(".wq").getAttribute("title");
            assert.ok(tip.startsWith(words[lang][0].split(" · ")[0]), tip);
            assert.equal(tip.split("\n").length >= 2, true, tip);
            // each part starts at one place on every row, and on every line
            // of a row, and each reset ends at one place, on the line of
            // what is used or, where the row is too narrow, under it, on
            // every line alike (a line's parts measured from its row's left
            // edge)
            const cols = (row) => row.locator(".wq > div").evaluateAll((ls) => ls.map((l) => {
              const left = l.closest(".rt-act").getBoundingClientRect().left, top = l.getBoundingClientRect().top;
              const [k, b, v, r] = [...l.children].map((c) => c.getBoundingClientRect());
              return [k.left, b.width ? b.left : -1, v.left, r.right, r.top - top, v.top - top].map((x, i) => Math.round(i < 4 && x !== -1 ? x - left : x));
            }));
            const [a, b] = [await cols(act), await cols(team)];
            assert.deepEqual(a[1], a[0]);
            assert.deepEqual(b[0], a[0]);
            assert.deepEqual(b[1], a[0]);
            assert.equal(a[0][5] <= 2, true, `${lang} at ${width}: what is used is on the window's first line: ${a[0]}`);
            // nothing in a line is cut short at either width
            const cut = await page.locator(".wq span").evaluateAll((ss) => ss.filter((s) => s.scrollWidth > s.clientWidth + 1).map((s) => s.textContent));
            assert.deepEqual(cut, [], `${lang} at ${width}: cut ${cut.join(", ")}`);
            const spare = page.locator(".rt-act", { hasText: "spare@example.com" }).first();
            await spare.waitFor();
            assert.equal(await spare.locator(".wins").count(), 0);
            assert.equal(await spare.locator(".wq").count(), 0);
            // nothing in the row runs past the list
            const over = await act.evaluate((e) => Math.max(e.scrollWidth - e.clientWidth, document.documentElement.scrollWidth - innerWidth));
            assert.ok(over <= 1, `${lang} at ${width}: ${over}px past`);
            assert.ok(asks.length >= 1, "the readings were asked for");
            assert.ok(asks.every((s) => !s.includes("asked")), "never an asked read: " + asks.join(", "));
            assert.deepEqual(errors, []);
          } finally {
            if (process.env.ARTIFACT_DIR) {
              await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
              const p = ctx.pages()[0];
              await p?.setViewportSize({ width, height: 2400 }).catch(() => {});
              await p?.waitForTimeout(300).catch(() => {});
              const box = await p?.locator(".rt-acts").first().evaluate((e) => { const r = e.getBoundingClientRect(); return { x: Math.max(0, r.left - 8), y: Math.max(0, r.top - 8), width: r.width + 16, height: r.height + 16 }; }).catch(() => null);
              if (box) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `windows-${engine}-${lang}-${width}.png`), clip: box }).catch(() => {});
            }
            await ctx.close();
          }
        });
      }
    }
  });
}
