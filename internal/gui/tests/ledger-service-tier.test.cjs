// Run with Node's test runner and Playwright on the module path; see README.md.
// JackyFill on X: the request log keeps each call's service tier (what the
// client asked for, what went to the vendor, what the reply said it ran at),
// and the request's details now show it as "Service tier". A tier the reply
// never named is not told as answered; a call with none shows no line.
// In Chromium and WebKit, in every language; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const reader = require("./reader.cjs");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();

// a counted call, 9000 of its 10000 prompt from the cache, and a Kiro one
// whose 148000 prompt no one counted: 90%, where reading Kiro's 0 as a miss
// said 9000 / 158000, 6%
const counted = { input: 500, cache_read: 9000, cache_write: 500 };
const kiro = { input: 148000, cache_read: 0, cache_write: 0, cache_unknown: 148000, cache_unknown_calls: 1 };
const plus = (...xs) => xs.reduce((a, x) => { for (const [k, v] of Object.entries(x)) a[k] = (a[k] || 0) + v; return a; }, {});
const agentRow = (name, x) => ({ name, icon: "", calls: 10, errors: 0, output: 1000, reasoning: 0, cost: 0, unpriced: 0, cache_unknown: 0, ...x });
const agents = [agentRow("Claude Code", plus(counted, kiro)), agentRow("Kiro only", kiro)];
const usage = { calls: 20, errors: 0, output: 2000, reasoning: 0, cost: 0, unpriced: 0, path: "/test/usage.jsonl", bucket: "day", agents, models: [], series: [], ...plus(counted, kiro, kiro) };

const req = (i, x) => ({
  t: new Date(now - (i + 1) * 60e3).toISOString(), agent: "claude", agentName: "Claude Code", icon: "claudecode-color",
  providerAccount: "a@example.com", req: "claude-sonnet-4.5", model: "claude-sonnet-4.5", served: "claude-sonnet-4.5",
  ms: 3000, status: 200, rid: "r-" + i, ep: "/v1/messages", cost: 0.01, priced: true, out: 19, ...x,
});
const ROWS = [
  req(0, { provider: "openai", providerName: "OpenAI", in: 500, tier_asked: "priority", tier_sent: "priority", tier_served: "default" }),
  req(1, { provider: "openai", providerName: "OpenAI", in: 500, tier_asked: "flex", tier_sent: "default" }),
  req(2, { provider: "anthropic", providerName: "Anthropic", in: 500 }),
];
const page = (q) => ({ period: q.get("period"), rows: ROWS, offset: 0, total: ROWS.length, calls: 2, errors: 0, output: 38, reasoning: 0, cost: 0.02, unpriced: 0,
  ...plus(counted, kiro), bucket: "day", series: [], by: { provider: [], agent: [], model: [] }, agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color" }] });

function serve(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: new Date(now).toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [], models: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, calls: [], groups: [] } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/requests") return json(page(url.searchParams));
    if (url.pathname === "/api/usage/requests/content") return json({ found: false, why: "read" });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage") return json(usage);
    if (url.pathname === "/api/sessions") return json({ sessions: [], dirs: [] });
    if (url.pathname === "/api/sessions/stats") return json({ from: "", to: "", days: [], agents: {} });
    if (url.pathname.startsWith("/api/window/")) return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404 }); }
  };
}

const LANGS = ["en", "zh", "zh-TW", "ja", "de"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a request's details show its service tier`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of LANGS) {
      await t.test(lang, async () => {
        const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, reducedMotion: "reduce" });
        const errors = [];
        try {
          const p = await context.newPage();
          p.setDefaultTimeout(5000);
          p.setDefaultNavigationTimeout(60000);
          p.on("pageerror", (e) => errors.push(e.message));
          await p.route("**/*", serve(lang));
          await p.goto("http://magpie.test/?view=usage");
          const say = (s, v) => p.evaluate(([s, v]) => t(s, v), [s, v]);
          const label = await say("Service tier");
          if (lang !== "en") assert.notEqual(label, "Service tier", "translated");
          await p.locator(":is(#usageTab .opt, .nav-s):visible").nth(1).click();
          const rows = p.locator(".led tbody tr.led-row");
          await rows.nth(ROWS.length - 1).waitFor({ timeout: 15000 });
          const tier = async (i) => {
            const d = await reader.openRequest(p, rows.nth(i));
            const v = await d.evaluate((b, l) => {
              const dt = [...b.querySelectorAll("dt")].find((x) => x.textContent === l);
              return dt ? dt.nextElementSibling.textContent : null;
            }, label);
            await reader.closeRequest(p);
            return v;
          };
          // asked, sent (same, so told once) and answered
          assert.equal(await tier(0), [await say("asked {tier}", { tier: "priority" }), await say("answered {tier}", { tier: "default" })].join(" · "));
          // asked and sent differ, no answer: the answer is not invented
          assert.equal(await tier(1), [await say("asked {tier}", { tier: "flex" }), await say("sent {tier}", { tier: "default" })].join(" · "));
          // none: no line
          assert.equal(await tier(2), null);
          assert.deepEqual(errors, []);
        } finally {
          await context.close();
        }
      });
    }
  });
}
