// Run with Node's test runner and Playwright on the module path; see README.md.
// App updates' proxy (JonahFu on X: GitHub needs a proxy, the vendors
// don't): Settings › Network has a row of its own under Proxy. Direct,
// Custom with a login, and Same as above each save updateProxy alone, the
// global proxy as it was; Custom again brings back the last address, which
// the server remembers as updateProxyCustom.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
function server(lang, posts) {
  let cur = {
    theme: "light", lang, tray: "panel", currency: "usd", textSize: 100,
    proxy: "direct", proxyNow: "", proxySource: "off", pluginProxy: "", updateProxy: "",
    trayUsageEvery: 3, version: "test", dir: "/test", lanURLs: [],
    visionModels: [], imageGenModels: [], workbuddyCheckins: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: true })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: cur.fx });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") {
        const body = req.postDataJSON();
        posts.push(body);
        // as settings.Save: the custom copy is the server's, set from the value
        const own = body.updateProxy && body.updateProxy !== "direct";
        cur = { ...cur, ...body, updateProxyCustom: own ? body.updateProxy : cur.updateProxyCustom };
      }
      return json(cur);
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const labels = {
  en: { row: "App updates", same: "Same as above", direct: "Direct", custom: "Custom", user: "Username (optional)", pass: "Password (optional)", saved: "Saved" },
  zh: { row: "应用更新", same: "同上", direct: "直连", custom: "自定义", user: "用户名（可选）", pass: "密码（可选）", saved: "已保存" },
  "zh-TW": { row: "應用程式更新", same: "同上", direct: "直連", custom: "自定義", user: "使用者名稱（選填）", pass: "密碼（選填）", saved: "已儲存" },
  ja: { row: "アプリのアップデート", same: "上と同じ", direct: "直接接続", custom: "カスタム", user: "ユーザー名（任意）", pass: "パスワード（任意）", saved: "保存しました" },
  de: { row: "App-Updates", same: "Wie oben", direct: "Direkt", custom: "Benutzerdefiniert", user: "Benutzername (optional)", pass: "Passwort (optional)", saved: "Gespeichert" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(labels)) {
    const l = labels[lang];
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: app updates take a proxy of their own`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const context = await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(4000);
        page.setDefaultNavigationTimeout(60000);
        const posts = [], errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posts));
        await page.goto("http://magpie.test/?view=settings&tab=network");
        const row = page.locator("#updateProxyRow");
        await row.getByText(l.row, { exact: true }).waitFor();
        const box = page.locator("#updateProxySegs");
        const mode = (name) => box.getByRole("button", { name, exact: true });
        const saved = async (action, want) => {
          const count = posts.length;
          const response = page.waitForResponse((r) => r.url().endsWith("/api/settings") && r.request().method() === "POST");
          await action();
          await response;
          await page.waitForFunction((word) => document.querySelector("#status")?.textContent.includes(word), l.saved);
          assert.equal(posts.length, count + 1, "save once");
          assert.equal(posts.at(-1).updateProxy, want);
          assert.equal(posts.at(-1).proxy, "direct", "the global proxy stays as it was");
          assert.equal(posts.at(-1).pluginProxy, "", "plugin downloads stay as they were");
          assert(!("updateProxyCustom" in posts.at(-1)), "the page sends the remembered proxy");
        };
        // its row starts on Same as above, Proxy's own on Off
        assert.equal(await mode(l.same).getAttribute("aria-pressed"), "true");
        assert.equal(await box.locator("input.proxy").count(), 0, "no address until Custom");

        await mode(l.custom).click();
        const address = box.locator("input.proxy");
        await address.fill("http://127.0.0.1:7890");
        await box.getByRole("textbox", { name: l.user, exact: true }).fill("me");
        await box.getByLabel(l.pass, { exact: true }).fill("p@ss");
        await saved(() => box.getByLabel(l.pass, { exact: true }).press("Enter"), "http://me:p%40ss@127.0.0.1:7890");
        // the line under the name shows the address without the login
        const sub = await page.locator("#updateProxySub").textContent();
        assert(sub.includes("127.0.0.1:7890") && !sub.includes("p%40ss"), sub);

        await saved(() => mode(l.direct).click(), "direct");
        await mode(l.custom).click();
        assert.equal(await address.inputValue(), "http://127.0.0.1:7890", "Custom brings back the last address");
        await address.press("Escape");
        await saved(() => mode(l.same).click(), "");

        // the global proxy row is drawn as before, its Test with it
        assert.equal(await page.locator("#proxySegs button.proxy-test").count(), 1);
        assert.equal(await page.locator("#updateProxySegs button.proxy-test").count(), 0);
        assert.equal(await page.locator("#pluginProxySegs button.proxy-test").count(), 0);
        // nothing is wider than the page
        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert(over <= 0, `the page scrolls sideways by ${over}px`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
