// Run with Node's test runner and Playwright on the module path; see README.md.
// A balance in two currencies (gakki on Discord): DeepSeek tells an
// account's CNY and USD at once, "$11.12 · ¥-0.05", and the trend under it
// is the USD's alone (balanceTrend.currency "$"). The curve's legend, its
// readings and its pace are said in dollars, not with the other currency
// stuck on each ("$11.12 · ¥-0.05" for every reading). English and Chinese,
// light and dark, at a narrow width; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const H = 3600e3;
const iso = (ms) => new Date(ms).toISOString();

function fixtures(now) {
  // the USD spent from $12 to $11.12 over two days; the CNY sits at -0.05
  const points = [[48, 12], [36, 11.8], [24, 11.6], [12, 11.35], [0, 11.12]].map(([h, v]) => ({ at: iso(now - h * H), amount: v }));
  return [
    { provider: "deepseek", name: "DeepSeek", icon: "deepseek", windows: [], balance: "$11.12 · ¥-0.05", readAt: iso(now),
      balanceTrend: { points, currency: "$", fitFrom: iso(now - 48 * H), fitStart: 12, fitNow: 11.12, perDay: 0.44 } },
  ];
}

function serve(lang, theme, quotas) {
  const settings = { theme, lang, tray: "panel", quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (body) => route.fulfill({ json: body });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"${theme}",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { balance: "Balance", pace: "about $0.44 a day", head: "Latest readings:" },
  zh: { balance: "余额", pace: "约每天 $0.44", head: "最近的读数：" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a balance in two currencies draws the first one's trend in its own sign`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const errors = [];
      for (const theme of ["light", "dark"]) {
        for (const width of [900, 440]) {
          const page = await (await browser.newContext({ viewport: { width, height: 560 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, theme, fixtures(Date.now())));
          await page.goto("http://magpie.test/?view=usage");
          const card = page.locator(".subscription-card", { hasText: "DeepSeek" });
          const curve = card.locator(".balance-curve");
          await curve.waitFor();
          assert.ok((await card.textContent()).includes("$11.12 · ¥-0.05"), "the card still says both");
          assert.deepEqual(await curve.locator(".qc-key").allTextContents(), [w.balance + "$11.12", w.pace]);
          const tip = (await curve.locator(".qc-key").first().getAttribute("title")).split("\n");
          assert.equal(tip[0], w.head);
          assert.equal(tip.length, 6);
          for (const line of tip.slice(1)) assert.match(line, /^\$\d+\.\d\d · /, `a reading in dollars alone: ${line}`);
          assert.ok(tip[1].startsWith("$11.12 · "), tip[1]);
          assert.equal(await curve.locator("path.qc-line").count(), 1);
          const cb = await card.boundingBox(), pb = await curve.locator(".qc-plot").boundingBox();
          assert.ok(pb.x >= cb.x && pb.x + pb.width <= cb.x + cb.width + 0.5, `the plot fits its card at ${width}px`);
          await page.context().close();
        }
      }
      assert.deepEqual(errors, []);
    });
  }
}
