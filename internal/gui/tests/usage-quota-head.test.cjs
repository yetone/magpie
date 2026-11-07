// Run with Node's test runner and Playwright on the module path; see README.md.
// #809 (Hu9956: 用量界面红框这行会延迟几秒才会出现 — 点到其他界面比如Agent，
// 再点回用量界面就能复现): the allowances' head (Allowances, the trend, the
// masking, Used/Left) stays over the cards it heads while the period's
// usage loads again, rather than being hidden until it comes in, and a
// period with no calls doesn't hide it. #808 (Hu9956: TraeCN自动签到失效):
// a failed check-in says why in its row, not only in its tooltip. English
// and Chinese; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const today = new Date(Date.now() + 8 * 3600e3).toISOString().slice(0, 10);
const quotas = [
  { provider: "codex", name: "Codex", icon: "openai", plan: "Plus", windows: [{ name: "5 hours", used: 20 }] },
  { provider: "trae-cn", name: "Trae CN", plan: "Free", user: "hu", windows: [{ name: "Credits", used: 3 }],
    checkins: true, checkinBy: "trae", checkin: { user: "hu", day: today, outcome: "failed", msg: "code 9074: 当前参与用户太多，请稍后再试（今日签到人数较多，活动火爆，请耐心等待）" } },
];

function serve(lang, gate) {
  const settings = { theme: "light", lang, quotaLeft: false, currency: "usd", traeCheckin: true };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") { await gate.quotas; return json(quotas); }
    if (url.pathname === "/api/usage") { await gate.usage; return json({ calls: gate.calls, input: 10, output: 5, cost: 0, agents: [], models: [], days: [] }); }
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: the allowances' head stays while the page loads again`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 900 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const gate = { usage: Promise.resolve(), quotas: Promise.resolve(), calls: 3 };
      await page.route("**/*", serve(lang, gate));
      await page.goto("http://magpie.test/?view=usage");
      const head = page.locator("#quotaHead");
      await page.locator(".subscription-card", { hasText: "Trae CN" }).waitFor();
      await head.waitFor();
      // the failed check-in's reason is in the row
      const say = (await page.locator(".wb-checkin .ci-say").innerText()).trim();
      assert.equal(say, (lang === "en" ? "Check-in failed; magpie tries again later" : "签到失败，magpie 稍后重试") + " · code 9074: 当前参与用户太多，请稍后再试（今日签到人数较多，活动火爆，请耐心等待）");
      // and all of it is seen, not cut off with "…" (#821)
      const clipped = await page.locator(".wb-checkin .ci-say > span").evaluate((s) => s.scrollWidth > s.clientWidth + 1 || s.getBoundingClientRect().right > s.closest(".subscription-card").getBoundingClientRect().right);
      assert.equal(clipped, false, "the check-in's reason is cut off");

      // away and back, with the vendors and the log both slow to answer
      let free;
      gate.usage = new Promise((r) => (free = r));
      gate.quotas = gate.usage;
      await page.click('nav button[data-view="agents"], button[data-view="agents"]');
      await page.click('button[data-view="usage"]');
      // the page shown before stays while it reads again (jiabubu on Discord)
      await page.waitForFunction(() => document.querySelector("#usageReload.reading"));
      assert.equal(await head.isVisible(), true, "the head went while the page loaded");
      assert.equal(await page.locator(".subscription-card", { hasText: "Codex" }).isVisible(), true);
      free();
      await page.waitForFunction(() => !document.querySelector("#usageReload.reading, #view-usage.loading"));
      assert.equal(await head.isVisible(), true);

      // a period without calls keeps it over its cards
      gate.calls = 0;
      await page.click('button[data-view="agents"]');
      await page.click('button[data-view="usage"]');
      await page.waitForFunction(() => document.querySelector("#stats.empty"));
      assert.equal(await head.isVisible(), true, "no calls hid the allowances' head");
      assert.deepEqual(errors, []);
    });
  }
}
