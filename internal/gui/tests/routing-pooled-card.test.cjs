// Run with Node's test runner and Playwright on the module path; see README.md.
// X (@apple_test_src): a Cursor account signed in through its plugin showed
// "what's left not known yet" in Routing's Accounts and keys while the Usage
// page read its allowance. Every window of a pooled Cursor plan is set aside
// (it can't stop the account), so routing weighs no figure for it, and the
// row said nothing was known. It now says the card's first window, as the
// Usage page read it, the rest on hover; the unknown line stays for an
// account with no card. Every language, 1100 and 420px, both engines.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const iso = (ms) => new Date(ms).toISOString();
const accts = [
  { id: "cursor", provider: "cursor", name: "Cursor", who: "work@example.com", kind: "account", agent: "cursor", model: "claude-opus-5", known: false, plan: "Pro" },
  { id: "cursor@spare", provider: "cursor", name: "Cursor", who: "spare@example.com", kind: "account", agent: "cursor", model: "claude-opus-5", known: false, plan: "Pro" },
];
const route = {
  id: 7, seq: 7, time: iso(now - 2000), agent: "cursor", model: "claude-opus-5", provider: "cursor", done: true, status: 200, ms: 900, tokens: 1500,
  order: accts, tries: [{ id: "cursor", model: "claude-opus-5", start: iso(now - 2000), done: true, ms: 800, status: 200 }],
};
const cards = [
  { provider: "cursor", name: "Cursor", kind: "subscription", user: "Work@Example.com", plan: "Max", windows: [
    { name: "5 hours", used: 40, aside: true, resetsAt: iso(now + 130 * 60e3) },
    { name: "7 days", used: 12, aside: true, resetsAt: iso(now + 73 * 3600e3) },
  ] },
  // another computer's reading of the spare account: not this one's
  { provider: "cursor", name: "Cursor · Home", kind: "subscription", user: "spare@example.com", from: "Home", windows: [{ name: "5 hours", used: 90 }] },
];

function serve(lang, asks) {
  const state = { agents: [{ id: "cursor", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light", quotaLeft: false } };
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

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a plugin account whose windows are all aside shows them, not unknown`, async (t) => {
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
            // the one figure routing went by, the windows each on hover over
            // it: a card keeps one line of what is left (ferock4669 on
            // Discord), and when it was read comes last on hover (danzi002)
            await act.waitFor();
            // the card's first window is the row's figure, the second on hover
            await page.waitForFunction((want) => [...document.querySelectorAll(".rt-act")].some((e) => e.textContent.includes("work@example.com") && e.querySelector(".st")?.textContent === want), words[lang][0]);
            assert.equal((await act.locator(".st").getAttribute("title")).split("\n").slice(0, 2).join("\n"), words[lang].join("\n"));
            assert.equal(await act.locator(".wins").count(), 0);
            // no card of its own (another computer's isn't this one's): unknown
            const spare = page.locator(".rt-act", { hasText: "spare@example.com" }).first();
            await spare.waitFor();
            assert.notEqual(await spare.locator(".st").textContent(), words[lang][0]);
            assert.equal(await spare.locator(".wins").count(), 0);
            // nothing in the row runs past the list
            const over = await act.evaluate((e) => Math.max(e.scrollWidth - e.clientWidth, document.documentElement.scrollWidth - innerWidth));
            assert.ok(over <= 1, `${lang} at ${width}: ${over}px past`);
            assert.ok(asks.length >= 1, "the readings were asked for");
            assert.ok(asks.every((s) => !s.includes("asked")), "never an asked read: " + asks.join(", "));
            assert.deepEqual(errors, []);
          } finally {
            await ctx.close();
          }
        });
      }
    }
  });
}
