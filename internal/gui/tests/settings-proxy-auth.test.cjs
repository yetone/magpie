// Run with Node's test runner and Playwright on the module path; see README.md.
// Optional SOCKS credentials survive saving/reloading, never appear in the
// address or status, and typing between fields cannot save a partial login.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
function server(lang, posts, proxy = "") {
  let cur = {
    theme: "light", lang, tray: "panel", currency: "usd", textSize: 100,
    proxy, proxyNow: proxy, proxySource: proxy ? "settings" : "none",
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
        cur = { ...cur, ...body, proxyNow: body.proxy, proxySource: body.proxy === "direct" ? "off" : body.proxy ? "settings" : "none" };
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

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: optional SOCKS credentials in settings`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const context = await browser.newContext({ viewport: { width: 1100, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(4000);
      const posts = [], errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, posts));
      await page.goto("http://magpie.test/?view=settings&tab=network");
      const box = page.locator("#proxySegs");
      const address = box.locator("input.proxy");
      const user = box.getByRole("textbox", { name: lang === "zh" ? "用户名（可选）" : "Username (optional)", exact: true });
      const pass = box.getByLabel(lang === "zh" ? "密码（可选）" : "Password (optional)", { exact: true });
      const saved = async (action, want) => {
        const count = posts.length;
        const response = page.waitForResponse((r) => r.url().endsWith("/api/settings") && r.request().method() === "POST");
        await action();
        await response;
        await page.waitForFunction(() => document.querySelector("#status")?.textContent.includes("Saved") || document.querySelector("#status")?.textContent.includes("已保存"));
        assert.equal(posts.length, count + 1, "save once for the whole proxy");
        assert.equal(posts.at(-1).proxy, want);
      };

      await box.getByRole("button", { name: lang === "zh" ? "自定义" : "Custom", exact: true }).click();
      await address.fill("socks5://127.0.0.1:1080");
      await user.fill("a@b:/% 用户");
      await user.press("Tab");
      assert.equal(posts.length, 0, "tabbing from username to password must not save half a login");
      await pass.fill("p@ss:/% #密码");
      assert.equal(await pass.getAttribute("type"), "password");
      const encoded = "socks5://a%40b%3A%2F%25%20%E7%94%A8%E6%88%B7:p%40ss%3A%2F%25%20%23%E5%AF%86%E7%A0%81@127.0.0.1:1080";
      await saved(async () => {
        // WebKit may skip Save when tabbing, leaving the editor and saving
        // now. If Tab stays inside, clicking outside saves the same login.
        await pass.press("Tab");
        await page.locator("#proxySub").click();
      }, encoded);
      await page.reload();
      assert.equal(await address.inputValue(), "socks5://127.0.0.1:1080");
      assert.equal(await user.inputValue(), "a@b:/% 用户");
      assert.equal(await pass.inputValue(), "p@ss:/% #密码");
      assert(!((await page.locator("#proxySub").innerText()).includes("p%40ss")), "status exposes the encoded password");
      assert(!((await page.locator("#proxySub").innerText()).includes("p@ss")), "status exposes the password");

      for (const width of [1100, 560, 375]) {
        await page.setViewportSize({ width, height: 760 });
        for (const field of [address, user, pass]) {
          const rect = await field.boundingBox();
          assert(rect && rect.x >= 0 && rect.x + rect.width <= width, `proxy field outside ${width}px window`);
        }
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-proxy-${width}.png`) });
        }
      }

      await user.fill("");
      await pass.fill("");
      await saved(() => pass.press("Enter"), "socks5://127.0.0.1:1080");
      await page.reload();
      assert.equal(await user.inputValue(), "");
      assert.equal(await pass.inputValue(), "");

      // Pasting an existing URL separates and decodes the login, including IPv6.
      await address.fill("socks5h://name:p%40ss@[::1]:1080");
      assert.equal(await address.inputValue(), "socks5h://[::1]:1080");
      assert.equal(await user.inputValue(), "name");
      assert.equal(await pass.inputValue(), "p@ss");
      await saved(() => pass.press("Enter"), "socks5h://name:p%40ss@[::1]:1080");
      await pass.fill("discard-me");
      const beforeEscape = posts.length;
      await pass.press("Escape");
      assert.equal(await pass.inputValue(), "p@ss");
      assert.equal(posts.length, beforeEscape, "Escape must discard, not save, the draft");

      // Arbitrary encoded bytes in an existing password remain masked and intact.
      await address.fill("socks5://name:p%FFss@127.0.0.1:1080");
      assert.equal(await address.inputValue(), "socks5://127.0.0.1:1080");
      assert.equal(await pass.inputValue(), "p%FFss");
      await saved(() => box.getByRole("button", { name: lang === "zh" ? "保存" : "Save", exact: true }).click(), "socks5://name:p%FFss@127.0.0.1:1080");

      // HTTP and bare addresses still work; a hidden SOCKS login isn't attached.
      await address.fill("http://127.0.0.1:7890");
      assert.equal(await user.isVisible(), false);
      await saved(() => address.press("Enter"), "http://127.0.0.1:7890");
      await address.fill("127.0.0.1:7891");
      await saved(() => page.locator("#proxySub").click(), "127.0.0.1:7891");

      await address.fill("socks5://127.0.0.1:1080");
      await user.fill("unsaved-user");
      await pass.fill("unsaved-password");
      await saved(() => box.getByRole("button", { name: lang === "zh" ? "关闭" : "Off", exact: true }).click(), "direct");
      await saved(() => box.getByRole("button", { name: lang === "zh" ? "自动" : "Auto", exact: true }).click(), "");
      assert.deepEqual(errors, []);
    });
  }
}
