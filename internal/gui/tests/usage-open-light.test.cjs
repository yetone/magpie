// Run with Node's test runner and Playwright on the module path; see README.md.
// Opening Usage again does no heavy drawing inside the click (_hii11 on
// Discord: the page switch stuttered, Usage most of all). The allowance cards
// already drawn stay as they are until the read that goes with every open
// answers, so each card and curve is built once per open, not twice and the
// first time in the click. And a curve's readings are parsed once for each
// read of the history: drawing the cards again parses none of them.
// Every language, Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = process.env.MAGPIE_OPEN_ASSETS || path.resolve(__dirname, "../assets");
const H = 3600e3;
const iso = (ms) => new Date(ms).toISOString();

function fixtures(now) {
  const quotas = ["a@x.com", "b@x.com"].map((user) => ({ provider: "codex", name: "Codex", icon: "openai", plan: "Pro", user, readAt: iso(now), windows: [
    { name: "5 hours", used: 40, resetsAt: iso(now + 2 * H) },
    { name: "Weekly", used: 30, resetsAt: iso(now + 72 * H) },
  ] }));
  // ten days of readings every 15 minutes: 960 a line
  const line = (name, span) => ({ name, points: Array.from({ length: 960 }, (_, k) => {
    const at = now - 240 * H + k * 15 * 60e3, s = Math.floor((at - now) / span);
    return { at: iso(at), left: 90 - (k % 20), start: iso(now + s * span), resetsAt: iso(now + (s + 1) * span) };
  }) });
  const history = quotas.map((q) => ({ provider: q.provider, user: q.user, lines: [line("5 hours", 5 * H), line("Weekly", 168 * H)] }));
  return { quotas, history };
}

function serve(lang, data, gate) {
  const settings = { theme: "light", lang, tray: "panel", currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (body) => route.fulfill({ json: body });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") { await gate.held; return json(data.quotas); }
    if (url.pathname === "/api/usage/quotas/history/holds") return json([]);
    if (url.pathname === "/api/usage/quotas/history") { await gate.held; return json(data.history); }
    if (url.pathname === "/api/usage") return json({ calls: 0, input: 0, output: 0, cost: 0, agents: [], accounts: [], models: [], series: [], days: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: opening Usage draws its cards once, after the click`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) await t.test(lang, async () => {
      const context = await browser.newContext({ viewport: { width: 1280, height: 820 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(10000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const gate = { held: Promise.resolve() };
      await page.route("**/*", serve(lang, fixtures(Date.now()), gate));
      await page.goto("http://magpie.test/");
      await page.locator('#nav button[data-view="usage"]').click();
      await page.locator(".subscription-card .quota-curve").first().waitFor();
      // both accounts share Codex's card, the second's curve behind its More
      const cards = await page.locator(".subscription-card").count();
      const curves = await page.locator(".subscription-card .quota-curve").count();
      assert.ok(cards >= 1 && curves >= 1);

      // drawn again from the same read of the history: no reading parsed again
      const parsed = await page.evaluate(() => {
        const parse = Date.parse; let n = 0;
        Date.parse = (s) => { n++; return parse(s); };
        try { renderQuotas(true); } finally { Date.parse = parse; }
        return n;
      });
      assert.ok(parsed < 100, `drawing the cards again parsed ${parsed} dates; the 3840 readings were parsed already`);

      // away and back, with the reads that go with the open held
      await page.locator('#nav button[data-view="agents"]').click();
      let release;
      gate.held = new Promise((r) => { release = r; });
      await page.evaluate(() => { for (const c of document.querySelectorAll(".subscription-card")) c.dataset.drawnBefore = "1"; });
      await page.locator('#nav button[data-view="usage"]').click();
      await page.locator("#view-usage").waitFor();
      assert.equal(await page.locator('.subscription-card[data-drawn-before="1"]').count(), cards, "the cards drawn before stay until the read answers");
      release();
      await page.waitForFunction((n) => document.querySelectorAll(".subscription-card").length === n && !document.querySelector('.subscription-card[data-drawn-before="1"]'), cards);
      assert.equal(await page.locator(".subscription-card .quota-curve").count(), curves, "the answer draws them again, curves and all");
      assert.deepEqual(errors, []);
      await context.close();
    });
  });
}
