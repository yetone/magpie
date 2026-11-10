// A cache hit rate never reads 100% unless every prompt token came from the
// cache (ZhangXiaof48473 on X: Codex gpt-6.1-sol requests of 77.5K tokens
// read "缓存 100%" on Routing's request list beside "缓存 99%"). Where
// rounding to a whole percent would say 100% or 0% of a prompt not all or
// none cached, the rate shows a decimal or two: 99.6%. One formatter
// (hitPct) says every hit rate. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const seat = { id: "codex", provider: "codex", name: "Codex", who: "test@example.com", kind: "account", model: "gpt-6.1-sol" };
function req(id, input, read) {
  const time = new Date(now.getTime() - (20 - id) * 60e3).toISOString();
  return { id, seq: id, time, agent: "codex", session: "cache-pct", model: "codex/gpt-6.1-sol", provider: "codex",
    order: [seat], tries: [{ id: seat.id, model: seat.model, start: time, done: true, status: 200, ms: 11000, ttft: 7000 }],
    done: true, status: 200, ms: 11000, ttft: 7000, tokens: input + read + 400, out: 400,
    usage: [{ provider: seat.provider, model: seat.model, in: input, out: 400, cache_read: read, cache_write: 0 }], priced: true, cost: 0.01 };
}
// newest first: 77,200 of 77,500 read (99.6%), all read, 77,035 of 77,500 (99.4%)
const fixtures = [req(3, 300, 77200), req(2, 0, 77500), req(1, 465, 77035)];

function serve(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.has("wait")) return new Promise(() => {});
      return json({ mine: true, now: now.toISOString(), seq: 3, totals: { requests: fixtures.length, rerouted: 0, errors: 0 }, routes: fixtures });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404 }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a cache hit rate reads 100% only when all of the prompt was cached`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: 1280, height: 900 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=routing");
      const rows = page.locator(".rt-req");
      await rows.nth(fixtures.length - 1).waitFor();
      assert.deepEqual(await rows.locator(".cache-hit .v").allTextContents(), ["99.6%", "100%", "99%"]);
      await rows.nth(0).click();
      assert.match(await page.locator(".rt-steps").textContent(), lang === "zh" ? /请求缓存命中率 99\.6%/ : /request cache hit rate 99\.6%/);
      // every caller's shared formatter, at its edges
      const got = await page.evaluate(() => [[1], [0], [0.6], [0.994], [0.9995], [0.99999], [0.003], [0.0004], [1e-7], [0.873, 1], [0.99996, 1], [1, 1]]
        .map(([h, d]) => hitPct(h, d)));
      assert.deepEqual(got, ["100%", "0%", "60%", "99%", "99.95%", "99.99%", "0.3%", "0.04%", "0.01%", "87.3%", "99.99%", "100.0%"]);
      assert.deepEqual(errors, []);
    });
  }
}
