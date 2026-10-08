// Claude for Microsoft 365 in Settings: turning it on shows the fixed HTTPS
// address, trust action and named key without exposing it; Copy key asks the
// explicit credential route. English and Chinese in Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
function payload(lang, on = false) {
  return {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", proxy: "", proxyNow: "none", proxySource: "none",
    version: "0.1.400", dir: "/config/magpie", gateway: "http://127.0.0.1:3425", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lan: false, lanURLs: [], corsOrigins: on ? ["https://pivot.claude.ai"] : [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false }, m365: on, m365KeyName: on ? "Claude for Microsoft 365" : "",
    m365KeyMasked: on ? "sk-magpie-key-…abcdef" : "", m365Status: { running: on, url: "https://127.0.0.1:8787", certificate: { ready: on, trusted: false, message: "Trust the local certificate before connecting from Microsoft 365" } },
  };
}
function server(lang, calls) {
  let s = payload(lang);
  return async (route) => {
    const u = new URL(route.request().url());
    const json = (data, status = 200) => route.fulfill({ json: data, status });
    if (u.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs={lang:"${lang}",theme:"light"};` });
    if (u.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (u.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: s.fx });
    if (u.pathname === "/api/settings") return json(s);
    if (u.pathname === "/api/settings/m365") { calls.push("toggle"); s = payload(lang, route.request().postDataJSON().on); return json({ settings: s }); }
    if (u.pathname === "/api/settings/m365/key") { calls.push("key"); return json({ secret: "sk-magpie-key-fixture" }); }
    if (u.pathname === "/api/copy") { calls.push("copy:" + route.request().postDataJSON().text); return json({}); }
    if (u.pathname === "/api/plugins") return json({ plugins: [] });
    if (u.pathname === "/api/usage/quotas") return json([]);
    if (u.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (u.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, u.pathname === "/" ? "index.html" : u.pathname);
    return route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
  };
}
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Microsoft 365 HTTPS setup", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch(process.env.CHROME ? { executablePath: process.env.CHROME } : { channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) await t.test(lang, async () => {
      const calls = [], page = await (await browser.newContext({ viewport: { width: 440, height: 650 }, reducedMotion: "reduce" })).newPage();
      await page.route("**/*", server(lang, calls));
      await page.goto("http://127.0.0.1:4999/?view=settings&tab=network");
      const box = page.locator("#m365List");
      await box.getByText("Claude for Microsoft 365", { exact: true }).waitFor();
      await box.getByRole("button", { name: lang === "zh" ? "开启" : "On", exact: true }).click();
      await box.getByText("https://127.0.0.1:8787", { exact: true }).waitFor();
      assert.equal(await box.locator("code").filter({ hasText: "sk-magpie-key" }).textContent(), "sk-magpie-key-…abcdef");
      const trust = lang === "zh" ? "信任证书" : "Trust certificate";
      assert.equal(await box.getByRole("button", { name: trust }).count(), 1);
      await box.getByRole("button", { name: lang === "zh" ? "复制密钥" : "Copy key" }).click();
      await page.waitForFunction(() => document.body.textContent.includes("copied") || document.body.textContent.includes("已复制"));
      assert(calls.includes("key"));
      assert(calls.includes("copy:sk-magpie-key-fixture"));
      assert.equal((await page.locator("html").evaluate((e) => e.scrollWidth <= e.clientWidth)), true);
    });
  });
}
