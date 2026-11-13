// Run with Node's test runner and Playwright on the module path; see README.md.
// Wing8Aqua on X: two Claude Code accounts In turn, each at 0% of its five
// hours and 41% / 12% of its week. The rows said "41% used", read as the
// five hours. The share is the fullest window's, which sets an account
// aside under every routing, In turn's too; the row now names it ("7d 41%
// used"). An account with one window says its share as before. In every
// language, at a window's width and at 420px, in both engines.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const iso = (ms) => new Date(ms).toISOString();
const H = 3600e3;
const M = "deepseek-v4.1-flash";

// two Claude accounts with a 5 hours and a week, the share the week's
// (usedWindow), cc-0 the one that answered (its row says so), and a WorkBuddy one with a single window (no usedWindow)
const claude = (u, used) => ({ id: `claude#${u}`, provider: "claude", name: "Claude", who: `${u}@example.com`, kind: "account", agent: "claude", model: "claude-opus-5-5",
  routing: "rotate", known: true, used, usedWindow: "7 days", renews: [iso(now + 48 * H), iso(now + 5 * H)], usedRenews: iso(now + 48 * H + 5 * 60e3) });
const route = {
  id: 3, seq: 3, time: iso(now - 2000), agent: "claude", model: "group/turns", provider: "claude", done: true, status: 200, ms: 900, tokens: 10,
  group: { id: "turns", name: "Turns", routing: "rotate", members: ["claude/claude-opus-5-5"] },
  order: [claude("cc-0", 5), claude("cc-1", 41), claude("cc-2", 12),
    { id: "wb#a1", provider: "wb", name: "WorkBuddy", who: "wb-1@example.com", kind: "account", model: "claude-opus-5-5", known: true, used: 30, renews: [iso(now + 10 * H)], usedRenews: iso(now + 10 * H) }],
  tries: [{ id: "claude#cc-0", model: "claude-opus-5-5", start: iso(now - 1500), done: true, status: 200, ms: 900 }],
};

function serve(lang) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light", quotaLeft: true } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 3, totals: { requests: 1, rerouted: 0, errors: 0 }, routes: [route] });
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

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: an account's share names its window when it has several`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      for (const width of [1100, 420]) {
        await t.test(`${lang} at ${width}px`, async () => {
          const ctx = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
          try {
            const page = await ctx.newPage();
            page.setDefaultTimeout(5000);
            page.setDefaultNavigationTimeout(60000);
            const errors = [];
            page.on("pageerror", (e) => errors.push(e.message));
            await page.route("**/*", serve(lang));
            await page.goto("http://magpie.test/?view=routing");
            // the request's order (.rt-accts) and the accounts' cards (.rt-act)
            const texts = (who) => page.evaluate((who) => [
              ...[...document.querySelectorAll(".rt-accts > li")].filter((l) => l.textContent.includes(who)).map((l) => l.querySelector(".st, em")?.textContent || ""),
              ...[...document.querySelectorAll(".rt-act")].filter((l) => l.textContent.includes(who)).map((l) => l.querySelector(".st")?.textContent || ""),
            ], who);
            await page.waitForFunction(() => document.querySelectorAll(".rt-act").length >= 4 && document.querySelectorAll(".rt-accts > li").length >= 4);
            for (const [who, n] of [["cc-1@", "59%"], ["cc-2@", "88%"]]) {
              const ts = await texts(who);
              assert.equal(ts.length, 2, `${lang} ${width}: ${who} rows ${JSON.stringify(ts)}`);
              for (const s of ts) {
                assert.ok(s.startsWith("7d ") && s.includes(n), `${lang} ${width}: ${who} says "${s}", not its week's "7d … ${n}"`);
                assert.equal(s.split("7d").length, 2, `${lang} ${width}: ${who} names it once: "${s}"`);
              }
            }
            for (const s of await texts("wb-1@")) {
              assert.ok(s.includes("70%") && !/\b\d+[hdw] /.test(s), `${lang} ${width}: one window, no name: "${s}"`);
            }
            const over = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
            assert.ok(over <= 1, `${lang} at ${width}: ${over}px past`);
            assert.deepEqual(errors, []);
          } finally {
            await ctx.close();
          }
        });
      }
    }
  });
}
