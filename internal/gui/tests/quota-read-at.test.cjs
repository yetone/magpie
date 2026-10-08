// Run with Node's test runner and Playwright on the module path; see README.md.
// When each allowance was read (#802, margbug01): a subscription's card
// says "Updated 40 minutes ago" from the reading's own time (readAt) — a
// Claude account's is what Claude Code last told, which can be well before
// the page asked — one standing in for a reading that failed says "As of",
// marked stale, and a failed one says neither. Hovering a window's plot
// lists its latest readings, each with when it was read, ↻ where
// the window started again; the card gets no control of its own.
// English and Chinese; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const H = 3600e3, M = 60e3;
const iso = (ms) => new Date(ms).toISOString();

function fixtures(now) {
  const r5 = now + 2 * H;
  const quotas = [
    { provider: "claude", name: "Claude Code", icon: "claude-color", plan: "Max", user: "a@x.com", readAt: iso(now - 40 * M),
      windows: [{ name: "5 hours", used: 28, resetsAt: iso(r5) }] },
    { provider: "codex", name: "Codex", icon: "openai", plan: "Plus", user: "b@x.com", asOf: iso(now - 3 * H),
      windows: [{ name: "5 hours", used: 50, resetsAt: iso(r5) }] },
    { provider: "kimi", name: "Kimi", icon: "kimi-color", error: "HTTP 503", windows: [] },
  ];
  const points = [
    { at: iso(r5 - 6 * H), left: 35, resetsAt: iso(r5 - 5 * H) },
    { at: iso(r5 - 4 * H), left: 100, resetsAt: iso(r5) },
    { at: iso(now - 40 * M), left: 72, resetsAt: iso(r5) },
  ];
  const history = [{ provider: "claude", user: "a@x.com", lines: [{ name: "5 hours", points }] }];
  return { quotas, history };
}

function serve(lang, data) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (body) => route.fulfill({ json: body });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json(data.quotas);
    if (url.pathname === "/api/usage/quotas/history") return json(data.history);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { updated: "Updated 40 minutes ago", asOf: /^As of /, latest: "Latest readings:" },
  zh: { updated: "40分钟前更新", asOf: /^截至|^As of/, latest: "最近的读数：" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a quota card says when it was read`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const errors = [];
      const page = await (await browser.newContext({ viewport: { width: 900, height: 700 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      const data = fixtures(Date.now());
      await page.route("**/*", serve(lang, data));
      await page.goto("http://magpie.test/?view=usage");

      const claude = page.locator(".subscription-card", { hasText: "Claude Code" });
      const read = claude.locator(".quota-read");
      await read.waitFor();
      assert.equal(await read.textContent(), w.updated, "the reading's own time, not the page's");
      assert.equal(await read.evaluate((e) => e.classList.contains("stale")), false);

      const codex = page.locator(".subscription-card", { hasText: "Codex" }).locator(".quota-read");
      assert.match(await codex.textContent(), w.asOf);
      assert.equal(await codex.evaluate((e) => e.classList.contains("stale")), true, "a kept reading is marked stale");

      assert.equal(await page.locator(".subscription-card", { hasText: "Kimi" }).locator(".quota-read").count(), 0, "a failed reading says no time");

      // the latest readings, newest first, on the window's plot
      const plot = claude.locator(".quota-plot").first();
      await plot.hover();
      const title = (await plot.getAttribute("title")).split("\n");
      assert.equal(title[0], w.latest, title.join(" | "));
      assert.deepEqual(title.slice(1).map((l) => l.replace(/ · .*/, "")), ["72%", "↻ 100%", "35%"]);
      for (const reading of title.slice(1)) assert.match(reading, / · .+/, "each reading includes its own time");
      const last = data.history[0].lines[0].points.at(-1);
      const readTime = new Date(last.at).toLocaleTimeString(lang === "zh" ? "zh-CN" : "en-US", { hour: "2-digit", minute: "2-digit", hour12: false });
      assert.ok(title[1].includes(readTime), "the latest reading's own time is reachable: " + title[1]);
      const dot = await plot.locator("circle.qc-dot title").textContent();
      assert.match(dot, /72%/);
      assert.ok(dot.includes(lang === "zh" ? "读取于" : "read "), dot);
      assert.equal(await plot.locator("button").count(), 0, "no control on the plot");
      assert.deepEqual(errors, []);
    });
  }
}
