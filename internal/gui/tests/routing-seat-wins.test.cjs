// Run with Node's test runner and Playwright on the module path; see README.md.
// YujunT46308 on X: the stage's seat showed a bar and one share, so they went
// to Usage to see what was left. An account's seat now tells every window it
// has under its status (name, what is used, when it starts again, as the
// Usage page words them: the same winParts), and nothing for a seat with no
// card of its own, whose figure is not guessed. In every language, at a
// window's width and at 420px, in both engines.
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
  en: [["5h", "40% used", "resets in 2h"], ["7d", "12% used", "resets in 3d"]],
  zh: [["5h", "已用 40%", "2 小时后重置"], ["7d", "已用 12%", "3 天后重置"]],
  "zh-TW": [["5h", "已用 40%", "2 小時後重置"], ["7d", "已用 12%", "3 天后重置"]],
  ja: [["5h", "使用済み 40%", "2 時間後にリセット"], ["7d", "使用済み 12%", "3 日後にリセット"]],
  de: [["5h", "40% verbraucht", "Reset in 2 Std."], ["7d", "12% verbraucht", "Reset in 3 T."]],
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: an account's seat on the stage tells every window it has`, async (t) => {
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
            const errors = [];
            page.on("pageerror", (e) => errors.push(e.message));
            await page.route("**/*", serve(lang, []));
            await page.goto("http://magpie.test/?view=routing");
            const seat = (who) => page.locator(".rt-accts li", { has: page.locator(".who", { hasText: who }) }).first();
            await page.waitForFunction(() => [...document.querySelectorAll(".rt-accts li .wins span")].length >= 2);
            const wins = seat("work@example.com").locator(".wins > span");
            assert.equal(await wins.count(), 2, "both windows, not only the first");
            const got = await wins.evaluateAll((es) => es.map((e) => [e.querySelector(".k").textContent, e.querySelector("b").textContent, e.querySelector(".r")?.textContent]));
            assert.deepEqual(got, words[lang]);
            // an account with no card of its own says nothing of windows it was not read for
            assert.equal(await seat("spare@example.com").locator(".wins > span").count(), 0);
            // every window in full on hover, with its reset
            assert.match(await wins.nth(1).getAttribute("title"), /12/);
            // nothing runs off the seat at any width
            const over = await seat("work@example.com").evaluate((li) => li.scrollWidth > li.clientWidth + 1);
            assert.equal(over, false);
            assert.deepEqual(errors, []);
          } finally {
            await ctx.close();
          }
        });
      }
    }
  });
}
