// Run with Node's test runner and Playwright on the module path; see README.md.
// A ChatGPT account's image allowance (#6160) is a row of its own on the
// Usage card, beside the 5-hour and weekly windows: "used up" with the time
// it comes back when a refusal named it, "unknown" when nothing is known,
// and never a share or a meter. In every language, at the window's width
// and a phone's. No backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const soon = new Date(Date.now() + 3 * 3600 * 1000).toISOString();
const img = (extra) => ({ name: "Image generation", lane: "images", text: true, aside: true, used: 0, display: "Unknown", ...extra });
const chat = [{ name: "5 hours", used: 40, resetSecs: 3600 }, { name: "7 days", used: 10, resetSecs: 86400 }];
const quotas = [
  { provider: "codex", name: "Codex", icon: "codex-color", user: "spent@example.com", windows: [...chat, img({ display: "Used up", resetsAt: soon, resetSecs: 10800 })] },
  { provider: "codex", name: "Codex", icon: "codex-color", user: "fresh@example.com", windows: [...chat, img({})] },
  // a drawing's 200 told the window's renewal (#6412)
  { provider: "codex", name: "Codex", icon: "codex-color", user: "drew@example.com", windows: [...chat, img({ display: "Available", resetsAt: soon, resetSecs: 10800 })] },
];

const words = {
  en: { name: "Image generation", used: "Used up", unknown: "Unknown", ok: "Available" },
  zh: { name: "生图模型", used: "已用完", unknown: "未知", ok: "可用" },
  "zh-TW": { name: "生圖模型", used: "已用完", unknown: "未知", ok: "可用" },
  ja: { name: "画像生成", used: "使い切り", unknown: "不明", ok: "利用可能" },
  de: { name: "Bilderzeugung", used: "Aufgebraucht", unknown: "Unbekannt", ok: "Verfügbar" },
};

function serve(lang) {
  const settings = { theme: "light", lang, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (body) => route.fulfill({ json: body });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/usage") return json({ days: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a ChatGPT account's image row says used up until a time, or unknown, in every language`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const width of [900, 390]) for (const lang of Object.keys(words)) {
      const at = `${lang} ${width}`;
      const errors = [];
      const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=usage&tab=usage");
      await page.locator("#subscriptionUsage .quota").first().waitFor();
      await page.locator(".quota-more").click();
      const w = words[lang];
      for (const [user, text, resets] of [["spent@example.com", w.used, true], ["fresh@example.com", w.unknown, false], ["drew@example.com", w.ok, true]]) {
        const mine = page.locator(`[data-card="codex|${user}"] + .quota-windows`);
        const row = mine.locator(".quota", { hasText: w.name });
        await row.waitFor();
        assert.equal((await row.locator(".quota-n").textContent()).trim(), text, at + " " + user);
        assert.equal(await row.locator(".quota-track").count(), 0, at + " an image row has no meter, nothing told a share");
        assert.equal(await row.locator(".quota-reset").count(), resets ? 1 : 0, at + " " + user + " reset line");
        assert.doesNotMatch(await row.textContent(), /%/, at + " no percent is made up");
      }
      assert.equal(await page.locator('[data-card="codex|spent@example.com"] + .quota-windows .quota-track').count(), 2, at + " the chat windows keep their meters");
      assert.deepEqual(errors, [], at);
      await page.context().close();
    }
  });
}
