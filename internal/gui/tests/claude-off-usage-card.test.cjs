// Run with Node's test runner and Playwright on the module path; see README.md.
// Kunlin03 on X: a Claude Pro account added in magpie, the Claude Code switch
// off, said "No allowance reported" on its card. The gateway now answers an
// account of a switched-off provider with its last reading, or, never read,
// that it is switched off (login_usage.go); the card says so in the user's
// language, not "No allowance reported" or "Allowance unavailable".
// Chromium and WebKit, every language. No backend; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const claude = () => ({
  id: "claude", name: "Claude Code", icon: "claude-color", chat: "", responses: "", anthropic: "", catalog: "", routing: "",
  models: [], agents: [], fallback: [], headers: {}, keyList: [],
  account: {
    agent: "claude", agentName: "Claude Code", agentIcon: "claude-color", user: "pro@example.com", plan: "Pro",
    logins: [{ user: "pro@example.com", plan: "Pro", active: true, on: true }],
  },
});
const serve = (lang, web) => async (route) => {
  const url = new URL(route.request().url());
  const json = (data) => route.fulfill({ json: data });
  if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"dark",web:${web}};` });
  if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
  if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "dark" } });
  if (url.pathname === "/api/providers") return json({ providers: [claude()], presets: [], excluded: [], gateway: { running: true, window: true } });
  if (url.pathname === "/api/groups") return json({ groups: [] });
  if (url.pathname === "/api/plugins") return json({ plugins: [] });
  if (url.pathname === "/api/login/usage") return json({
    "pro@example.com": { provider: "claude", plan: "Pro", windows: [], error: "not read yet: this subscription is switched off in magpie, so nothing is read for it" },
  });
  if (url.pathname.startsWith("/api/")) return json({});
  const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
  try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
};

const want = { en: "Switched off, so no allowance is read — switch it on and refresh", zh: "已关闭,不读取额度 — 打开开关后再刷新", "zh-TW": "已關閉,不讀取額度 — 開啟開關後再重新整理", ja: "オフのため利用枠を読みません — オンにして更新してください", de: "Ausgeschaltet, das Kontingent wird nicht gelesen — einschalten und neu laden" };
const langs = ["en", "zh", "zh-TW", "ja", "de"];
for (const [engine, browserType] of [["chromium", chromium], ["webkit", webkit]]) {
  test(engine + ": a switched-off Claude account's card says it is switched off", async (t) => {
    const browser = await browserType.launch();
    t.after(() => browser.close());
    for (const width of [1000, 390]) for (const lang of langs) {
      const at = `${lang} ${width}`;
      const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      await page.route("**/*", serve(lang, false));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Claude Code" }).first().click();
      const line = page.locator('.editor .accts .acc[data-account-id="pro@example.com"] .aq .aq-none');
      await line.waitFor();
      assert.equal((await line.textContent()).trim(), want[lang === "en" ? "en" : lang], at);
      await context.close();
    }
  });
}
