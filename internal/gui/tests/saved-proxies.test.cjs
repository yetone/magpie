// Run with Node's test runner and Playwright on the module path; see README.md.
// Named proxies (@tsingchao on X): Settings > Network keeps a list of them,
// each with a Test, and the global proxy picks one as "@id" in a Saved mode.
// A saved proxy in use can't be deleted; a password is typed, never shown.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
function server(lang, posts) {
  let cur = {
    theme: "light", lang, tray: "panel", currency: "usd", textSize: 100,
    proxy: "", proxyNow: "", proxySource: "off", pluginProxy: "",
    trayUsageEvery: 3, version: "test", dir: "/test", lanURLs: [],
    visionModels: [], imageGenModels: [], workbuddyCheckins: [],
    proxies: [
      { id: "px1", name: "Office", url: "http://10.0.0.1:8080", user: "me", hasPass: true, uses: [{ where: "plugins" }] },
      { id: "px2", name: "Home", url: "socks5://192.168.1.2:1080" },
    ],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: true })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: cur.fx });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") { const body = req.postDataJSON(); posts.push(body); cur = { ...cur, ...body, proxies: cur.proxies }; }
      return json(cur);
    }
    if (url.pathname === "/api/proxies/test") { posts.push({ test: req.postDataJSON().id }); return json({ ok: true, ms: 42, at: 1 }); }
    if (url.pathname === "/api/proxies/save") {
      const b = req.postDataJSON();
      posts.push({ save: b });
      cur = { ...cur, proxies: [...cur.proxies, { id: "px3", name: b.name, url: b.url, user: b.user, hasPass: !!b.pass }] };
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
  en: { test: "Test", add: "Add proxy", name: "Name", save: "Save", del: "Delete", saved: "Saved", ok: "Reachable, 42 ms", pass: "Password (optional)" },
  zh: { test: "测试", add: "添加代理", name: "名称", save: "保存", del: "删除", saved: "已保存", ok: "可连接，42 毫秒", pass: "密码（可选）" },
  "zh-TW": { test: "測試", add: "新增代理", name: "名稱", save: "儲存", del: "刪除", saved: "已儲存", ok: "可連線，42 毫秒", pass: "密碼（選填）" },
  ja: { test: "テスト", add: "プロキシを追加", name: "名前", save: "保存", del: "削除", saved: "保存しました", ok: "接続できます、42 ms", pass: "パスワード（任意）" },
  de: { test: "Testen", add: "Proxy hinzufügen", name: "Name", save: "Speichern", del: "Löschen", saved: "Gespeichert", ok: "Erreichbar, 42 ms", pass: "Passwort (optional)" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(labels)) {
    const l = labels[lang];
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: saved proxies are listed, tested and chosen`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(4000);
        page.setDefaultNavigationTimeout(60000);
        const posts = [], errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posts));
        await page.goto("http://magpie.test/?view=settings&tab=network");
        const list = page.locator("#proxiesList");
        await list.getByText("Office", { exact: true }).waitFor();
        const rows = list.locator(".saved-proxy");
        assert.equal(await rows.count(), 2);
        // the address shows without the password, and one in use can't be deleted
        const first = await rows.nth(0).textContent();
        assert(first.includes("10.0.0.1:8080") && !first.includes("hunter"), first);
        assert.equal(await rows.nth(0).getByRole("button", { name: l.del, exact: true }).isDisabled(), true);
        assert.equal(await rows.nth(1).getByRole("button", { name: l.del, exact: true }).isDisabled(), false);

        // Test shows the answer
        await rows.nth(1).getByRole("button", { name: l.test, exact: true }).click();
        await rows.nth(1).getByText(l.ok).waitFor();

        // adding: a blank password isn't sent as one
        await list.getByRole("button", { name: l.add, exact: true }).click();
        await list.getByLabel(l.name, { exact: true }).fill("Lab");
        await list.getByLabel(l.pass, { exact: true }).fill("s3cret");
        await list.locator("input").nth(1).fill("http://lab:3128");
        await list.getByRole("button", { name: l.save, exact: true }).click();
        await list.getByText("Lab", { exact: true }).waitFor();
        const save = posts.find((p) => p.save).save;
        assert.deepEqual([save.name, save.url, save.pass], ["Lab", "http://lab:3128", "s3cret"]);
        assert.equal(await page.evaluate(() => document.body.textContent.includes("s3cret")), false, "the password is never shown");

        // the global proxy picks one as @id
        const global = page.locator("#proxySegs");
        const response = page.waitForResponse((r) => r.url().endsWith("/api/settings") && r.request().method() === "POST");
        await global.getByRole("button", { name: l.saved, exact: true }).click();
        await response;
        assert.equal(posts.filter((p) => "proxy" in p).at(-1).proxy, "@px1");
        await global.locator("button.proxy-saved").getByText("Office", { exact: true }).waitFor();

        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert(over <= 0, `the page scrolls sideways by ${over}px`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
