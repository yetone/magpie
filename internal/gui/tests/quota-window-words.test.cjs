// Run with Node's test runner and Playwright on the module path; see README.md.
// yi_time on Discord: a plugin names and counts its usage windows in
// English (WorkBuddy's "Credits" and "Extra credits", in "credits"), and
// the Usage page's card says them in the page's language, in every
// language magpie has, as the tray's tooltip does (traywords.go). No
// backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const quotas = [
  { provider: "workbuddy", name: "WorkBuddy", icon: "workbuddy-color", plan: "Pro", user: "李雷", windows: [
    { name: "Credits", used: 0, display: "0 / 500", amount: 0, limit: 500, unit: "credits" },
    { name: "Extra credits", used: 10, display: "20 / 200", amount: 20, limit: 200, unit: "credits", aside: true },
  ] },
  { provider: "qoder", name: "Qoder", icon: "qoder", plan: "Pro", user: "me@example.com", windows: [
    { name: "Add-on credits", used: 50, display: "50 / 100", amount: 50, limit: 100, unit: "credits" },
  ] },
];

function serve(lang) {
  const settings = { lang, theme: "light", currency: "usd" };
  const state = { agents: [], profiles: [], settings };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { wb: ["Credits", "Extra credits"], unit: "credits", qoder: "Add-on credits" },
  zh: { wb: ["积分", "额外积分"], unit: "积分", qoder: "加购积分" },
  "zh-TW": { wb: ["積分", "額外積分"], unit: "積分", qoder: "加購積分" },
  ja: { wb: ["クレジット", "追加クレジット"], unit: "クレジット", qoder: "アドオンクレジット" },
  de: { wb: ["Credits", "Zusätzliche Credits"], unit: "Credits", qoder: "Add-on-Credits" },
};
const squash = (s) => s.replace(/\s+/g, " ").trim();

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const [lang, w] of Object.entries(words)) {
      test(`${engine} ${width}px ${lang}: a plugin's window names and units in the page's language`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=usage");

        const card = page.locator(".subscription-card", { hasText: "WorkBuddy" });
        await card.locator(".quota-name").first().waitFor();
        const names = (await card.locator(".quota-name").allTextContents()).map(squash);
        for (const n of w.wb) assert.ok(names.includes(n), `${lang} WorkBuddy names ${JSON.stringify(names)} lack ${n}`);
        const count = squash(await card.locator(".quota-n").first().textContent());
        assert.ok(count.startsWith("0 / 500 " + w.unit), `${lang} count ${count}`);
        const qoder = page.locator(".subscription-card", { hasText: "Qoder" });
        await qoder.locator(".quota-name").first().waitFor();
        assert.equal(squash(await qoder.locator(".quota-name").first().textContent()), w.qoder);
        assert.deepEqual(errors, []);
      });
    }
  }
}
