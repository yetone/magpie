// Run with Node's test runner and Playwright on the module path; see README.md.
// freefcw on X (11-09): magpie at 32% CPU and its webviews at 220MB, idle.
// The Routing page redraws Accounts and keys each second (its countdowns),
// and each drawing made every provider's heading anew: a new <img> asks for
// its icon whether or not the list keeps the old heading, so five providers
// were five icon requests a second through the app's asset server, each a
// new SVG document in the webview. A heading that says the same is kept:
// over seconds of nothing new, no icon is asked for again and each heading
// is the node it was. Every language, at a window's width and at 420px, in
// both engines.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const H = 3600e3;
const iso = (ms) => new Date(ms).toISOString();
const provs = [
  ["codex", "Codex", "codex-color", "account"],
  ["deepseek", "DeepSeek", "deepseek-color", "key"],
  ["aihubmix", "AIHubMix", "aihubmix-color", "key"],
  ["claude", "Claude Code", "claudecode-color", "account"],
];
const seats = provs.map(([id, name, ic, kind], i) => ({
  seat: { id: id + "@a", provider: id, name, icon: ic, who: kind === "account" ? id + "@example.com" : "sk-" + id, kind, model: "m" + i, plan: kind === "account" ? "Pro" : "",
    // a reset ahead
    known: kind === "account", used: 30, renews: [iso(now + 3 * H)], usedRenews: iso(now + 3 * H),
    // and one resting a minute and a half, whose time back counts down by the second
    ...(id === "deepseek" ? { rest: { key: "r-rate", why: "rate", until: iso(now + 90e3), status: 429, error: "" } } : {}) },
  tally: { tried: 10, ok: 10, fails: {}, last: now - (i + 1) * 60e3 },
}));
const tallies = {};
for (const { seat, tally } of seats) tallies[JSON.stringify([seat.provider, seat.who])] = { ...seat, ...tally, models: [seat.model] };
const order = seats.map((s) => s.seat);
const routes = [{ id: 1, seq: 1, time: iso(now - 20e3), agent: "codex", model: "m0", provider: "codex", done: true, status: 200, ms: 4200, order,
  tries: [{ id: "codex@a", model: "m0", start: iso(now - 20e3), done: true, ms: 4100, status: 200 }] }];
const langs = ["en", "zh", "zh-TW", "ja", "de"];

function serve(lang, asked) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light", quotaLeft: false } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 40, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [{ day: new Date(now).toISOString().slice(0, 10), requests: 40 }], routes: [], tallies });
    if (url.pathname === "/api/usage/quotas" || url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.pathname.startsWith("/icons/")) asked.push(url.pathname);
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    // as the app's server sends them (revalidated): asked again each time
    await r.fulfill({ body: await fs.readFile(file), contentType, headers: { "Cache-Control": "no-cache" } });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: Accounts and keys asks for no icon again while nothing changes`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const width of [1100, 420]) for (const lang of (width > 1000 ? langs : ["en"])) {
      await t.test(`${lang} at ${width}px`, async () => {
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        try {
          const page = await ctx.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [], asked = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, asked));
          await page.goto("http://magpie.test/?view=routing");
          await page.locator(".rt-acts .rt-prov").nth(provs.length - 1).waitFor();
          // every heading drawn, each icon asked for once
          await page.waitForFunction((n) => [...document.querySelectorAll(".rt-acts .rt-prov img")].filter((i) => i.complete).length === n, provs.length);
          const heads = await page.evaluate(() => [...document.querySelectorAll(".rt-acts .rt-prov")].map((h, i) => { h.dataset.mark = String(i); return h.querySelector("b").textContent; }));
          assert.deepEqual(heads.sort(), provs.map((p) => p[1]).sort());
          // the countdown redraws the list each second: wait out three of
          // its drawings, counted by the clock it shows changing
          const before = asked.length;
          const ticks = await page.evaluate(() => new Promise((done) => {
            let n = 0;
            const seen = new MutationObserver(() => n++);
            seen.observe(document.querySelector(".rt-acts"), { subtree: true, childList: true, characterData: true });
            setTimeout(() => { seen.disconnect(); done(n); }, 3500);
          }));
          assert.ok(ticks > 0, "the list was drawn again meanwhile");
          assert.deepEqual(asked.slice(before), [], "no icon asked for again");
          const kept = await page.evaluate(() => [...document.querySelectorAll(".rt-acts .rt-prov")].map((h) => h.dataset.mark || "new"));
          assert.deepEqual(kept.sort(), provs.map((_, i) => String(i)), "each heading is the one drawn before");
          assert.deepEqual(errors, []);
        } finally {
          await ctx.close();
        }
      });
    }
  });
}
