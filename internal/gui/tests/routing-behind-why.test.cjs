// Run with Node's test runner and Playwright on the module path; see README.md.
// demonyins on X: under Smart, an Open Design "Design Plan Go" account at
// 75% left sat grey beside the Claude Max account that answered, its row
// saying only "75% left · renews in 20h", and read as paused. Smart had put
// the Max account first because its week renewed sooner. A row Smart
// weighed with quota to spare now says why another went first: one
// renewing sooner, or the conversation kept on the one that answered it.
// The first's own row, and one behind for no such reason, say what they
// did. In every language, at a window's width and at 420px, in both
// engines.
//
// YsiCing on X: three Codex accounts under Smart, every conversation on
// the Pro one, the other two rows saying "0% used · conversation kept on
// another". Smart puts the account renewing soonest first for a new
// conversation too, so "kept" read as stickiness gone wrong. A row behind
// one that answered a kept conversation and renews sooner says that one
// renews sooner; "kept" is left to a row that renews sooner itself, where
// only the conversation's cache holds it elsewhere. A fresh account, its
// windows not started, says so without a renewal of its own.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ROUTING_ASSETS || path.resolve(__dirname, "../assets"));
const now = Date.now();
const iso = (ms) => new Date(ms).toISOString();
const H = 3600e3;

// the request: Max first (its week renews in 12h), Design Plan Go behind
// (its week in 20h); affinity, when given, kept it on Max. "later": Max's
// week renews after Design Plan Go's (in 30h), so only the kept
// conversation holds it first. "fresh": Design Plan Go's windows not
// started, nothing used, no renewal of the window it used
function routeOf(affinity, kind) {
  const maxWeek = kind === "later" ? 30 * H : 12 * H;
  const max = { id: "claude", provider: "claude", name: "Claude", who: "max@example.com", kind: "account", agent: "claude", model: "claude-haiku-5-5",
    known: true, used: 80, renews: [iso(now + maxWeek + 5 * 60e3), iso(now + 2 * H)], usedRenews: iso(now + maxWeek + 5 * 60e3), plan: "max" };
  const od = kind === "fresh"
    ? { id: "opendesign", provider: "opendesign", name: "Open Design", who: "od@example.com", kind: "account", agent: "opendesign", model: "claude-haiku-5.5",
      known: true, used: 0, renews: [iso(now + 7 * 24 * H), iso(now + 5 * H)], soon: [iso(now + 7 * 24 * H), iso(now + 5 * H)], plan: "Design Plan Go" }
    : { id: "opendesign", provider: "opendesign", name: "Open Design", who: "od@example.com", kind: "account", agent: "opendesign", model: "claude-haiku-5.5",
      known: true, used: 25, renews: [iso(now + 20 * H + 5 * 60e3), iso(now + 3 * H)], usedRenews: iso(now + 20 * H + 5 * 60e3), plan: "Design Plan Go" };
  return {
    id: 7, seq: 7, time: iso(now - 2000), agent: "claude", model: "group/haiku", provider: "claude", done: true, status: 200, ms: 900, tokens: 1500,
    group: { id: "haiku", name: "Haiku", routing: "", affinity: "", members: ["claude/claude-haiku-5-5", "opendesign/claude-haiku-5.5"] },
    ...(affinity ? { affinity: { why: "cache", last: "claude", at: iso(now - 60e3), cacheRead: 40000, cacheWrite: 200 } } : {}),
    order: [max, od], tries: [{ id: "claude", model: "claude-haiku-5-5", start: iso(now - 2000), done: true, ms: 800, status: 200 }],
  };
}

function serve(lang, route) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light", quotaLeft: true } };
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
    if (url.pathname === "/api/usage/quotas") return json([]);
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

// what the Design Plan row says: behind one renewing sooner (the
// conversation kept there or not), behind the conversation kept on one
// renewing later, and fresh behind one renewing sooner; then what Max's
// own row says when its week renews in 12h
const words = {
  en: ["75% left · renews in 20 h · one renewing sooner goes first", "75% left · conversation kept on another · renews in 20 h", "100% left · one renewing sooner goes first", "answered this request · renews in 12 h"],
  zh: ["剩余 75% · 20 小时后重置 · 先用更早重置的账号", "剩余 75% · 会话留在另一个账号 · 20 小时后重置", "剩余 100% · 先用更早重置的账号", "回答了这个请求 · 12 小时后重置"],
  "zh-TW": ["剩餘 75% · 20 小時後重置 · 先用更早重置的帳號", "剩餘 75% · 對話留在另一個帳號 · 20 小時後重置", "剩餘 100% · 先用更早重置的帳號"],
  ja: ["残り 75% · 20 時間 後に更新 · 先に更新されるアカウントを優先", "残り 75% · 会話は別のアカウントに固定 · 20 時間 後に更新", "残り 100% · 先に更新されるアカウントを優先"],
  de: ["75% übrig · erneuert sich in 20 h · eines, das sich früher erneuert, geht vor", "75% übrig · Unterhaltung bleibt bei einem anderen · erneuert sich in 20 h", "100% übrig · eines, das sich früher erneuert, geht vor"],
};
// each case: its name, the affinity kept or not, the kind, and which of
// words[lang] the Design Plan row says
const cases = [["", false, "", 0], [", kept on the one renewing sooner", true, "", 0], [", kept on one renewing later", true, "later", 1],
  [", fresh, kept on the one renewing sooner", true, "fresh", 2], [", fresh", false, "fresh", 2]];

const rowText = (page, who) => page.locator(".rt-accts > li", { hasText: who }).first().locator(".st, em").first();

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a Smart row with quota left says why another went first`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(words)) {
      for (const width of [1100, 420]) {
        for (const [name, affinity, kind, i] of cases) {
          await t.test(`${lang} at ${width}px${name}`, async () => {
            const ctx = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
            try {
              const page = await ctx.newPage();
              page.setDefaultTimeout(5000);
              page.setDefaultNavigationTimeout(60000);
              const errors = [];
              page.on("pageerror", (e) => errors.push(e.message));
              await page.route("**/*", serve(lang, routeOf(affinity, kind)));
              await page.goto("http://magpie.test/?view=routing");
              const od = rowText(page, "od@example.com");
              await od.waitFor();
              await page.waitForFunction((w) => [...document.querySelectorAll(".rt-accts > li")].some((l) => l.textContent.includes(w)), words[lang][i], { timeout: 4000 }).catch(() => {});
              assert.equal((await od.textContent()).trim(), words[lang][i]);
              // the one that answered says so, and when its week renews (dzxx0496)
              if (words[lang][3] && kind !== "later") assert.equal((await rowText(page, "max@example.com").textContent()).trim(), words[lang][3]);
              const over = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
              assert.ok(over <= 1, `${lang} at ${width}: ${over}px past`);
              assert.deepEqual(errors, []);
            } finally {
              await ctx.close();
            }
          });
        }
      }
    }
  });
}
