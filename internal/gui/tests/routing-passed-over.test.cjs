// Run with Node's test runner and Playwright on the module path; see README.md.
// Dazzle_sys on X: a group's GPT-6.2 Sol and GPT-6 Luna on one Codex
// account, Sol listed first. The request went to Luna and Sol's row said
// nothing of why. Sol had failed and rested the account, and Luna, on the
// same account, answered and lifted the rest, so the row fell back to the
// account's quota. A row the request went on from says the status and the
// vendor's own words, whether its failure rested it or not; a row a new
// session passed over for the sessions on it (Affinity.Spread) says so;
// and a member behind one on its own account that a conversation is kept
// on says the conversation is kept on another, as one on another account
// does. In every language, at a window's width and at 420px, in both
// engines.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ROUTING_ASSETS || path.resolve(__dirname, "../assets"));
const now = Date.now();
const iso = (ms) => new Date(ms).toISOString();
const H = 3600e3;
const said = "Codex: The model gpt-6.2-sol is not available for this request";
const limit = "Codex: You've hit your usage limit. Try again in 3 hours.";
const acct = { id: "codex@me@example.com", provider: "codex", name: "Codex", who: "me@example.com", kind: "account", agent: "codex",
  known: true, used: 45, renews: [iso(now + 25 * H), iso(now + 2 * H)], usedRenews: iso(now + 25 * H), plan: "plus" };
const sol = { ...acct, model: "gpt-6.2-sol", fixed: "medium" };
const luna = { ...acct, model: "gpt-6-luna", fixed: "max" };
const ds = { id: "deepseek#1", provider: "deepseek", name: "DeepSeek", who: "key one", kind: "key", model: "deepseek-v4" };
const group = { id: "codex", name: "Codex", routing: "", affinity: "session", members: ["codex/gpt-6.2-sol:medium", "codex/gpt-6-luna:max", "deepseek/deepseek-v4"] };
const base = { id: 7, seq: 7, time: iso(now - 2000), agent: "codex", model: "group/codex", done: true, status: 200, ms: 900, tokens: 19, group };

const routes = {
  // the reporter's: Sol refused, resting the account a minute, then Luna
  // on the same account answered
  failed: { ...base, provider: "codex", order: [sol, luna, ds], tries: [
    { id: sol.id, model: sol.model, fixed: "medium", start: iso(now - 2000), done: true, status: 403, ms: 300, fail: "other", error: said,
      rest: { why: "other", status: 403, until: iso(now + 60e3), key: sol.id, error: said } },
    { id: luna.id, model: luna.model, fixed: "max", start: iso(now - 1700), done: true, status: 200, ms: 600 },
  ] },
  // Sol rate limited: magpie's reading of it beside the vendor's words
  limited: { ...base, provider: "codex", order: [sol, luna, ds], tries: [
    { id: sol.id, model: sol.model, fixed: "medium", start: iso(now - 2000), done: true, status: 429, ms: 300, fail: "rate", error: limit },
    { id: luna.id, model: luna.model, fixed: "max", start: iso(now - 1700), done: true, status: 200, ms: 600 },
  ] },
  // a new session, put on DeepSeek, which no session is on: Sol, ahead of
  // it, has one
  spread: { ...base, provider: "deepseek", affinity: { why: "first", spread: 1 }, order: [ds, sol, luna],
    tries: [{ id: ds.id, model: ds.model, start: iso(now - 2000), done: true, status: 200, ms: 800 }] },
  // the conversation kept on Luna: Sol, on the same account, behind it
  kept: { ...base, provider: "codex", affinity: { why: "session", kept: true, last: luna.id, at: iso(now - 60e3) }, order: [luna, sol, ds],
    tries: [{ id: luna.id, model: luna.model, fixed: "max", start: iso(now - 2000), done: true, status: 200, ms: 800 }] },
};

function serve(lang, route) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light", quotaLeft: true } };
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

// what Sol's row says: went on (with the vendor's words), passed over for
// the sessions on it, behind the conversation kept on Luna
const words = {
  en: { failed: "went on to the next", limited: "rate limited", spread: "other sessions on it", kept: "conversation kept on another" },
  zh: { failed: "已转给下一个", limited: "限流", spread: "已有其他会话在用", kept: "会话留在另一个账号" },
  "zh-TW": { failed: "已轉給下一個", limited: "限流", spread: "已有其他工作階段在用", kept: "對話留在另一個帳號" },
  ja: { failed: "次に回しました", limited: "レート制限", spread: "他のセッションが使用中", kept: "会話は別のアカウントに固定" },
  de: { failed: "an den nächsten weitergegeben", limited: "Rate-Limit", spread: "andere Sitzungen darauf", kept: "Unterhaltung bleibt bei einem anderen" },
};

const rowOf = (page, model) => page.locator(".rt-accts > li", { hasText: model }).first().locator(".st, em").first();

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a member passed over says why on its row`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(words)) {
      for (const width of [1100, 420]) {
        for (const kind of Object.keys(routes)) {
          await t.test(`${lang} at ${width}px, ${kind}`, async () => {
            const ctx = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
            try {
              const page = await ctx.newPage();
              page.setDefaultTimeout(5000);
              page.setDefaultNavigationTimeout(60000);
              const errors = [];
              page.on("pageerror", (e) => errors.push(e.message));
              await page.route("**/*", serve(lang, routes[kind]));
              await page.goto("http://magpie.test/?view=routing");
              const row = rowOf(page, "gpt-6.2-sol");
              await row.waitFor();
              const want = words[lang][kind];
              await page.waitForFunction((w) => [...document.querySelectorAll(".rt-accts > li")].some((l) => l.textContent.includes(w)), want, { timeout: 4000 }).catch(() => {});
              const text = (await row.textContent()).trim();
              assert.ok(text.includes(want), `${kind}: Sol's row says ${JSON.stringify(text)}`);
              if (kind === "failed") {
                // the status and the vendor's words, on the row or, cut
                // short there, over it
                const whole = text + " " + ((await row.getAttribute("title")) || "");
                assert.ok(text.startsWith("403"), text);
                assert.ok(whole.includes(said), `the vendor's words are not on the row: ${JSON.stringify(whole)}`);
              }
              if (kind === "limited") {
                const whole = text + " " + ((await row.getAttribute("title")) || "");
                assert.ok(text.startsWith("429") && text.includes(words[lang].failed), text);
                assert.ok(whole.includes(limit), `the vendor's words are not on the row: ${JSON.stringify(whole)}`);
              }
              // Luna, behind Sol, wasn't passed over for a new session
              if (kind === "spread") assert.ok(!(await rowOf(page, "gpt-6-luna").textContent()).includes(want));
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
