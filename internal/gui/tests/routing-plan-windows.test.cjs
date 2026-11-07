// Run with Node's test runner and Playwright on the module path; see README.md.
// #1016: a lone Kimi Code key shows the windows from its Usage plan card
// while it answers or rests. The shared Usage read is made once; a kept
// day's request never acquires today's windows. The row also fits narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = process.env.MAGPIE_ROUTING_ASSETS || path.resolve(__dirname, "../assets");
const now = Date.now();
const iso = (ms) => new Date(ms).toISOString();
const day = iso(now).slice(0, 10);
const windows = [
  { name: "5 hours", used: 49, resetsAt: iso(now + 2 * 3600e3) },
  { name: "7 days", used: 45, resetsAt: iso(now + 3 * 86400e3) },
];

const words = {
  en: ["5 hours 49% used · resets in 2h", "7 days 45% used · resets in 3d"],
  zh: ["5 小时 已用 49% · 2 小时后重置", "7 天 已用 45% · 3 天后重置"],
};

function fixture(resting) {
  const key = { id: "kimi-code-cn", provider: "kimi-code-cn", name: "Kimi Code (China)", kind: "provider", model: "kimi-for-coding" };
  if (resting) key.rest = { why: "rate", until: iso(now + 3600e3) };
  const route = {
    id: 7, seq: 7, time: iso(now - 2000), agent: "codex", model: key.model, provider: key.provider,
    order: [key], tries: resting ? [] : [{ id: key.id, model: key.model, start: iso(now - 1500) }],
  };
  return { key, route };
}

function serve(lang, resting, asks) {
  const { route } = fixture(resting);
  const card = { provider: "kimi-code-cn", name: "Kimi Code (China)", kind: "plan", windows };
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light", quotaLeft: false } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: iso(now), seq: 7, totals: { requests: 1, rerouted: 0, errors: 0 }, routes: [route] });
    }
    if (url.pathname === "/api/gateway/history") {
      return json(url.searchParams.get("day") ? { cut: false, days: [{ day, requests: 1 }], routes: [{ ...route, done: true, status: 200 }] }
        : { cut: false, days: [{ day, requests: 1 }], routes: [] });
    }
    if (url.pathname === "/api/usage/quotas") { asks.push(url.search); return json([card]); }
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const resting of [false, true]) {
      test(`${engine} ${lang}: a lone Kimi key shows plan windows while ${resting ? "resting" : "answering"}`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        for (const width of [1100, 420]) {
          await t.test(`${width}px`, async () => {
            const ctx = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
            try {
              const page = await ctx.newPage();
              page.setDefaultTimeout(5000);
              const errors = [], asks = [];
              page.on("pageerror", (e) => errors.push(e.message));
              await page.route("**/*", serve(lang, resting, asks));
              await page.goto("http://magpie.test/?view=routing");
              const row = page.locator(".rt-act", { hasText: "kimi-for-coding" }).first();
              await row.locator(".wins span").first().waitFor();
              assert.deepEqual(await row.locator(".wins span").allTextContents(), words[lang]);
              assert.equal(await row.evaluate((e) => Math.max(e.scrollWidth - e.clientWidth, document.documentElement.scrollWidth - innerWidth) <= 1), true);
              assert.equal(asks.length, 1, "Routing reuses the one shared Usage read");
              assert.ok(asks.every((s) => !s.includes("asked")), "never an asked read: " + asks.join(", "));

              if (width === 1100) {
                await page.locator(".rt-days .rt-day").nth(1).click();
                await page.locator(".rt-req").first().waitFor();
                assert.equal(await page.locator(".rt-act .wins").count(), 0, "kept requests do not acquire today's windows");
                assert.equal(asks.length, 1, "history does not read Usage again");
              }
              assert.deepEqual(errors, []);
            } finally {
              await ctx.close();
            }
          });
        }
      });
    }
  }
}
